package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tgruben-circuit/percy/llm"
)

// ExtractedCell is the structured output from LLM extraction.
type ExtractedCell struct {
	CellType  string  `json:"cell_type"`
	Salience  float64 `json:"salience"`
	Content   string  `json:"content"`
	TopicHint string  `json:"topic_hint"`
}

var validCellTypes = map[string]bool{
	"fact":     true,
	"decision": true,
	"preference": true,
	"task":     true,
	"risk":     true,
	"code_ref": true,
}

const extractionPrompt = `You are a memory extraction engine for a coding assistant called Percy. Convert the conversation below into structured memory cells — atomic units of knowledge useful for FUTURE conversations.

Return a JSON array. Each object must have:
- cell_type: one of (fact, decision, preference, task, risk, code_ref)
- salience: 0.0-1.0 (how important for future work?)
- content: compressed, factual statement (1-2 sentences max)
- topic_hint: short topic label (e.g. "authentication", "database schema", "deployment")

Cell types:
- fact: concrete info ("Percy uses SQLite with sqlc codegen")
- decision: choice + rationale ("Switched to argon2id because bcrypt was too slow")
- preference: user style/tooling ("User prefers tabs over spaces")
- task: work item status ("Auth middleware blocked on JWT library choice")
- risk: gotcha/constraint ("pkill -f percy kills the parent process")
- code_ref: file path + role ("server/auth.go handles JWT validation middleware")

Focus on information useful in FUTURE conversations. Discard: greetings, debugging dead-ends (keep only final fix), intermediate states that were overwritten, routine acknowledgments, verbose tool output (keep only findings).

If the conversation is trivial (greetings, simple Q&A with no lasting value), return an empty array: []

Conversation:
%s`

// ExtractCells calls the LLM to extract structured cells from a conversation.
// Returns nil, nil on unparseable output (graceful degradation).
func ExtractCells(ctx context.Context, svc llm.Service, messages []MessageText) ([]ExtractedCell, error) {
	transcript := formatTranscript(messages)
	prompt := fmt.Sprintf(extractionPrompt, transcript)

	req := &llm.Request{
		System: []llm.SystemContent{{Text: "You extract structured memory cells from conversations. Respond only with a JSON array."}},
		Messages: []llm.Message{
			{
				Role:    llm.MessageRoleUser,
				Content: []llm.Content{{Type: llm.ContentTypeText, Text: prompt}},
			},
		},
	}

	resp, err := svc.Do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("memory: extract cells: %w", err)
	}

	// Find the text content in the response.
	var text string
	for _, c := range resp.Content {
		if c.Type == llm.ContentTypeText {
			text = c.Text
			break
		}
	}

	if text == "" {
		return nil, nil
	}

	// Strip code fences if present.
	text = stripCodeFences(text)

	var raw []ExtractedCell
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		// Graceful degradation: return nil, nil on bad JSON.
		return nil, nil
	}

	var cells []ExtractedCell
	for _, c := range raw {
		// Skip cells with empty content.
		if c.Content == "" {
			continue
		}
		// Validate cell type.
		if !validCellTypes[c.CellType] {
			c.CellType = "fact"
		}
		// Clamp salience to [0, 1].
		if c.Salience < 0 {
			c.Salience = 0
		}
		if c.Salience > 1 {
			c.Salience = 1
		}
		cells = append(cells, c)
	}

	if cells == nil {
		return []ExtractedCell{}, nil
	}
	return cells, nil
}

// FallbackChunkToCells converts messages to cells without an LLM,
// using ChunkMessages for splitting.
func FallbackChunkToCells(sourceID, sourceName string, messages []MessageText) []ExtractedCell {
	chunks := ChunkMessages(messages, 1024)
	cells := make([]ExtractedCell, 0, len(chunks))
	for _, ch := range chunks {
		cells = append(cells, ExtractedCell{
			CellType:  "fact",
			Salience:  0.5,
			Content:   ch.Text,
			TopicHint: sourceName,
		})
	}
	return cells
}

// formatTranscript renders messages as "Role: text\n" lines.
func formatTranscript(messages []MessageText) string {
	var buf strings.Builder
	for _, m := range messages {
		buf.WriteString(roleLabel(m.Role))
		buf.WriteString(": ")
		buf.WriteString(m.Text)
		buf.WriteByte('\n')
	}
	return buf.String()
}

// stripCodeFences removes ```json ... ``` wrapping from LLM output.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Remove opening fence line.
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		// Remove closing fence.
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}
	return s
}
