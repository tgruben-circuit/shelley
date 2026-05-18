package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tgruben-circuit/percy/llm"
	"github.com/tgruben-circuit/percy/a2a"
)

// A2ADispatchTool delegates a task to a remote A2A-speaking agent.
type A2ADispatchTool struct{}

const (
	a2aDispatchName        = "a2a_dispatch"
	a2aDispatchDescription = `Delegate a task to a remote agent that speaks the Agent-to-Agent (A2A) protocol.

Use this to route work to specialized agents on the network: a Pi-hosted coding
agent, another Percy instance, or any A2A-compliant service. Blocks until the
remote agent finishes the task and returns its reply.

The "url" is the remote agent's base URL (e.g. "http://pi.local:9000"); the tool
appends /.well-known/agent-card.json and /a2a as needed. Pass "context_id" on
follow-up calls to continue a remote conversation.`
	a2aDispatchInputSchema = `
{
  "type": "object",
  "required": ["url", "prompt"],
  "properties": {
    "url":        {"type": "string",  "description": "Base URL of the remote A2A agent"},
    "token":      {"type": "string",  "description": "Bearer token for the remote agent (optional)"},
    "prompt":     {"type": "string",  "description": "The task to send to the remote agent"},
    "context_id": {"type": "string",  "description": "Existing contextId to continue a remote conversation (optional)"},
    "timeout_seconds": {"type": "integer", "description": "Max wait time in seconds (default 300, max 1800)"}
  }
}
`
)

type a2aDispatchInput struct {
	URL            string `json:"url"`
	Token          string `json:"token,omitempty"`
	Prompt         string `json:"prompt"`
	ContextID      string `json:"context_id,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// Tool returns the llm.Tool wrapper.
func (a *A2ADispatchTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        a2aDispatchName,
		Description: strings.TrimSpace(a2aDispatchDescription),
		InputSchema: llm.MustSchema(a2aDispatchInputSchema),
		Run:         a.Run,
		Deferred:    true,
		Category:    "a2a",
		Concurrent:  true,
	}
}

func (a *A2ADispatchTool) Run(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var req a2aDispatchInput
	if err := json.Unmarshal(m, &req); err != nil {
		return llm.ErrorfToolOut("failed to parse input: %w", err)
	}
	if req.URL == "" {
		return llm.ErrorfToolOut("url is required")
	}
	if req.Prompt == "" {
		return llm.ErrorfToolOut("prompt is required")
	}
	timeout := 300 * time.Second
	if req.TimeoutSeconds > 0 {
		if req.TimeoutSeconds > 1800 {
			req.TimeoutSeconds = 1800
		}
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := a2a.NewClient(req.URL, req.Token)
	task, err := client.SendMessage(callCtx, req.ContextID, req.Prompt)
	if err != nil {
		return llm.ErrorfToolOut("a2a dispatch failed: %w", err)
	}
	text := a2a.ExtractAgentText(task)
	if text == "" {
		text = fmt.Sprintf("[remote agent task %s ended in state %s with no text reply]", task.ID, task.Status.State)
	}
	header := fmt.Sprintf("Remote agent (%s) replied [task=%s context=%s state=%s]:\n", req.URL, task.ID, task.ContextID, task.Status.State)
	return llm.ToolOut{
		LLMContent: llm.TextContent(header + text),
		Display: A2ADispatchDisplayData{
			URL:       req.URL,
			TaskID:    task.ID,
			ContextID: task.ContextID,
			State:     string(task.Status.State),
		},
	}
}

// A2ADispatchDisplayData is the UI-side display payload.
type A2ADispatchDisplayData struct {
	URL       string `json:"url"`
	TaskID    string `json:"task_id"`
	ContextID string `json:"context_id"`
	State     string `json:"state"`
}
