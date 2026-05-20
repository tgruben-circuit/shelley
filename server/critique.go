package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tgruben-circuit/percy/db"
	"github.com/tgruben-circuit/percy/db/generated"
	"github.com/tgruben-circuit/percy/llm"
)

// critiqueSystemPrompt asks the model to act as a staff engineer reviewer.
const critiqueSystemPrompt = `You are an experienced staff engineer doing a focused review of an AI coding assistant's previous response. Your job is to find real problems before the user does.

Look hard for:
- factual mistakes, hallucinated APIs, wrong function signatures
- hidden assumptions ("assumes file X exists", "assumes Linux", etc.)
- missing edge cases or error paths
- missing or insufficient tests
- claims of "done" that weren't actually verified
- security, concurrency, or correctness traps
- contradictions with the user's stated intent
- overconfidence on uncertain ground

Output rules:
- Markdown, no preamble, no "Here is..."
- A bulleted list of concrete issues, most-severe first.
- Each bullet: one line, name the specific file/symbol/claim, then say what's wrong.
- If after careful review you genuinely find nothing wrong, reply with a single line: "No issues found." — but only after actually checking.
- Do not rewrite the response. Do not propose a full fix. Flag, don't refactor.

End with exactly one <confidence level="..."> block summarizing your own confidence in the review.`

// critiqueRequest is the JSON body for POST /api/conversation/{id}/critique.
type critiqueRequest struct {
	Model string `json:"model,omitempty"`
}

// handleCritiqueConversation runs a self-critique pass against the latest
// assistant response in the conversation and inserts the critique as a new
// assistant message.
func (s *Server) handleCritiqueConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	var req critiqueRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
	}

	conv, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}

	modelID := req.Model
	if modelID == "" && conv.Model != nil {
		modelID = *conv.Model
	}
	if modelID == "" {
		modelID = s.defaultModel
	}
	modelID, err = s.resolveModelID(modelID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Unsupported model: %s", modelID), http.StatusBadRequest)
		return
	}

	messages, err := s.db.ListMessages(ctx, conversationID)
	if err != nil {
		s.logger.Error("critique: list messages", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	target, userCtx, err := findCritiqueTarget(messages)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	svc, err := s.llmManager.GetService(modelID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Unsupported model: %s", modelID), http.StatusBadRequest)
		return
	}

	llmCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	transcript := buildCritiqueInput(userCtx, target)
	resp, err := svc.Do(llmCtx, &llm.Request{
		System: []llm.SystemContent{{Text: critiqueSystemPrompt, Type: "text"}},
		Messages: []llm.Message{{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: transcript}},
		}},
	})
	if err != nil {
		s.logger.Error("critique: LLM call failed", "conversationID", conversationID, "error", err)
		http.Error(w, fmt.Sprintf("Critique failed: %v", err), http.StatusBadGateway)
		return
	}

	var critique string
	for _, c := range resp.Content {
		if c.Type == llm.ContentTypeText {
			critique += c.Text
		}
	}
	critique = strings.TrimSpace(critique)
	if critique == "" {
		http.Error(w, "Critique returned empty", http.StatusBadGateway)
		return
	}

	// Mark the critique message so the UI can style it distinctly.
	userData := map[string]string{"kind": "critique"}
	body := "## 🔍 Critique\n\n" + critique
	critiqueMessage := llm.Message{
		Role:    llm.MessageRoleAssistant,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: body}},
	}

	createdMsg, err := s.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        critiqueMessage,
		UserData:       userData,
		UsageData:      resp.Usage,
	})
	if err != nil {
		s.logger.Error("critique: persist message", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	go s.notifySubscribersNewMessage(context.WithoutCancel(ctx), conversationID, createdMsg)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{ //nolint:errchkjson // best-effort response
		"status":     "ok",
		"message_id": createdMsg.MessageID,
	})
}

// findCritiqueTarget walks backward through messages and returns the most
// recent assistant text response, plus the user message that prompted it (if any).
func findCritiqueTarget(messages []generated.Message) (target string, userCtx string, err error) {
	var assistantText, userText string
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Type != string(db.MessageTypeAgent) || m.LlmData == nil {
			continue
		}
		var lm llm.Message
		if jerr := json.Unmarshal([]byte(*m.LlmData), &lm); jerr != nil {
			continue
		}
		if lm.Role != llm.MessageRoleAssistant {
			continue
		}
		// Skip critique messages so re-running /critique critiques the original.
		if m.UserData != nil {
			var ud map[string]string
			if jerr := json.Unmarshal([]byte(*m.UserData), &ud); jerr == nil && ud["kind"] == "critique" {
				continue
			}
		}
		text := extractText(lm.Content)
		if text == "" {
			continue
		}
		assistantText = text
		// Walk back further for the most recent user message.
		for j := i - 1; j >= 0; j-- {
			um := messages[j]
			if um.Type != string(db.MessageTypeUser) || um.LlmData == nil {
				continue
			}
			var ulm llm.Message
			if jerr := json.Unmarshal([]byte(*um.LlmData), &ulm); jerr != nil {
				continue
			}
			if ulm.Role != llm.MessageRoleUser {
				continue
			}
			userText = extractText(ulm.Content)
			break
		}
		break
	}
	if assistantText == "" {
		return "", "", fmt.Errorf("no assistant message to critique")
	}
	return assistantText, userText, nil
}

func extractText(content []llm.Content) string {
	var b strings.Builder
	for _, c := range content {
		if c.Type == llm.ContentTypeText {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func buildCritiqueInput(userCtx, assistantResp string) string {
	var b strings.Builder
	if userCtx != "" {
		b.WriteString("# User's most recent message\n\n")
		b.WriteString(truncateUTF8(userCtx, 4000))
		b.WriteString("\n\n")
	}
	b.WriteString("# Assistant response to critique\n\n")
	b.WriteString(truncateUTF8(assistantResp, 12000))
	return b.String()
}
