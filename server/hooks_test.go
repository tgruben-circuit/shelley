package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tgruben-circuit/percy/claudetool"
	"github.com/tgruben-circuit/percy/loop"
)

// writeHook creates an executable hook script in dir with the given name.
func writeHook(t *testing.T, dir, name, script string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write hook %s: %v", name, err)
	}
}

func TestFindHookIn(t *testing.T) {
	dir := t.TempDir()
	writeHook(t, dir, "system-prompt", "#!/bin/sh\ncat\n")
	// Non-executable file should not be found.
	if err := os.WriteFile(filepath.Join(dir, "chat-message"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if p, err := findHookIn(dir, "system-prompt"); err != nil || p == "" {
		t.Errorf("expected to find system-prompt, got %q err=%v", p, err)
	}
	if p, err := findHookIn(dir, "chat-message"); err != nil || p != "" {
		t.Errorf("non-executable should not be found, got %q err=%v", p, err)
	}
	if p, err := findHookIn(dir, "new-conversation"); err != nil || p != "" {
		t.Errorf("missing hook should return empty, got %q err=%v", p, err)
	}
	if p, err := findHookIn("", "system-prompt"); err != nil || p != "" {
		t.Errorf("empty dir disables hooks, got %q err=%v", p, err)
	}
	if _, err := findHookIn(dir, "../escape"); err == nil {
		t.Error("expected error for path-traversal hook name")
	}
}

func TestRunSystemPromptHookIn(t *testing.T) {
	// No hook installed: prompt returned unchanged.
	if out, err := RunSystemPromptHookIn("", "hello"); err != nil || out != "hello" {
		t.Errorf("no hook: got %q err=%v", out, err)
	}

	dir := t.TempDir()
	writeHook(t, dir, "system-prompt", "#!/bin/sh\necho REPLACED\n")
	out, err := RunSystemPromptHookIn(dir, "original")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "REPLACED" {
		t.Errorf("expected REPLACED, got %q", out)
	}

	// Failing hook aborts.
	failDir := t.TempDir()
	writeHook(t, failDir, "system-prompt", "#!/bin/sh\nexit 1\n")
	if _, err := RunSystemPromptHookIn(failDir, "x"); err == nil {
		t.Error("expected error from failing hook")
	}

	// Empty output is an error for system-prompt (non-empty required).
	emptyDir := t.TempDir()
	writeHook(t, emptyDir, "system-prompt", "#!/bin/sh\ntrue\n")
	if _, err := RunSystemPromptHookIn(emptyDir, "x"); err == nil {
		t.Error("expected error for empty system-prompt output")
	}
}

func TestRunNewConversationHookIn(t *testing.T) {
	// No hook: input echoed back.
	res, err := RunNewConversationHookIn("", NewConversationHookInput{Prompt: "hi", Model: "m", Cwd: "/c"})
	if err != nil || res.Prompt != "hi" || res.Model != "m" || res.Cwd != "/c" {
		t.Fatalf("no hook: got %+v err=%v", res, err)
	}

	dir := t.TempDir()
	// Hook overrides model and slug, leaves prompt/cwd untouched (empty fields ignored).
	writeHook(t, dir, "new-conversation", "#!/bin/sh\necho '{\"model\":\"override\",\"slug\":\"my-slug\"}'\n")
	res, err = RunNewConversationHookIn(dir, NewConversationHookInput{Prompt: "keep", Model: "orig", Cwd: "/w"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Prompt != "keep" || res.Model != "override" || res.Cwd != "/w" || res.Slug != "my-slug" {
		t.Errorf("unexpected result: %+v", res)
	}

	// Empty stdout = no-op.
	noopDir := t.TempDir()
	writeHook(t, noopDir, "new-conversation", "#!/bin/sh\ntrue\n")
	res, err = RunNewConversationHookIn(noopDir, NewConversationHookInput{Prompt: "p", Model: "m"})
	if err != nil || res.Prompt != "p" || res.Model != "m" {
		t.Errorf("noop: got %+v err=%v", res, err)
	}

	// Invalid JSON output aborts.
	badDir := t.TempDir()
	writeHook(t, badDir, "new-conversation", "#!/bin/sh\necho not-json\n")
	if _, err := RunNewConversationHookIn(badDir, NewConversationHookInput{Prompt: "p"}); err == nil {
		t.Error("expected error for invalid JSON output")
	}
}

func TestRunChatMessageHookIn(t *testing.T) {
	// No hook: message unchanged.
	if msg, err := RunChatMessageHookIn("", ChatMessageHookInput{Message: "hello"}); err != nil || msg != "hello" {
		t.Errorf("no hook: got %q err=%v", msg, err)
	}

	dir := t.TempDir()
	writeHook(t, dir, "chat-message", "#!/bin/sh\necho '{\"message\":\"rewritten\"}'\n")
	msg, err := RunChatMessageHookIn(dir, ChatMessageHookInput{Message: "original"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != "rewritten" {
		t.Errorf("expected rewritten, got %q", msg)
	}

	// Empty message field = no-op.
	noopDir := t.TempDir()
	writeHook(t, noopDir, "chat-message", "#!/bin/sh\necho '{\"message\":\"\"}'\n")
	if msg, err := RunChatMessageHookIn(noopDir, ChatMessageHookInput{Message: "keep"}); err != nil || msg != "keep" {
		t.Errorf("noop: got %q err=%v", msg, err)
	}
}

func TestRunEndOfTurnHookIn(t *testing.T) {
	// No hook: no error.
	if err := RunEndOfTurnHookIn("", EndOfTurnHookInput{ConversationID: "c"}); err != nil {
		t.Errorf("no hook: err=%v", err)
	}

	dir := t.TempDir()
	sink := filepath.Join(dir, "captured.json")
	writeHook(t, dir, "end-of-turn", "#!/bin/sh\ncat > "+sink+"\n")
	if err := RunEndOfTurnHookIn(dir, EndOfTurnHookInput{ConversationID: "abc", FinalResponse: "done"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(sink)
	if err != nil {
		t.Fatalf("hook did not capture stdin: %v", err)
	}
	if !strings.Contains(string(data), `"conversation_id":"abc"`) || !strings.Contains(string(data), `"final_response":"done"`) {
		t.Errorf("unexpected captured payload: %s", data)
	}

	// Failing end-of-turn hook returns an error (caller logs it, does not abort).
	failDir := t.TempDir()
	writeHook(t, failDir, "end-of-turn", "#!/bin/sh\nexit 3\n")
	if err := RunEndOfTurnHookIn(failDir, EndOfTurnHookInput{ConversationID: "c"}); err == nil {
		t.Error("expected error from failing end-of-turn hook")
	}
}

// TestNewConversationHookSlugHTTP exercises the new-conversation hook through
// the real HTTP handler: a hook that returns a slug should set it on the new
// conversation (and suppress async slug generation).
func TestNewConversationHookSlugHTTP(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	server := NewServer(database, llmManager, claudetool.ToolSetConfig{}, slog.Default(), true, "", "predictable", "", nil)

	hooksDir := t.TempDir()
	writeHook(t, hooksDir, "new-conversation", "#!/bin/sh\necho '{\"slug\":\"Hook Slug!\"}'\n")
	server.hooksDir = hooksDir

	body, _ := json.Marshal(ChatRequest{Message: "hello", Model: "predictable"})
	req := httptest.NewRequest("POST", "/api/conversations/new", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleNewConversation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	conv, err := database.GetConversationByID(context.Background(), resp.ConversationID)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if conv.Slug == nil || *conv.Slug != "hook-slug" {
		t.Errorf("expected slug hook-slug from hook, got %v", conv.Slug)
	}
}

// TestChatMessageHookRewriteHTTP exercises the chat-message hook: a hook that
// rewrites the message should cause the rewritten text to be recorded.
func TestChatMessageHookRewriteHTTP(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	server := NewServer(database, llmManager, claudetool.ToolSetConfig{}, slog.Default(), true, "", "predictable", "", nil)

	hooksDir := t.TempDir()
	writeHook(t, hooksDir, "chat-message", "#!/bin/sh\necho '{\"message\":\"echo: REWRITTEN\"}'\n")
	server.hooksDir = hooksDir

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	body, _ := json.Marshal(ChatRequest{Message: "original text", Model: "predictable"})
	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleChatConversation(w, req, conversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	msgs, err := database.ListMessages(context.Background(), conversationID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	var found bool
	for _, m := range msgs {
		if m.LlmData != nil && strings.Contains(*m.LlmData, "echo: REWRITTEN") {
			found = true
		}
		if m.LlmData != nil && strings.Contains(*m.LlmData, "original text") {
			t.Errorf("original (un-rewritten) text should not be recorded: %s", *m.LlmData)
		}
	}
	if !found {
		t.Errorf("expected rewritten message to be recorded, messages: %d", len(msgs))
	}
}
