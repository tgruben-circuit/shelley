package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tgruben-circuit/percy/llm"
	"github.com/tgruben-circuit/percy/a2a"
)

// A2ABackend adapts *Server to a2a.Backend.
type A2ABackend struct {
	server *Server
	runner *SubagentRunner
}

// NewA2ABackend wires a SubagentRunner-backed A2A adapter.
func NewA2ABackend(s *Server) *A2ABackend {
	return &A2ABackend{server: s, runner: NewSubagentRunner(s)}
}

// NewConversation creates a fresh root conversation visible in the sidebar,
// tagged with an "a2a-" slug so the UI can distinguish it.
func (b *A2ABackend) NewConversation(ctx context.Context, cwd string) (string, error) {
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		} else {
			cwd = "/"
		}
	}
	slug := fmt.Sprintf("a2a-%d", time.Now().UnixNano())
	conv, err := b.server.db.CreateConversation(ctx, &slug, false, &cwd, nil)
	if err != nil {
		return "", err
	}
	b.server.publishConversationListUpdate(ConversationListUpdate{Type: "update", Conversation: conv})
	return conv.ConversationID, nil
}

// SendMessage delivers a prompt and blocks until the agent finishes.
func (b *A2ABackend) SendMessage(ctx context.Context, conversationID, prompt string, timeout time.Duration) (string, error) {
	return b.runner.RunSubagent(ctx, conversationID, prompt, true, timeout, "")
}

// Cancel aborts the in-flight turn.
func (b *A2ABackend) Cancel(ctx context.Context, conversationID string) error {
	b.server.mu.Lock()
	mgr, ok := b.server.activeConversations[conversationID]
	b.server.mu.Unlock()
	if !ok {
		return fmt.Errorf("conversation not active: %s", conversationID)
	}
	return mgr.CancelConversation(ctx)
}

// SubscribeMessages returns a channel that emits each new agent text message
// produced during the next turn. The channel closes when the agent stops.
func (b *A2ABackend) SubscribeMessages(ctx context.Context, conversationID string) (<-chan string, error) {
	mgr, err := b.server.getOrCreateSubagentConversationManager(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	out := make(chan string, 16)
	go func() {
		defer close(out)
		next := mgr.subpub.Subscribe(ctx, 0)
		for {
			sr, ok := next()
			if !ok {
				return
			}
			if sr.Heartbeat {
				continue
			}
			for _, m := range sr.Messages {
				text := extractAgentText(m)
				if text == "" {
					continue
				}
				select {
				case out <- text:
				case <-ctx.Done():
					return
				}
			}
			// Stop streaming once the agent has finished this turn.
			if sr.ConversationState != nil && !sr.ConversationState.Working {
				return
			}
		}
	}()
	return out, nil
}

func extractAgentText(m APIMessage) string {
	if m.LlmData == nil {
		return ""
	}
	var lm llm.Message
	if err := json.Unmarshal([]byte(*m.LlmData), &lm); err != nil {
		return ""
	}
	if lm.Role != llm.MessageRoleAssistant {
		return ""
	}
	var parts []string
	for _, c := range lm.Content {
		if c.Type == llm.ContentTypeText && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// MountA2A registers A2A routes on the given mux if PERCY_A2A_TOKEN is set.
// Returns true if the endpoints were mounted.
func (s *Server) MountA2A(mux *http.ServeMux) bool {
	token := os.Getenv("PERCY_A2A_TOKEN")
	if token == "" {
		return false
	}
	backend := NewA2ABackend(s)
	publicURL := os.Getenv("PERCY_A2A_URL")
	if publicURL == "" {
		publicURL = "http://localhost:8080/a2a"
	}
	card := a2a.AgentCard{
		ProtocolVersion:    "0.2.5",
		Name:               "Percy",
		Description:        "Percy coding agent (multi-model, multi-modal, tool-using).",
		URL:                publicURL,
		Version:            "0.1.0",
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []a2a.AgentSkill{{
			ID:          "coding",
			Name:        "General coding assistance",
			Description: "Reads/writes files, runs shell commands, navigates code, executes tasks.",
			Tags:        []string{"code", "shell", "refactor"},
		}},
		SecuritySchemes: map[string]any{
			"bearer": map[string]any{"type": "http", "scheme": "bearer"},
		},
		Security: []map[string][]any{{"bearer": {}}},
	}
	h := a2a.New(backend, card, token)
	mux.Handle("/.well-known/agent-card.json", h)
	mux.Handle("/a2a", h)
	s.logger.Info("A2A endpoints mounted", "url", publicURL)
	return true
}
