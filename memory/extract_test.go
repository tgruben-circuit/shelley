package memory_test

import (
	"context"
	"testing"

	"github.com/tgruben-circuit/percy/llm"
	"github.com/tgruben-circuit/percy/memory"
)

type mockLLMService struct {
	response string
}

func (m *mockLLMService) Do(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return &llm.Response{
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: m.response}},
	}, nil
}
func (m *mockLLMService) TokenContextWindow() int { return 128000 }
func (m *mockLLMService) MaxImageDimension() int  { return 0 }

func TestExtractCells(t *testing.T) {
	mock := &mockLLMService{
		response: `[
			{"cell_type": "fact", "salience": 0.9, "content": "Percy uses SQLite with sqlc codegen", "topic_hint": "database"},
			{"cell_type": "decision", "salience": 0.7, "content": "Switched to argon2id because bcrypt was too slow", "topic_hint": "authentication"},
			{"cell_type": "preference", "salience": 0.5, "content": "User prefers tabs over spaces", "topic_hint": "code style"}
		]`,
	}

	msgs := []memory.MessageText{
		{Role: "user", Text: "We use SQLite with sqlc for code generation."},
		{Role: "agent", Text: "Got it. I see the database layer uses sqlc."},
	}

	cells, err := memory.ExtractCells(context.Background(), mock, msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cells) != 3 {
		t.Fatalf("expected 3 cells, got %d", len(cells))
	}

	if cells[0].CellType != "fact" {
		t.Errorf("cell 0: expected type 'fact', got %q", cells[0].CellType)
	}
	if cells[0].Salience != 0.9 {
		t.Errorf("cell 0: expected salience 0.9, got %f", cells[0].Salience)
	}
	if cells[0].Content != "Percy uses SQLite with sqlc codegen" {
		t.Errorf("cell 0: unexpected content %q", cells[0].Content)
	}
	if cells[0].TopicHint != "database" {
		t.Errorf("cell 0: expected topic_hint 'database', got %q", cells[0].TopicHint)
	}

	if cells[1].CellType != "decision" {
		t.Errorf("cell 1: expected type 'decision', got %q", cells[1].CellType)
	}
	if cells[2].CellType != "preference" {
		t.Errorf("cell 2: expected type 'preference', got %q", cells[2].CellType)
	}
}

func TestExtractCellsFallbackOnBadJSON(t *testing.T) {
	mock := &mockLLMService{response: "not valid json"}

	msgs := []memory.MessageText{
		{Role: "user", Text: "Hello"},
		{Role: "agent", Text: "Hi there"},
	}

	cells, err := memory.ExtractCells(context.Background(), mock, msgs)
	if err != nil {
		t.Fatalf("expected no error on bad JSON, got: %v", err)
	}
	if len(cells) != 0 {
		t.Fatalf("expected empty slice on bad JSON, got %d cells", len(cells))
	}
}

func TestExtractCellsFallbackChunking(t *testing.T) {
	msgs := []memory.MessageText{
		{Role: "user", Text: "Tell me about the auth system."},
		{Role: "agent", Text: "The auth system uses JWT tokens."},
	}

	cells := memory.FallbackChunkToCells("src-123", "auth-conv", msgs)
	if len(cells) == 0 {
		t.Fatal("expected at least one cell from fallback chunking")
	}
	for i, c := range cells {
		if c.CellType != "fact" {
			t.Errorf("cell %d: expected type 'fact', got %q", i, c.CellType)
		}
		if c.Salience != 0.5 {
			t.Errorf("cell %d: expected salience 0.5, got %f", i, c.Salience)
		}
		if c.TopicHint != "auth-conv" {
			t.Errorf("cell %d: expected topic_hint 'auth-conv', got %q", i, c.TopicHint)
		}
		if c.Content == "" {
			t.Errorf("cell %d: expected non-empty content", i)
		}
	}
}

func TestExtractCellsStripsCodeFences(t *testing.T) {
	mock := &mockLLMService{
		response: "```json\n" + `[{"cell_type": "fact", "salience": 0.8, "content": "Uses esbuild for bundling", "topic_hint": "build"}]` + "\n```",
	}

	msgs := []memory.MessageText{
		{Role: "user", Text: "We use esbuild."},
	}

	cells, err := memory.ExtractCells(context.Background(), mock, msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cells) != 1 {
		t.Fatalf("expected 1 cell, got %d", len(cells))
	}
	if cells[0].Content != "Uses esbuild for bundling" {
		t.Errorf("unexpected content: %q", cells[0].Content)
	}
}

func TestExtractCellsValidation(t *testing.T) {
	mock := &mockLLMService{
		response: `[
			{"cell_type": "invalid_type", "salience": 1.5, "content": "some content", "topic_hint": "test"},
			{"cell_type": "fact", "salience": -0.3, "content": "negative salience", "topic_hint": "test"},
			{"cell_type": "", "salience": 0.5, "content": "empty type", "topic_hint": "test"},
			{"cell_type": "fact", "salience": 0.5, "content": "", "topic_hint": "test"}
		]`,
	}

	msgs := []memory.MessageText{
		{Role: "user", Text: "Test message"},
	}

	cells, err := memory.ExtractCells(context.Background(), mock, msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The cell with empty content should be skipped, leaving 3 cells.
	if len(cells) != 3 {
		t.Fatalf("expected 3 cells (empty content skipped), got %d", len(cells))
	}

	// First cell: invalid type should default to "fact", salience clamped to 1.0.
	if cells[0].CellType != "fact" {
		t.Errorf("cell 0: expected type 'fact' (defaulted), got %q", cells[0].CellType)
	}
	if cells[0].Salience != 1.0 {
		t.Errorf("cell 0: expected salience clamped to 1.0, got %f", cells[0].Salience)
	}

	// Second cell: negative salience should be clamped to 0.0.
	if cells[1].Salience != 0.0 {
		t.Errorf("cell 1: expected salience clamped to 0.0, got %f", cells[1].Salience)
	}

	// Third cell: empty type should default to "fact".
	if cells[2].CellType != "fact" {
		t.Errorf("cell 2: expected type 'fact' (defaulted), got %q", cells[2].CellType)
	}
}
