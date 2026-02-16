package memory

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/tgruben-circuit/percy/llm"
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
	if err := mdb.IndexConversation(ctx, "conv-auth", "Auth Discussion", authMessages, embedder, nil); err != nil {
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
	results, err := mdb.TwoTierSearch("authentication JWT", nil, "", 10)
	if err != nil {
		t.Fatalf("TwoTierSearch (authentication JWT): %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'authentication JWT', got 0")
	}
	t.Logf("search 'authentication JWT' returned %d results", len(results))
	for _, r := range results {
		t.Logf("  type=%s source_type=%s source_name=%s score=%.4f", r.ResultType, r.SourceType, r.SourceName, r.Score)
	}

	// Step 4: Search "JWT" with sourceType "file" — verify results found and all cells have SourceType "file".
	fileResults, err := mdb.TwoTierSearch("JWT", nil, "file", 10)
	if err != nil {
		t.Fatalf("TwoTierSearch (JWT, file): %v", err)
	}
	if len(fileResults) == 0 {
		t.Fatal("expected file results for 'JWT', got 0")
	}
	for _, r := range fileResults {
		if r.ResultType == "cell" && r.SourceType != "file" {
			t.Errorf("expected source_type='file', got %q", r.SourceType)
		}
	}
	t.Logf("search 'JWT' (file only) returned %d results", len(fileResults))

	// Step 5: Re-index same conversation — verify no error (hash skip).
	if err := mdb.IndexConversation(ctx, "conv-auth", "Auth Discussion", authMessages, embedder, nil); err != nil {
		t.Fatalf("re-index same conversation: %v", err)
	}

	// Verify content is still searchable after the skip.
	afterSkip, err := mdb.TwoTierSearch("authentication", nil, "", 10)
	if err != nil {
		t.Fatalf("TwoTierSearch after re-index: %v", err)
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
	if err := mdb.IndexConversation(ctx, "conv-db", "Database Design", dbMessages, embedder, nil); err != nil {
		t.Fatalf("IndexConversation (db): %v", err)
	}

	// Step 7: Search "database schema" — verify results found.
	dbResults, err := mdb.TwoTierSearch("database schema", nil, "", 10)
	if err != nil {
		t.Fatalf("TwoTierSearch (database schema): %v", err)
	}
	if len(dbResults) == 0 {
		t.Fatal("expected results for 'database schema', got 0")
	}
	t.Logf("search 'database schema' returned %d results", len(dbResults))
	for _, r := range dbResults {
		t.Logf("  type=%s source_type=%s source_name=%s score=%.4f", r.ResultType, r.SourceType, r.SourceName, r.Score)
	}
}

// mockLLMForIntegration is a mock LLM service that returns pre-configured responses.
type mockLLMForIntegration struct {
	responses []string
	callCount int
}

func (m *mockLLMForIntegration) Do(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	resp := m.responses[m.callCount%len(m.responses)]
	m.callCount++
	return &llm.Response{
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: resp}},
	}, nil
}
func (m *mockLLMForIntegration) TokenContextWindow() int { return 128000 }
func (m *mockLLMForIntegration) MaxImageDimension() int  { return 0 }

func TestFullMemoryPipeline(t *testing.T) {
	dir := t.TempDir()
	mdb, err := Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()
	ctx := context.Background()

	// Mock LLM returns extraction results.
	extractResp, _ := json.Marshal([]ExtractedCell{
		{CellType: "decision", Salience: 0.9, Content: "Auth uses JWT with RS256", TopicHint: "authentication"},
		{CellType: "code_ref", Salience: 0.7, Content: "server/auth.go handles JWT middleware", TopicHint: "authentication"},
		{CellType: "fact", Salience: 0.6, Content: "UI built with React and TypeScript", TopicHint: "frontend"},
	})
	svc := &mockLLMForIntegration{responses: []string{string(extractResp)}}

	// Step 1: Index a conversation with LLM extraction.
	messages := []MessageText{
		{Role: "user", Text: "Implement JWT auth"},
		{Role: "assistant", Text: "Done — JWT with RS256 in server/auth.go. UI updated with React."},
	}
	err = mdb.IndexConversation(ctx, "conv_1", "auth-impl", messages, nil, svc)
	if err != nil {
		t.Fatal(err)
	}

	// Step 2: Verify cells are searchable.
	results, err := mdb.TwoTierSearch("JWT", nil, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results after indexing")
	}
	t.Logf("search returned %d results", len(results))
	for _, r := range results {
		t.Logf("  type=%s topic=%s cell_type=%s content=%s", r.ResultType, r.TopicName, r.CellType, r.Content)
	}

	// Step 3: Verify topics were created.
	topics, err := mdb.AllTopics()
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) < 2 {
		t.Errorf("expected at least 2 topics (auth + frontend), got %d", len(topics))
	}
	t.Logf("topics created: %d", len(topics))
	for _, topic := range topics {
		t.Logf("  topic=%s name=%s cells=%d", topic.TopicID, topic.Name, topic.CellCount)
	}

	// Step 4: Re-indexing with same content should be a no-op.
	callsBefore := svc.callCount
	err = mdb.IndexConversation(ctx, "conv_1", "auth-impl", messages, nil, svc)
	if err != nil {
		t.Fatal(err)
	}
	if svc.callCount != callsBefore {
		t.Error("re-indexing unchanged conversation should not make LLM calls")
	}

	// Step 5: Index a file and verify it's searchable.
	err = mdb.IndexFile(ctx, "/docs/README.md", "README.md", "# Auth\n\nJWT tokens for API access.\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	fileResults, err := mdb.TwoTierSearch("JWT tokens", nil, "file", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fileResults) == 0 {
		t.Fatal("expected file results")
	}

	// Step 6: Verify cross-type search returns both sources.
	allResults, err := mdb.TwoTierSearch("JWT", nil, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	hasConv, hasFile := false, false
	for _, r := range allResults {
		if r.SourceType == "conversation" {
			hasConv = true
		}
		if r.SourceType == "file" {
			hasFile = true
		}
	}
	if !hasConv {
		t.Error("expected conversation results in cross-type search")
	}
	if !hasFile {
		t.Error("expected file results in cross-type search")
	}
}
