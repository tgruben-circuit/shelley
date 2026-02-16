package memory

import (
	"path/filepath"
	"testing"
)

func TestVectorSearch(t *testing.T) {
	dir := t.TempDir()
	mdb, err := Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Insert 3 chunks with known embeddings.
	chunks := []struct {
		id    string
		text  string
		embed []float32
	}{
		{"c1", "closest to query", []float32{0.9, 0.1, 0}},
		{"c2", "orthogonal to query", []float32{0, 0, 1}},
		{"c3", "somewhat similar", []float32{0.7, 0.3, 0.1}},
	}

	for _, c := range chunks {
		emb := SerializeEmbedding(c.embed)
		if err := mdb.InsertChunk(c.id, "conversation", "src-1", "test", 0, c.text, emb); err != nil {
			t.Fatalf("InsertChunk(%s): %v", c.id, err)
		}
	}

	queryVec := []float32{1, 0, 0}
	results, err := mdb.SearchVector(queryVec, "", 10)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// c1 should be first (most similar to [1,0,0]).
	if results[0].ChunkID != "c1" {
		t.Errorf("expected first result c1, got %s", results[0].ChunkID)
	}
	// c2 should be last (orthogonal).
	if results[2].ChunkID != "c2" {
		t.Errorf("expected last result c2, got %s", results[2].ChunkID)
	}

	for _, r := range results {
		t.Logf("  chunk=%s score=%.4f", r.ChunkID, r.Score)
	}
}

func TestVectorSearchNoEmbeddings(t *testing.T) {
	dir := t.TempDir()
	mdb, err := Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Insert chunk without embedding.
	if err := mdb.InsertChunk("c1", "conversation", "src-1", "test", 0, "no embedding here", nil); err != nil {
		t.Fatal(err)
	}

	results, err := mdb.SearchVector([]float32{1, 0, 0}, "", 10)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestHybridSearch(t *testing.T) {
	dir := t.TempDir()
	mdb, err := Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// c1: FTS match + medium vector similarity.
	emb1 := SerializeEmbedding([]float32{0.5, 0.5, 0})
	if err := mdb.InsertChunk("c1", "conversation", "src-1", "test", 0,
		"authentication with JWT tokens", emb1); err != nil {
		t.Fatal(err)
	}

	// c2: no FTS match + strong vector similarity.
	emb2 := SerializeEmbedding([]float32{0.95, 0.05, 0})
	if err := mdb.InsertChunk("c2", "conversation", "src-1", "test", 1,
		"unrelated content about databases", emb2); err != nil {
		t.Fatal(err)
	}

	// c3: FTS match + medium vector similarity.
	emb3 := SerializeEmbedding([]float32{0.4, 0.6, 0})
	if err := mdb.InsertChunk("c3", "conversation", "src-1", "test", 2,
		"authentication middleware layer", emb3); err != nil {
		t.Fatal(err)
	}

	queryVec := []float32{1, 0, 0}
	results, err := mdb.HybridSearch("authentication", queryVec, "", 10)
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}

	// Both c1 and c2 should appear in results.
	found := make(map[string]bool)
	for _, r := range results {
		found[r.ChunkID] = true
		t.Logf("  chunk=%s score=%.4f", r.ChunkID, r.Score)
	}
	if !found["c1"] {
		t.Error("expected c1 in hybrid results")
	}
	if !found["c2"] {
		t.Error("expected c2 in hybrid results (strong vector similarity)")
	}
}

func TestHybridSearchFTSOnly(t *testing.T) {
	dir := t.TempDir()
	mdb, err := Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Insert chunk without embedding.
	if err := mdb.InsertChunk("c1", "conversation", "src-1", "test", 0,
		"authentication with JWT tokens for the API", nil); err != nil {
		t.Fatal(err)
	}

	// Search with nil queryVec — should fall back to FTS only.
	results, err := mdb.HybridSearch("authentication", nil, "", 10)
	if err != nil {
		t.Fatalf("HybridSearch FTS-only: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 FTS-only result, got 0")
	}
	if results[0].ChunkID != "c1" {
		t.Errorf("expected c1, got %s", results[0].ChunkID)
	}
	t.Logf("FTS-only results: %d", len(results))
}

