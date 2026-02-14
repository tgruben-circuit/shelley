package memory

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	memdb "shelley.exe.dev/memory"
)

func TestMemorySearchTool(t *testing.T) {
	dir := t.TempDir()
	db, err := memdb.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Seed the DB with chunks.
	chunks := []struct {
		id, srcType, srcID, srcName string
		index                       int
		text                        string
	}{
		{"conv-1-0", "conversation", "conv-1", "Auth Discussion", 0,
			"We decided to use JWT authentication for the API gateway"},
		{"conv-1-1", "conversation", "conv-1", "Auth Discussion", 1,
			"The authentication middleware validates tokens and extracts user claims"},
		{"file-1-0", "file", "file-1", "auth.go", 0,
			"Package auth provides authentication helpers including token verification"},
	}
	for _, c := range chunks {
		if err := db.InsertChunk(c.id, c.srcType, c.srcID, c.srcName, c.index, c.text, nil); err != nil {
			t.Fatalf("InsertChunk(%s): %v", c.id, err)
		}
	}

	tool := NewMemorySearchTool(db, nil)
	input, err := json.Marshal(searchInput{Query: "authentication"})
	if err != nil {
		t.Fatalf("Failed to marshal input: %v", err)
	}
	result := tool.Run(context.Background(), input)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if len(result.LLMContent) == 0 {
		t.Fatal("expected non-empty LLMContent")
	}
	text := result.LLMContent[0].Text
	if text == "" {
		t.Fatal("expected non-empty text in LLMContent")
	}
	if !strings.Contains(text, "relevant memories") {
		t.Errorf("expected results header, got: %s", text)
	}
	t.Logf("result: %s", text)
}

func TestMemorySearchToolNoDatabase(t *testing.T) {
	tool := NewMemorySearchTool(nil, nil)
	input, err := json.Marshal(searchInput{Query: "anything"})
	if err != nil {
		t.Fatalf("Failed to marshal input: %v", err)
	}
	result := tool.Run(context.Background(), input)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if len(result.LLMContent) == 0 {
		t.Fatal("expected non-empty LLMContent")
	}
	text := result.LLMContent[0].Text
	if !strings.Contains(text, "No memory index found") {
		t.Errorf("expected helpful nil-db message, got: %s", text)
	}
}

func TestMemorySearchToolEmptyResults(t *testing.T) {
	dir := t.TempDir()
	db, err := memdb.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	tool := NewMemorySearchTool(db, nil)
	input, err := json.Marshal(searchInput{Query: "nonexistent"})
	if err != nil {
		t.Fatalf("Failed to marshal input: %v", err)
	}
	result := tool.Run(context.Background(), input)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if len(result.LLMContent) == 0 {
		t.Fatal("expected non-empty LLMContent")
	}
	text := result.LLMContent[0].Text
	if !strings.Contains(text, "No relevant memories found") {
		t.Errorf("expected no-results message, got: %s", text)
	}
}
