package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tgruben-circuit/percy/db"
	"github.com/tgruben-circuit/percy/db/generated"
	"github.com/tgruben-circuit/percy/llm"
)

func TestFindCritiqueTarget_PicksLastAssistantSkipsCritiques(t *testing.T) {
	mkMsg := func(typ db.MessageType, role llm.MessageRole, text, kind string) generated.Message {
		m := llm.Message{Role: role, Content: []llm.Content{{Type: llm.ContentTypeText, Text: text}}}
		data, _ := json.Marshal(m)
		s := string(data)
		out := generated.Message{Type: string(typ), LlmData: &s}
		if kind != "" {
			ud, _ := json.Marshal(map[string]string{"kind": kind})
			uds := string(ud)
			out.UserData = &uds
		}
		return out
	}
	msgs := []generated.Message{
		mkMsg(db.MessageTypeUser, llm.MessageRoleUser, "the question", ""),
		mkMsg(db.MessageTypeAgent, llm.MessageRoleAssistant, "the answer", ""),
		mkMsg(db.MessageTypeAgent, llm.MessageRoleAssistant, "a prior critique", "critique"),
	}
	target, userCtx, err := findCritiqueTarget(msgs)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if target != "the answer" {
		t.Errorf("target = %q, want %q", target, "the answer")
	}
	if userCtx != "the question" {
		t.Errorf("userCtx = %q, want %q", userCtx, "the question")
	}
}

func TestFindCritiqueTarget_NoAssistant(t *testing.T) {
	if _, _, err := findCritiqueTarget(nil); err == nil {
		t.Error("expected error when there is no assistant message")
	}
}

// TestCritiqueEndpoint_HappyPath runs the critique endpoint end-to-end against
// the predictable LLM after a real exchange.
func TestCritiqueEndpoint_HappyPath(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Close()

	h.NewConversation("hello", "/tmp")
	if got := h.WaitResponse(); !strings.Contains(got, "hi there") {
		t.Fatalf("unexpected agent reply: %q", got)
	}

	req := httptest.NewRequest("POST", "/api/conversation/"+h.ConversationID()+"/critique", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.server.handleCritiqueConversation(w, req, h.ConversationID())
	if w.Code != http.StatusOK {
		t.Fatalf("critique endpoint: status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Status    string `json:"status"`
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if resp.Status != "ok" || resp.MessageID == "" {
		t.Fatalf("bad resp: %+v", resp)
	}

	// Verify the critique message is in the DB and marked as kind=critique.
	var messages []generated.Message
	if err := h.db.Queries(context.Background(), func(q *generated.Queries) error {
		var qerr error
		messages, qerr = q.ListMessages(context.Background(), h.ConversationID())
		return qerr
	}); err != nil {
		t.Fatalf("list messages: %v", err)
	}
	found := false
	for _, m := range messages {
		if m.MessageID != resp.MessageID {
			continue
		}
		found = true
		if m.UserData == nil {
			t.Fatal("critique message has no user_data")
		}
		var ud map[string]string
		if err := json.Unmarshal([]byte(*m.UserData), &ud); err != nil {
			t.Fatalf("user_data: %v", err)
		}
		if ud["kind"] != "critique" {
			t.Errorf("kind = %q, want critique", ud["kind"])
		}
		if m.LlmData == nil {
			t.Fatal("critique missing LLM data")
		}
		var lm llm.Message
		if err := json.Unmarshal([]byte(*m.LlmData), &lm); err != nil {
			t.Fatalf("llm_data: %v", err)
		}
		if len(lm.Content) == 0 || !strings.Contains(lm.Content[0].Text, "Critique") {
			t.Errorf("critique body missing header: %+v", lm.Content)
		}
	}
	if !found {
		t.Fatal("created critique message not found in conversation")
	}
}

func TestCritiqueEndpoint_NoAssistant(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Close()

	// Create an empty conversation with no messages.
	conv, err := h.db.CreateConversation(context.Background(), nil, true, nil, strPtr("predictable"))
	if err != nil {
		t.Fatalf("create conv: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/conversation/"+conv.ConversationID+"/critique", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.server.handleCritiqueConversation(w, req, conv.ConversationID)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func strPtr(s string) *string { return &s }
