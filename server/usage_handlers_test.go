package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tgruben-circuit/percy/db"
	"github.com/tgruben-circuit/percy/llm"
)

func TestHandleUsage(t *testing.T) {
	h := NewTestHarness(t)
	defer h.cleanup()

	ctx := context.Background()
	model := "test-model"
	conv, err := h.db.CreateConversation(ctx, nil, true, nil, &model)
	if err != nil {
		t.Fatal(err)
	}

	// Add an agent message with usage data
	agentMsg := llm.Message{
		Role:    llm.MessageRoleAssistant,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "response"}},
	}
	usageData := map[string]interface{}{
		"input_tokens":  1000,
		"output_tokens": 200,
		"cost_usd":      0.05,
		"model":         "test-model",
	}
	_, err = h.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        agentMsg,
		UsageData:      usageData,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/usage?since=2020-01-01", nil)
	w := httptest.NewRecorder()
	h.server.handleUsage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp usageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if len(resp.ByDate) != 1 {
		t.Fatalf("expected 1 date row, got %d", len(resp.ByDate))
	}
	if resp.ByDate[0].TotalCostUSD != 0.05 {
		t.Fatalf("expected cost 0.05, got %f", resp.ByDate[0].TotalCostUSD)
	}
	if len(resp.ByConversation) != 1 {
		t.Fatalf("expected 1 conversation row, got %d", len(resp.ByConversation))
	}
	if resp.TotalCostUSD != 0.05 {
		t.Fatalf("expected total cost 0.05, got %f", resp.TotalCostUSD)
	}
}

func TestHandleUsageCostEstimation(t *testing.T) {
	h := NewTestHarness(t)
	defer h.cleanup()

	ctx := context.Background()
	model := "claude-opus-4-6"
	conv, err := h.db.CreateConversation(ctx, nil, true, nil, &model)
	if err != nil {
		t.Fatal(err)
	}

	// Agent message with zero cost_usd but known model — should be estimated
	usageData := map[string]interface{}{
		"input_tokens":                  1000,
		"output_tokens":                 500,
		"cache_read_input_tokens":       0,
		"cache_creation_input_tokens":   0,
		"cost_usd":                      0,
		"model":                         "claude-opus-4-6",
	}
	_, err = h.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeAgent,
		LLMData: llm.Message{
			Role:    llm.MessageRoleAssistant,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}},
		},
		UsageData: usageData,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/usage?since=2020-01-01", nil)
	w := httptest.NewRecorder()
	h.server.handleUsage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp usageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Cost should be estimated: 1000*5/1M + 500*25/1M = 0.005 + 0.0125 = 0.0175
	if resp.TotalCostUSD < 0.015 || resp.TotalCostUSD > 0.02 {
		t.Fatalf("expected estimated cost ~0.0175, got %f", resp.TotalCostUSD)
	}
}

func TestHandleUsageEmpty(t *testing.T) {
	h := NewTestHarness(t)
	defer h.cleanup()

	req := httptest.NewRequest("GET", "/api/usage", nil)
	w := httptest.NewRecorder()
	h.server.handleUsage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp usageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(resp.ByDate) != 0 {
		t.Fatalf("expected 0 date rows, got %d", len(resp.ByDate))
	}
	if resp.TotalCostUSD != 0 {
		t.Fatalf("expected total cost 0, got %f", resp.TotalCostUSD)
	}
}
