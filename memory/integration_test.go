package memory

import (
	"context"
	"path/filepath"
	"testing"
)

func TestIntegrationFullFlow(t *testing.T) {
	dir := t.TempDir()
	mdb, err := Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	ctx := context.Background()
	embedder := &NoneEmbedder{}

	// Step 1: Index a conversation about authentication/JWT.
	authMessages := []MessageText{
		{Role: "user", Text: "How do I implement JWT authentication in Go?"},
		{Role: "assistant", Text: "You can use the golang-jwt library to create and verify JWT tokens."},
		{Role: "user", Text: "Can you show me middleware for HTTP handlers?"},
		{Role: "assistant", Text: "Here is an authentication middleware that extracts and validates the bearer token."},
	}
	if err := mdb.IndexConversation(ctx, "conv-auth", "Auth Discussion", authMessages, embedder); err != nil {
		t.Fatalf("IndexConversation (auth): %v", err)
	}

	// Step 2: Index a workspace file with project info and auth section.
	fileContent := `# Project Overview

This is the main project documentation for our web application.

## Architecture

The application uses a microservices architecture with a Go backend and React frontend.

## Authentication

The authentication system uses JWT tokens signed with HMAC-SHA256.
Users authenticate via the /login endpoint and receive a bearer token.
The middleware validates tokens on every protected API route.

## Database

We use PostgreSQL for persistent storage with connection pooling.
`
	if err := mdb.IndexFile(ctx, "/docs/README.md", "README.md", fileContent, embedder); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}

	// Step 3: Search "authentication JWT" — verify results found.
	results, err := mdb.HybridSearch("authentication JWT", nil, "", 10)
	if err != nil {
		t.Fatalf("HybridSearch (authentication JWT): %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'authentication JWT', got 0")
	}
	t.Logf("search 'authentication JWT' returned %d results", len(results))
	for _, r := range results {
		t.Logf("  chunk=%s source_type=%s source_name=%s score=%.4f", r.ChunkID, r.SourceType, r.SourceName, r.Score)
	}

	// Step 4: Search "JWT" with sourceType "file" — verify results found and all have SourceType "file".
	fileResults, err := mdb.HybridSearch("JWT", nil, "file", 10)
	if err != nil {
		t.Fatalf("HybridSearch (JWT, file): %v", err)
	}
	if len(fileResults) == 0 {
		t.Fatal("expected file results for 'JWT', got 0")
	}
	for _, r := range fileResults {
		if r.SourceType != "file" {
			t.Errorf("expected source_type='file', got %q", r.SourceType)
		}
	}
	t.Logf("search 'JWT' (file only) returned %d results", len(fileResults))

	// Step 5: Re-index same conversation — verify no error (hash skip).
	if err := mdb.IndexConversation(ctx, "conv-auth", "Auth Discussion", authMessages, embedder); err != nil {
		t.Fatalf("re-index same conversation: %v", err)
	}

	// Verify content is still searchable after the skip.
	afterSkip, err := mdb.HybridSearch("authentication", nil, "", 10)
	if err != nil {
		t.Fatalf("HybridSearch after re-index: %v", err)
	}
	if len(afterSkip) == 0 {
		t.Fatal("expected results after re-index skip, got 0")
	}

	// Step 6: Index a second conversation about database schema.
	dbMessages := []MessageText{
		{Role: "user", Text: "How should I design the database schema for user management?"},
		{Role: "assistant", Text: "You should create separate tables for users, roles, and permissions with proper foreign keys."},
		{Role: "user", Text: "What about schema migrations?"},
		{Role: "assistant", Text: "Use a migration tool like golang-migrate to version your database schema changes."},
	}
	if err := mdb.IndexConversation(ctx, "conv-db", "Database Design", dbMessages, embedder); err != nil {
		t.Fatalf("IndexConversation (db): %v", err)
	}

	// Step 7: Search "database schema" — verify results found.
	dbResults, err := mdb.HybridSearch("database schema", nil, "", 10)
	if err != nil {
		t.Fatalf("HybridSearch (database schema): %v", err)
	}
	if len(dbResults) == 0 {
		t.Fatal("expected results for 'database schema', got 0")
	}
	t.Logf("search 'database schema' returned %d results", len(dbResults))
	for _, r := range dbResults {
		t.Logf("  chunk=%s source_type=%s source_name=%s score=%.4f", r.ChunkID, r.SourceType, r.SourceName, r.Score)
	}
}
