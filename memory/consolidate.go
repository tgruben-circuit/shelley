package memory

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tgruben-circuit/percy/llm"
)

const consolidationThreshold = 5 // Minimum unsummarized cells before consolidating

// ConsolidationResult is the structured output from LLM consolidation.
type ConsolidationResult struct {
	Summary           string   `json:"summary"`
	SupersededCellIDs []string `json:"superseded_cell_ids"`
}

// NeedsConsolidation returns true if the topic has enough unsummarized cells
// to warrant consolidation.
func NeedsConsolidation(db *DB, topicID string) (bool, error) {
	count, err := db.UnsummarizedCellCount(topicID)
	if err != nil {
		return false, err
	}
	return count >= consolidationThreshold, nil
}

const consolidationPrompt = `You are a memory consolidation engine for Percy, an AI coding assistant. Given a topic and its memory cells, produce an updated summary.

Rules:
- If a new cell contradicts an older one, the newer cell wins. List the cell_ids of superseded cells.
- Keep the summary under 150 words.
- Focus on current state, not history. Write "Auth uses JWT" not "We considered OAuth then switched to JWT".
- Preserve specific values: file paths, config keys, API shapes, port numbers.
- If there is an existing summary, update it with new information. Don't discard existing facts unless contradicted.

Topic: %s
Existing summary: %s
Cells (newest first):
%s

Return ONLY valid JSON with no code fences:
{"summary": "...", "superseded_cell_ids": ["cell_id_1", ...]}`

// ConsolidateTopic uses an LLM to consolidate a topic's cells into an updated summary.
func ConsolidateTopic(ctx context.Context, db *DB, svc llm.Service, embedder Embedder, topicID string) error {
	topic, err := db.GetTopic(topicID)
	if err != nil {
		return fmt.Errorf("memory: consolidate: %w", err)
	}
	if topic == nil {
		return fmt.Errorf("memory: consolidate: topic %q not found", topicID)
	}

	cells, err := db.GetCellsByTopic(topicID, false)
	if err != nil {
		return fmt.Errorf("memory: consolidate: %w", err)
	}
	if len(cells) == 0 {
		return nil
	}

	prompt := fmt.Sprintf(consolidationPrompt, topic.Name, topic.Summary, formatCellsForPrompt(cells))

	req := &llm.Request{
		Messages: []llm.Message{
			{
				Role:    llm.MessageRoleUser,
				Content: []llm.Content{{Type: llm.ContentTypeText, Text: prompt}},
			},
		},
	}

	resp, err := svc.Do(ctx, req)
	if err != nil {
		return fmt.Errorf("memory: consolidate llm: %w", err)
	}

	var text string
	for _, c := range resp.Content {
		if c.Type == llm.ContentTypeText {
			text = c.Text
			break
		}
	}

	result := parseConsolidationResult(text)
	if result.Summary == "" {
		return fmt.Errorf("memory: consolidate: empty summary from LLM")
	}

	// Update topic summary.
	topic.Summary = result.Summary

	// Re-embed the summary if an embedder is provided.
	if embedder != nil {
		vecs, err := embedder.Embed(ctx, []string{result.Summary})
		if err != nil {
			return fmt.Errorf("memory: consolidate embed: %w", err)
		}
		if len(vecs) > 0 && vecs[0] != nil {
			topic.Embedding = SerializeEmbedding(vecs[0])
		}
	}

	// Update cell_count to reflect non-superseded cells.
	topic.CellCount = len(cells) - len(result.SupersededCellIDs)

	if err := db.UpsertTopic(*topic); err != nil {
		return fmt.Errorf("memory: consolidate upsert: %w", err)
	}

	if err := db.SupersedeCells(result.SupersededCellIDs); err != nil {
		return fmt.Errorf("memory: consolidate supersede: %w", err)
	}

	return nil
}

// formatCellsForPrompt formats cells as JSON lines for the consolidation prompt.
func formatCellsForPrompt(cells []Cell) string {
	var buf []byte
	for _, c := range cells {
		line, _ := json.Marshal(struct {
			CellID   string  `json:"cell_id"`
			Type     string  `json:"type"`
			Salience float64 `json:"salience"`
			Content  string  `json:"content"`
		}{
			CellID:   c.CellID,
			Type:     c.CellType,
			Salience: c.Salience,
			Content:  c.Content,
		})
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	return string(buf)
}

// parseConsolidationResult parses the LLM response into a ConsolidationResult.
// Strips code fences and returns zero-value on parse failure.
func parseConsolidationResult(text string) ConsolidationResult {
	text = stripCodeFences(text)
	var result ConsolidationResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return ConsolidationResult{}
	}
	return result
}