func TestTwoTierSearch(t *testing.T) {
	dir := t.TempDir()
	mdb, err := Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Create topic with summary.
	mdb.UpsertTopic(Topic{
		TopicID: "topic_auth", Name: "authentication",
		Summary: "Authentication uses JWT with RS256 signing. Tokens in httpOnly cookies.",
	})

	// Create cells.
	mdb.InsertCell(Cell{
		CellID: "c1", TopicID: "topic_auth", SourceType: "conversation",
		SourceID: "conv1", CellType: "decision", Salience: 0.9,
		Content: "Switched from bcrypt to argon2id for password hashing",
	})
	mdb.InsertCell(Cell{
		CellID: "c2", TopicID: "topic_auth", SourceType: "conversation",
		SourceID: "conv1", CellType: "code_ref", Salience: 0.7,
		Content: "server/auth.go handles JWT authentication validation middleware",
	})

	results, err := mdb.TwoTierSearch("JWT authentication", nil, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}

	// Should have at least one topic_summary result.
	foundSummary := false
	foundCell := false
	for _, r := range results {
		if r.ResultType == "topic_summary" {
			foundSummary = true
		}
		if r.ResultType == "cell" {
			foundCell = true
		}
	}
	if !foundSummary {
		t.Error("expected at least one topic_summary result")
	}
	if !foundCell {
		t.Error("expected at least one cell result")
	}
}

func TestFTS(t *testing.T) {
	dir := t.TempDir()
	mdb, err := Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Insert 3 conversation chunks and 1 file chunk.
	chunks := []struct {
		id, srcType, srcID, srcName string
		index                       int
		text                        string
	}{
		{"conv-1-0", "conversation", "conv-1", "Auth Discussion", 0,
			"We need to implement authentication using JWT tokens for the API gateway"},
		{"conv-1-1", "conversation", "conv-1", "Auth Discussion", 1,
			"The authentication middleware should validate tokens and extract user claims"},
		{"conv-2-0", "conversation", "conv-2", "Database Design", 0,
			"The database schema uses PostgreSQL with separate tables for users and sessions"},
		{"file-1-0", "file", "file-1", "auth.go", 0,
			"Package auth provides authentication helpers including JWT token verification"},
	}

	for _, c := range chunks {
		if err := mdb.InsertChunk(c.id, c.srcType, c.srcID, c.srcName, c.index, c.text, nil); err != nil {
			t.Fatalf("InsertChunk(%s): %v", c.id, err)
		}
	}

	// 1. Search for "authentication" — should return results.
	results, err := mdb.SearchFTS("authentication", "", 10)
	if err != nil {
		t.Fatalf("SearchFTS(authentication): %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'authentication', got 0")
	}
	t.Logf("search 'authentication' returned %d results", len(results))
	for _, r := range results {
		t.Logf("  chunk=%s source_type=%s score=%.4f", r.ChunkID, r.SourceType, r.Score)
	}

	// 2. Search with source_type filter "file" — should return exactly 1 result.
	fileResults, err := mdb.SearchFTS("authentication", "file", 10)
	if err != nil {
		t.Fatalf("SearchFTS(authentication, file): %v", err)
	}
	if len(fileResults) != 1 {
		t.Fatalf("expected 1 file result, got %d", len(fileResults))
	}
	if fileResults[0].SourceType != "file" {
		t.Errorf("expected source_type=file, got %s", fileResults[0].SourceType)
	}

	// 3. Search for "kubernetes" — should return 0 results.
	noResults, err := mdb.SearchFTS("kubernetes", "", 10)
	if err != nil {
		t.Fatalf("SearchFTS(kubernetes): %v", err)
	}
	if len(noResults) != 0 {
		t.Fatalf("expected 0 results for 'kubernetes', got %d", len(noResults))
	}
}
