package server

import (
	"context"
	"os/exec"
	"strings"

	"github.com/tgruben-circuit/percy/cluster"
)

// startClusterMonitor starts the orchestrator monitor if this node is the orchestrator.
func (s *Server) startClusterMonitor() {
	if s.clusterNode == nil || !s.clusterNode.IsOrchestrator() {
		return
	}

	workingBranch := detectWorkingBranch(s.toolSetConfig.WorkingDir)
	if workingBranch == "" {
		s.logger.Warn("Could not detect working branch, skipping cluster monitor")
		return
	}

	orch := cluster.NewOrchestrator(s.clusterNode)
	orch.SetWorkingBranch(workingBranch)

	mw, err := cluster.NewMergeWorktree(s.toolSetConfig.WorkingDir, s.clusterNode.Config.AgentID, workingBranch)
	if err != nil {
		s.logger.Error("Failed to create merge worktree", "error", err)
		return
	}

	var resolver cluster.ConflictResolver
	llmService, err := s.llmManager.GetService(s.defaultModel)
	if err == nil {
		resolver = cluster.NewLLMConflictResolver(llmService)
	}

	mon := cluster.NewMonitor(s.clusterNode, orch, mw, resolver)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-s.shutdownCh
		cancel()
		mw.Cleanup()
	}()
	go mon.Run(ctx)

	s.logger.Info("Cluster monitor started", "branch", workingBranch)
}

func detectWorkingBranch(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		return "" // detached HEAD
	}
	return branch
}
