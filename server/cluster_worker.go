package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/tgruben-circuit/percy/cluster"
	"github.com/tgruben-circuit/percy/db"
	"github.com/tgruben-circuit/percy/llm"
)

// startClusterWorker starts the background task watcher if in cluster mode.
func (s *Server) startClusterWorker() {
	if s.clusterNode == nil {
		return
	}

	handler := func(ctx context.Context, task cluster.Task) cluster.TaskResult {
		return s.executeClusterTask(ctx, task)
	}

	worker := cluster.NewWorker(s.clusterNode, handler)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-s.shutdownCh
		cancel()
	}()
	go worker.Run(ctx)
	s.logger.Info("Cluster worker started", "agent", s.clusterNode.Config.AgentID)
}

func (s *Server) executeClusterTask(ctx context.Context, task cluster.Task) cluster.TaskResult {
	agentID := s.clusterNode.Config.AgentID
	taskID := task.ID
	branchName := fmt.Sprintf("agent/%s/%s", agentID, taskID)

	// 1. Create git worktree
	worktreeDir, err := s.createWorktree(ctx, task, branchName)
	if err != nil {
		s.logger.Error("Failed to create worktree", "task", taskID, "error", err)
		return cluster.TaskResult{Summary: fmt.Sprintf("worktree creation failed: %v", err)}
	}
	defer s.cleanupWorktree(worktreeDir)

	// 2. Create conversation
	slug := fmt.Sprintf("task-%s", taskID)
	cwd := worktreeDir
	modelID := s.defaultModel
	conv, err := s.db.CreateConversation(ctx, &slug, false, &cwd, &modelID)
	if err != nil {
		s.logger.Error("Failed to create conversation", "task", taskID, "error", err)
		return cluster.TaskResult{Summary: fmt.Sprintf("conversation creation failed: %v", err)}
	}

	// 3. Insert system prompt directly into DB
	systemPrompt := fmt.Sprintf(
		"You are a worker agent executing a task from the cluster orchestrator.\n"+
			"You are on branch %s. Do NOT create or switch branches.\n\n"+
			"Your task: %s\n\n%s",
		branchName, task.Title, task.Description,
	)
	systemMsg := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: systemPrompt}},
	}
	if err := s.recordMessageForConversation(ctx, conv.ConversationID, systemMsg, llm.Usage{}, db.MessageTypeSystem); err != nil {
		return cluster.TaskResult{Summary: fmt.Sprintf("system prompt recording failed: %v", err)}
	}

	// 4. Get conversation manager and send task
	manager, err := s.getOrCreateConversationManager(ctx, conv.ConversationID)
	if err != nil {
		return cluster.TaskResult{Summary: fmt.Sprintf("manager creation failed: %v", err)}
	}

	llmService, err := s.llmManager.GetService(modelID)
	if err != nil {
		return cluster.TaskResult{Summary: fmt.Sprintf("llm service failed: %v", err)}
	}

	userMsg := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Execute the task described in the system prompt."}},
	}
	if _, err := manager.AcceptUserMessage(ctx, llmService, modelID, userMsg); err != nil {
		return cluster.TaskResult{Summary: fmt.Sprintf("accept message failed: %v", err)}
	}

	// 5. Poll until done (subagent pattern)
	for {
		select {
		case <-ctx.Done():
			return cluster.TaskResult{Summary: "cancelled"}
		case <-time.After(500 * time.Millisecond):
		}

		if !manager.IsAgentWorking() {
			break
		}
	}

	// 6. Get result
	summary := s.getLastAssistantText(ctx, conv.ConversationID)
	return cluster.TaskResult{
		Branch:  branchName,
		Summary: summary,
	}
}

func (s *Server) createWorktree(ctx context.Context, task cluster.Task, branchName string) (string, error) {
	baseBranch := task.Context.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	worktreeDir := filepath.Join("/tmp", "percy-worktree-"+task.ID)

	// Use the server's working directory as the git repo root
	repoDir := s.toolSetConfig.WorkingDir

	// Fetch latest (best-effort)
	fetch := exec.CommandContext(ctx, "git", "fetch", "origin")
	fetch.Dir = repoDir
	fetch.Run()

	// Create worktree with new branch
	cmd := exec.CommandContext(ctx, "git", "worktree", "add", worktreeDir,
		"-b", branchName, "origin/"+baseBranch)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add: %s: %w", string(out), err)
	}

	return worktreeDir, nil
}

func (s *Server) cleanupWorktree(dir string) {
	if err := exec.Command("git", "worktree", "remove", "--force", dir).Run(); err != nil {
		os.RemoveAll(dir)
	}
}

func (s *Server) getLastAssistantText(ctx context.Context, conversationID string) string {
	msg, err := s.db.GetLatestMessage(ctx, conversationID)
	if err != nil {
		return ""
	}
	if msg.LlmData == nil {
		return ""
	}
	var m llm.Message
	if err := json.Unmarshal([]byte(*msg.LlmData), &m); err != nil {
		return ""
	}
	for _, c := range m.Content {
		if c.Type == llm.ContentTypeText && c.Text != "" {
			return c.Text
		}
	}
	return ""
}
