package claudetool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tgruben-circuit/percy/a2a"
	"github.com/tgruben-circuit/percy/llm"
)

// fakeA2AServer is a hand-rolled minimal A2A server for testing the client tool.
// It accepts message/send and echoes the prompt back as the agent reply.
func fakeA2AServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent-card.json", func(w http.ResponseWriter, r *http.Request) {
		card := a2a.AgentCard{
			ProtocolVersion:    "0.2.5",
			Name:               "FakeAgent",
			Description:        "echo agent",
			URL:                "http://test/a2a",
			Version:            "0.0.1",
			DefaultInputModes:  []string{"text/plain"},
			DefaultOutputModes: []string{"text/plain"},
			Skills:             []a2a.AgentSkill{{ID: "echo", Name: "Echo"}},
		}
		_ = json.NewEncoder(w).Encode(card)
	})
	mux.HandleFunc("/a2a", func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var env struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  struct {
				Message a2a.Message `json:"message"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if env.Method != "message/send" {
			http.Error(w, "unsupported: "+env.Method, 400)
			return
		}
		var promptText string
		for _, p := range env.Params.Message.Parts {
			promptText += p.Text
		}
		ctxID := env.Params.Message.ContextID
		if ctxID == "" {
			ctxID = "fake-ctx-1"
		}
		task := a2a.Task{
			Kind:      "task",
			ID:        "fake-task-1",
			ContextID: ctxID,
			Status: a2a.TaskStatus{
				State: a2a.TaskStateCompleted,
				Message: &a2a.Message{
					Kind:      "message",
					MessageID: "reply-1",
					Role:      "agent",
					Parts:     []a2a.Part{{Kind: "text", Text: "echo:" + promptText}},
					ContextID: ctxID,
					TaskID:    "fake-task-1",
				},
			},
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": env.ID, "result": task}
		_ = json.NewEncoder(w).Encode(resp)
	})
	return httptest.NewServer(mux)
}

func TestA2ADispatchEchoes(t *testing.T) {
	ts := fakeA2AServer(t, "")
	defer ts.Close()

	tool := (&A2ADispatchTool{}).Tool()
	if !tool.Deferred || tool.Category != "a2a" || !tool.Concurrent {
		t.Errorf("tool flags wrong: deferred=%v category=%q concurrent=%v", tool.Deferred, tool.Category, tool.Concurrent)
	}

	input, _ := json.Marshal(map[string]any{"url": ts.URL, "prompt": "hello pi"})
	out := tool.Run(context.Background(), input)
	if out.LLMContent == nil {
		t.Fatalf("nil llm content: %+v", out)
	}
	for _, c := range out.LLMContent {
		if c.Type == llm.ContentTypeText && strings.Contains(c.Text, "echo:hello pi") {
			return
		}
	}
	t.Errorf("reply did not contain echo: %+v", out.LLMContent)
}

func TestA2ADispatchPropagatesContext(t *testing.T) {
	ts := fakeA2AServer(t, "")
	defer ts.Close()

	tool := (&A2ADispatchTool{}).Tool()
	input, _ := json.Marshal(map[string]any{"url": ts.URL, "prompt": "x", "context_id": "continued-ctx"})
	out := tool.Run(context.Background(), input)
	display, ok := out.Display.(A2ADispatchDisplayData)
	if !ok {
		t.Fatalf("missing display data: %T", out.Display)
	}
	if display.ContextID != "continued-ctx" {
		t.Errorf("context not echoed back: %q", display.ContextID)
	}
	if display.State != "completed" {
		t.Errorf("state=%q, want completed", display.State)
	}
}

func TestA2ADispatchAuth(t *testing.T) {
	ts := fakeA2AServer(t, "secret")
	defer ts.Close()

	tool := (&A2ADispatchTool{}).Tool()
	// Without token — should fail.
	input, _ := json.Marshal(map[string]any{"url": ts.URL, "prompt": "x"})
	out := tool.Run(context.Background(), input)
	if out.Error == nil || !strings.Contains(out.Error.Error(), "401") {
		t.Errorf("expected 401 error, got %v", out.Error)
	}
	// With token — should succeed.
	input, _ = json.Marshal(map[string]any{"url": ts.URL, "prompt": "x", "token": "secret"})
	out = tool.Run(context.Background(), input)
	if out.Error != nil {
		t.Errorf("expected success with token, got error: %v", out.Error)
	}
}
