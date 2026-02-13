package memory

import (
	"context"
	"path/filepath"
	"testing"
)

func TestIndexConversation(t *testing.T) {
	dir := t.TempDir()
	mdb, err := Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	messages := []MessageText{
		{Role: "user", Text: "How do I implement JWT authentication in Go?"},
		{Role: "assistant", Text: "You can use the golang-jwt library to create and verify JWT tokens."},
		{Role: "user", Text: "Can you show me middleware for HTTP handlers?"},
		{Role: "assistant", Text: "Here is an authentication middleware that extracts and validates the bearer token."},
	}

	ctx := context.Background()
	err = mdb.IndexConversation(ctx, "conv-abc", "Auth Discussion", messages, nil)
	if err != nil {
		t.Fatalf("IndexConversation: %v", err)
	}

	// Verify SearchFTS finds the indexed content.
	results, err := mdb.SearchFTS("authentication", "conversation", 10)
	if err != nil {
		t.Fatalf("SearchFTS: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results for 'authentication', got 0")
	}

	// Verify source_name is the slug we passed.
	for _, r := range results {
		if r.SourceName != "Auth Discussion" {
			t.Errorf("expected source_name='Auth Discussion', got %q", r.SourceName)
		}
		if r.SourceID != "conv-abc" {
			t.Errorf("expected source_id='conv-abc', got %q", r.SourceID)
		}
	}
}

func TestIndexConversationSkipsIfUnchanged(t *testing.T) {
	dir := t.TempDir()
	mdb, err := Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	messages := []MessageText{
		{Role: "user", Text: "Tell me about Kubernetes deployments."},
		{Role: "assistant", Text: "Kubernetes deployments manage replica sets and pod rollouts."},
	}

	ctx := context.Background()

	// First index.
	if err := mdb.IndexConversation(ctx, "conv-1", "K8s Chat", messages, nil); err != nil {
		t.Fatalf("first IndexConversation: %v", err)
	}

	// Second index with same content should succeed (skip).
	if err := mdb.IndexConversation(ctx, "conv-1", "K8s Chat", messages, nil); err != nil {
		t.Fatalf("second IndexConversation: %v", err)
	}

	// Verify content is still searchable after the skip.
	results, err := mdb.SearchFTS("Kubernetes", "conversation", 10)
	if err != nil {
		t.Fatalf("SearchFTS: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results after skip, got 0")
	}
}

func TestIndexFile(t *testing.T) {
	dir := t.TempDir()
	mdb, err := Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	content := `# Authentication Module

This module provides JWT-based authentication for the API server.

## Token Generation

Tokens are generated using HMAC-SHA256 with a configurable secret key.

## Middleware

The HTTP middleware validates incoming bearer tokens and rejects expired ones.
`

	ctx := context.Background()
	err = mdb.IndexFile(ctx, "/src/auth.go", "auth.go", content, nil)
	if err != nil {
		t.Fatalf("IndexFile: %v", err)
	}

	// Verify SearchFTS finds the indexed file content.
	results, err := mdb.SearchFTS("authentication", "file", 10)
	if err != nil {
		t.Fatalf("SearchFTS: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results for 'authentication' in files, got 0")
	}

	for _, r := range results {
		if r.SourceType != "file" {
			t.Errorf("expected source_type='file', got %q", r.SourceType)
		}
		if r.SourceName != "auth.go" {
			t.Errorf("expected source_name='auth.go', got %q", r.SourceName)
		}
	}
}
