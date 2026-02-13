# Memory Search Tool Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a `memory_search` tool that lets the agent search past conversation transcripts and workspace files using hybrid FTS5 + vector search.

**Architecture:** Separate `memory.db` alongside `shelley.db` with FTS5 for keyword search and BLOB-stored embeddings for vector search (cosine similarity computed in Go). Three embedding backends: API provider reuse, Ollama, or none. Indexing triggered post-conversation.

**Tech Stack:** Go, `modernc.org/sqlite` (pure Go, no CGO), FTS5, pure-Go cosine similarity (no sqlite-vec — incompatible with modernc), OpenAI/Gemini/Ollama embedding APIs.

**Design doc:** `docs/plans/2026-02-13-memory-search-tool-design.md`

---

### Task 1: Memory database package — schema and migrations

**Files:**
- Create: `memory/db.go`
- Create: `memory/schema.sql`

**Step 1: Write the failing test**

Create `memory/db_test.go`:

```go
package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCreatesDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "memory.db")
	mdb, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Verify file exists
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database file not created: %v", err)
	}

	// Verify tables exist by inserting and querying
	_, err = mdb.db.Exec(`INSERT INTO chunks (chunk_id, source_type, source_id, source_name, chunk_index, text)
		VALUES ('test1', 'conversation', 'c1', 'test', 0, 'hello world')`)
	if err != nil {
		t.Fatalf("chunks table not created: %v", err)
	}

	_, err = mdb.db.Exec(`INSERT INTO index_state (source_type, source_id, indexed_at, hash)
		VALUES ('conversation', 'c1', datetime('now'), 'abc123')`)
	if err != nil {
		t.Fatalf("index_state table not created: %v", err)
	}
}

func TestOpenIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "memory.db")

	// Open twice — second open should not fail
	mdb1, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	mdb1.Close()

	mdb2, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	mdb2.Close()
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./memory/ -run TestOpen -v`
Expected: FAIL — package doesn't exist yet

**Step 3: Write minimal implementation**

Create `memory/schema.sql` (embedded):

```sql
CREATE TABLE IF NOT EXISTS chunks (
    chunk_id    TEXT PRIMARY KEY,
    source_type TEXT NOT NULL CHECK(source_type IN ('conversation', 'file')),
    source_id   TEXT NOT NULL,
    source_name TEXT,
    chunk_index INTEGER NOT NULL,
    text        TEXT NOT NULL,
    token_count INTEGER,
    embedding   BLOB,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_chunks_source ON chunks(source_type, source_id);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
    text,
    content='chunks',
    content_rowid='rowid'
);

-- Triggers to keep FTS in sync with chunks table
CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_fts(rowid, text) VALUES (new.rowid, new.text);
END;
CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES('delete', old.rowid, old.text);
END;
CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES('delete', old.rowid, old.text);
    INSERT INTO chunks_fts(rowid, text) VALUES (new.rowid, new.text);
END;

CREATE TABLE IF NOT EXISTS index_state (
    source_type TEXT NOT NULL,
    source_id   TEXT NOT NULL,
    indexed_at  DATETIME NOT NULL,
    hash        TEXT,
    PRIMARY KEY (source_type, source_id)
);
```

Create `memory/db.go`:

```go
package memory

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// DB wraps the memory database connection.
type DB struct {
	db   *sql.DB
	path string
}

// Open opens (or creates) the memory database at the given path and runs migrations.
func Open(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("memory: create dir: %w", err)
		}
	}

	dsn := path + "?_foreign_keys=on&_journal_mode=WAL"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("memory: open: %w", err)
	}
	sqldb.SetMaxOpenConns(1)

	if _, err := sqldb.Exec(schemaSQL); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("memory: migrate: %w", err)
	}

	return &DB{db: sqldb, path: path}, nil
}

// Close closes the database connection.
func (d *DB) Close() error {
	return d.db.Close()
}

// MemoryDBPath returns the memory.db path derived from the main shelley.db path.
func MemoryDBPath(shelleyDBPath string) string {
	dir := filepath.Dir(shelleyDBPath)
	return filepath.Join(dir, "memory.db")
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./memory/ -run TestOpen -v`
Expected: PASS

**Step 5: Commit**

```bash
git add memory/
git commit -m "memory: add database package with schema and migrations"
```

---

### Task 2: Chunking — conversations and workspace files

**Files:**
- Create: `memory/chunk.go`
- Create: `memory/chunk_test.go`

**Step 1: Write the failing test**

Create `memory/chunk_test.go`:

```go
package memory

import (
	"testing"
)

func TestChunkMessages(t *testing.T) {
	messages := []MessageText{
		{Role: "user", Text: "How do I write tests in Go?"},
		{Role: "agent", Text: "You can use the testing package. Here's an example..."},
		{Role: "user", Text: "What about table-driven tests?"},
		{Role: "agent", Text: "Table-driven tests are a common Go pattern..."},
	}

	chunks := ChunkMessages(messages, 1024)
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	// All messages fit in one chunk at 1024 token target
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for short messages, got %d", len(chunks))
	}
	// Chunk should contain role prefixes
	if !containsString(chunks[0].Text, "User:") {
		t.Error("chunk should contain role prefix 'User:'")
	}
	if !containsString(chunks[0].Text, "Agent:") {
		t.Error("chunk should contain role prefix 'Agent:'")
	}
}

func TestChunkMessagesMultipleChunks(t *testing.T) {
	// Create messages that exceed one chunk
	longText := make([]byte, 5000)
	for i := range longText {
		longText[i] = 'a'
	}
	messages := []MessageText{
		{Role: "user", Text: string(longText)},
		{Role: "agent", Text: string(longText)},
	}

	chunks := ChunkMessages(messages, 512)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for long messages, got %d", len(chunks))
	}
}

func TestChunkMarkdown(t *testing.T) {
	md := "# Section 1\n\nContent for section 1.\n\n# Section 2\n\nContent for section 2.\n"
	chunks := ChunkMarkdown(md, 1024)
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
}

func TestEstimateTokens(t *testing.T) {
	// Rough estimate: ~4 chars per token
	tokens := EstimateTokens("hello world")
	if tokens < 2 || tokens > 5 {
		t.Fatalf("unexpected token estimate for 'hello world': %d", tokens)
	}
}

func containsString(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && // avoid false positives
		len(s) >= len(substr) &&
		stringContains(s, substr)
}

func stringContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./memory/ -run TestChunk -v`
Expected: FAIL — types/functions not defined

**Step 3: Write minimal implementation**

Create `memory/chunk.go`:

```go
package memory

import (
	"fmt"
	"strings"
)

// MessageText is a simplified message for chunking (extracted from llm.Message).
type MessageText struct {
	Role string
	Text string
}

// Chunk is a piece of indexed content.
type Chunk struct {
	Text       string
	Index      int
	TokenCount int
}

// EstimateTokens returns a rough token count (~4 chars per token).
func EstimateTokens(s string) int {
	n := len(s) / 4
	if n == 0 && len(s) > 0 {
		n = 1
	}
	return n
}

// ChunkMessages groups messages into chunks targeting maxTokens per chunk.
// Splits at message boundaries — never splits mid-message.
func ChunkMessages(messages []MessageText, maxTokens int) []Chunk {
	if len(messages) == 0 {
		return nil
	}

	var chunks []Chunk
	var buf strings.Builder
	currentTokens := 0
	chunkIdx := 0

	for _, msg := range messages {
		line := fmt.Sprintf("%s: %s\n", roleLabel(msg.Role), msg.Text)
		lineTokens := EstimateTokens(line)

		// If adding this message exceeds the limit and buffer is non-empty, flush
		if currentTokens+lineTokens > maxTokens && buf.Len() > 0 {
			chunks = append(chunks, Chunk{
				Text:       buf.String(),
				Index:      chunkIdx,
				TokenCount: currentTokens,
			})
			buf.Reset()
			currentTokens = 0
			chunkIdx++
		}

		buf.WriteString(line)
		currentTokens += lineTokens
	}

	// Flush remaining
	if buf.Len() > 0 {
		chunks = append(chunks, Chunk{
			Text:       buf.String(),
			Index:      chunkIdx,
			TokenCount: currentTokens,
		})
	}

	return chunks
}

// ChunkMarkdown splits markdown by headings, targeting maxTokens per chunk.
func ChunkMarkdown(md string, maxTokens int) []Chunk {
	if md == "" {
		return nil
	}

	lines := strings.Split(md, "\n")
	var chunks []Chunk
	var buf strings.Builder
	currentTokens := 0
	chunkIdx := 0

	for _, line := range lines {
		isHeading := strings.HasPrefix(line, "# ")
		lineTokens := EstimateTokens(line + "\n")

		// Split at headings when buffer is non-empty and would exceed limit
		if isHeading && buf.Len() > 0 && currentTokens+lineTokens > maxTokens {
			chunks = append(chunks, Chunk{
				Text:       buf.String(),
				Index:      chunkIdx,
				TokenCount: currentTokens,
			})
			buf.Reset()
			currentTokens = 0
			chunkIdx++
		}

		buf.WriteString(line)
		buf.WriteByte('\n')
		currentTokens += lineTokens
	}

	if buf.Len() > 0 {
		chunks = append(chunks, Chunk{
			Text:       buf.String(),
			Index:      chunkIdx,
			TokenCount: currentTokens,
		})
	}

	return chunks
}

func roleLabel(role string) string {
	switch role {
	case "user":
		return "User"
	case "agent", "assistant":
		return "Agent"
	default:
		return strings.Title(role)
	}
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./memory/ -run TestChunk -v`
Expected: PASS

**Step 5: Commit**

```bash
git add memory/chunk.go memory/chunk_test.go
git commit -m "memory: add chunking for conversations and markdown files"
```

---

### Task 3: Embedding interface and providers

**Files:**
- Create: `memory/embed.go`
- Create: `memory/embed_test.go`

**Step 1: Write the failing test**

Create `memory/embed_test.go`:

```go
package memory

import (
	"context"
	"math"
	"testing"
)

func TestCosineSimilarity(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	sim := CosineSimilarity(a, b)
	if math.Abs(float64(sim)-1.0) > 0.001 {
		t.Fatalf("identical vectors should have similarity 1.0, got %f", sim)
	}

	c := []float32{0, 1, 0}
	sim = CosineSimilarity(a, c)
	if math.Abs(float64(sim)) > 0.001 {
		t.Fatalf("orthogonal vectors should have similarity 0.0, got %f", sim)
	}
}

func TestSerializeDeserializeEmbedding(t *testing.T) {
	original := []float32{1.5, -2.3, 0.0, 4.7}
	blob := SerializeEmbedding(original)
	result := DeserializeEmbedding(blob)
	if len(result) != len(original) {
		t.Fatalf("length mismatch: got %d, want %d", len(result), len(original))
	}
	for i := range original {
		if result[i] != original[i] {
			t.Fatalf("mismatch at index %d: got %f, want %f", i, result[i], original[i])
		}
	}
}

func TestNoneEmbedder(t *testing.T) {
	e := &NoneEmbedder{}
	result, err := e.Embed(context.Background(), []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatal("NoneEmbedder should return nil embeddings")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./memory/ -run "TestCosine|TestSerialize|TestNone" -v`
Expected: FAIL — types not defined

**Step 3: Write minimal implementation**

Create `memory/embed.go`:

```go
package memory

import (
	"context"
	"encoding/binary"
	"math"
)

// Embedder generates vector embeddings for text.
type Embedder interface {
	// Embed generates embeddings for the given texts. Returns nil if embeddings are disabled.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dimension returns the embedding vector dimension, or 0 if unknown/disabled.
	Dimension() int
}

// NoneEmbedder is a no-op embedder that disables vector search.
type NoneEmbedder struct{}

func (e *NoneEmbedder) Embed(_ context.Context, _ []string) ([][]float32, error) {
	return nil, nil
}

func (e *NoneEmbedder) Dimension() int { return 0 }

// CosineSimilarity computes cosine similarity between two vectors.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}

// SerializeEmbedding converts a float32 slice to a BLOB for SQLite storage.
func SerializeEmbedding(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// DeserializeEmbedding converts a BLOB back to a float32 slice.
func DeserializeEmbedding(blob []byte) []float32 {
	if len(blob)%4 != 0 {
		return nil
	}
	v := make([]float32, len(blob)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return v
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./memory/ -run "TestCosine|TestSerialize|TestNone" -v`
Expected: PASS

**Step 5: Commit**

```bash
git add memory/embed.go memory/embed_test.go
git commit -m "memory: add embedding interface, cosine similarity, and serialization"
```

---

### Task 4: Ollama embedding provider

**Files:**
- Create: `memory/embed_ollama.go`
- Create: `memory/embed_ollama_test.go`

**Step 1: Write the failing test**

Create `memory/embed_ollama_test.go`:

```go
package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaEmbedder(t *testing.T) {
	// Mock Ollama /api/embed endpoint
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Fatalf("unexpected method: %s", r.Method)
		}

		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		if req.Model != "nomic-embed-text" {
			t.Fatalf("unexpected model: %s", req.Model)
		}

		// Return fake embeddings
		resp := struct {
			Embeddings [][]float32 `json:"embeddings"`
		}{
			Embeddings: make([][]float32, len(req.Input)),
		}
		for i := range req.Input {
			resp.Embeddings[i] = []float32{0.1, 0.2, 0.3}
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	embedder := NewOllamaEmbedder(server.URL, "nomic-embed-text")
	results, err := embedder.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(results))
	}
	if len(results[0]) != 3 {
		t.Fatalf("expected dimension 3, got %d", len(results[0]))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./memory/ -run TestOllamaEmbedder -v`
Expected: FAIL — `NewOllamaEmbedder` not defined

**Step 3: Write minimal implementation**

Create `memory/embed_ollama.go`:

```go
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// OllamaEmbedder generates embeddings via the Ollama API.
type OllamaEmbedder struct {
	url   string
	model string
}

// NewOllamaEmbedder creates an embedder that calls the Ollama /api/embed endpoint.
func NewOllamaEmbedder(url, model string) *OllamaEmbedder {
	return &OllamaEmbedder{url: url, model: model}
}

func (e *OllamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	reqBody := struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}{
		Model: e.model,
		Input: texts,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("ollama embed: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", e.url+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama embed: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embed: status %d", resp.StatusCode)
	}

	var result struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("ollama embed: decode: %w", err)
	}

	return result.Embeddings, nil
}

func (e *OllamaEmbedder) Dimension() int { return 0 } // unknown until first call
```

**Step 4: Run test to verify it passes**

Run: `go test ./memory/ -run TestOllamaEmbedder -v`
Expected: PASS

**Step 5: Commit**

```bash
git add memory/embed_ollama.go memory/embed_ollama_test.go
git commit -m "memory: add Ollama embedding provider"
```

---

### Task 5: FTS5 search

**Files:**
- Modify: `memory/db.go`
- Create: `memory/search.go`
- Create: `memory/search_test.go`

**Step 1: Write the failing test**

Create `memory/search_test.go`:

```go
package memory

import (
	"path/filepath"
	"testing"
)

func TestFTSSearch(t *testing.T) {
	dir := t.TempDir()
	mdb, err := Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Insert test chunks
	chunks := []struct {
		id, sourceType, sourceID, sourceName, text string
		index                                      int
	}{
		{"c1", "conversation", "conv1", "auth-discussion", "We discussed JWT authentication for the API", 0},
		{"c2", "conversation", "conv1", "auth-discussion", "The login flow uses OAuth2 with refresh tokens", 1},
		{"c3", "conversation", "conv2", "database-design", "We decided to use SQLite for the database", 0},
		{"c4", "file", "CLAUDE.md", "CLAUDE.md", "This project uses Go and React", 0},
	}
	for _, c := range chunks {
		err := mdb.InsertChunk(c.id, c.sourceType, c.sourceID, c.sourceName, c.index, c.text, nil)
		if err != nil {
			t.Fatalf("insert chunk %s: %v", c.id, err)
		}
	}

	// Search for "authentication"
	results, err := mdb.SearchFTS("authentication", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'authentication'")
	}
	if results[0].ChunkID != "c1" {
		t.Fatalf("expected chunk c1 first, got %s", results[0].ChunkID)
	}

	// Search with source_type filter
	results, err = mdb.SearchFTS("Go React", "file", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 file result, got %d", len(results))
	}

	// Search for non-existent term
	results, err = mdb.SearchFTS("kubernetes", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./memory/ -run TestFTSSearch -v`
Expected: FAIL — `InsertChunk`, `SearchFTS`, `SearchResult` not defined

**Step 3: Write minimal implementation**

Create `memory/search.go`:

```go
package memory

import "fmt"

// SearchResult represents a single search result.
type SearchResult struct {
	ChunkID    string
	SourceType string
	SourceID   string
	SourceName string
	Text       string
	Score      float64
}

// InsertChunk inserts a chunk into the database. embedding may be nil.
func (d *DB) InsertChunk(chunkID, sourceType, sourceID, sourceName string, chunkIndex int, text string, embedding []byte) error {
	_, err := d.db.Exec(
		`INSERT OR REPLACE INTO chunks (chunk_id, source_type, source_id, source_name, chunk_index, text, token_count, embedding, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		chunkID, sourceType, sourceID, sourceName, chunkIndex, text, EstimateTokens(text), embedding,
	)
	return err
}

// DeleteChunksBySource removes all chunks for a given source.
func (d *DB) DeleteChunksBySource(sourceType, sourceID string) error {
	_, err := d.db.Exec(`DELETE FROM chunks WHERE source_type = ? AND source_id = ?`, sourceType, sourceID)
	return err
}

// SearchFTS performs a full-text search using FTS5 with BM25 ranking.
// sourceType may be empty to search all types.
func (d *DB) SearchFTS(query, sourceType string, limit int) ([]SearchResult, error) {
	if query == "" {
		return nil, nil
	}

	var sql string
	var args []any

	if sourceType != "" {
		sql = `SELECT c.chunk_id, c.source_type, c.source_id, c.source_name, c.text, rank
			FROM chunks_fts f
			JOIN chunks c ON c.rowid = f.rowid
			WHERE chunks_fts MATCH ?
			AND c.source_type = ?
			ORDER BY rank
			LIMIT ?`
		args = []any{query, sourceType, limit}
	} else {
		sql = `SELECT c.chunk_id, c.source_type, c.source_id, c.source_name, c.text, rank
			FROM chunks_fts f
			JOIN chunks c ON c.rowid = f.rowid
			WHERE chunks_fts MATCH ?
			ORDER BY rank
			LIMIT ?`
		args = []any{query, limit}
	}

	rows, err := d.db.Query(sql, args...)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ChunkID, &r.SourceType, &r.SourceID, &r.SourceName, &r.Text, &r.Score); err != nil {
			return nil, fmt.Errorf("fts search scan: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./memory/ -run TestFTSSearch -v`
Expected: PASS

**Step 5: Commit**

```bash
git add memory/search.go memory/search_test.go
git commit -m "memory: add FTS5 search and chunk insertion"
```

---

### Task 6: Vector search (pure Go cosine similarity)

**Files:**
- Modify: `memory/search.go`
- Modify: `memory/search_test.go`

**Step 1: Write the failing test**

Add to `memory/search_test.go`:

```go
func TestVectorSearch(t *testing.T) {
	dir := t.TempDir()
	mdb, err := Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Insert chunks with embeddings
	// embed1 is close to query, embed2 is orthogonal, embed3 is somewhat close
	embed1 := SerializeEmbedding([]float32{0.9, 0.1, 0.0})
	embed2 := SerializeEmbedding([]float32{0.0, 0.0, 1.0})
	embed3 := SerializeEmbedding([]float32{0.7, 0.3, 0.1})

	mdb.InsertChunk("c1", "conversation", "conv1", "test1", 0, "close match", embed1)
	mdb.InsertChunk("c2", "conversation", "conv2", "test2", 0, "orthogonal", embed2)
	mdb.InsertChunk("c3", "conversation", "conv3", "test3", 0, "somewhat close", embed3)

	queryVec := []float32{1.0, 0.0, 0.0}
	results, err := mdb.SearchVector(queryVec, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// c1 should be most similar to [1,0,0]
	if results[0].ChunkID != "c1" {
		t.Fatalf("expected c1 first (most similar), got %s", results[0].ChunkID)
	}
	// c2 should be least similar
	if results[len(results)-1].ChunkID != "c2" {
		t.Fatalf("expected c2 last (least similar), got %s", results[len(results)-1].ChunkID)
	}
}

func TestVectorSearchNoEmbeddings(t *testing.T) {
	dir := t.TempDir()
	mdb, err := Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Insert chunks without embeddings
	mdb.InsertChunk("c1", "conversation", "conv1", "test", 0, "no embedding", nil)

	results, err := mdb.SearchVector([]float32{1, 0, 0}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results when no embeddings, got %d", len(results))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./memory/ -run TestVectorSearch -v`
Expected: FAIL — `SearchVector` not defined

**Step 3: Write minimal implementation**

Add to `memory/search.go`:

```go
// SearchVector performs brute-force cosine similarity search over stored embeddings.
// sourceType may be empty to search all types.
func (d *DB) SearchVector(queryVec []float32, sourceType string, limit int) ([]SearchResult, error) {
	if len(queryVec) == 0 {
		return nil, nil
	}

	var sql string
	var args []any
	if sourceType != "" {
		sql = `SELECT chunk_id, source_type, source_id, source_name, text, embedding FROM chunks WHERE embedding IS NOT NULL AND source_type = ?`
		args = []any{sourceType}
	} else {
		sql = `SELECT chunk_id, source_type, source_id, source_name, text, embedding FROM chunks WHERE embedding IS NOT NULL`
	}

	rows, err := d.db.Query(sql, args...)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	defer rows.Close()

	type scored struct {
		result SearchResult
		sim    float32
	}
	var all []scored

	for rows.Next() {
		var r SearchResult
		var embBlob []byte
		if err := rows.Scan(&r.ChunkID, &r.SourceType, &r.SourceID, &r.SourceName, &r.Text, &embBlob); err != nil {
			return nil, fmt.Errorf("vector search scan: %w", err)
		}
		emb := DeserializeEmbedding(embBlob)
		sim := CosineSimilarity(queryVec, emb)
		all = append(all, scored{result: r, sim: sim})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Sort by similarity descending
	sort.Slice(all, func(i, j int) bool {
		return all[i].sim > all[j].sim
	})

	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}

	results := make([]SearchResult, len(all))
	for i, s := range all {
		s.result.Score = float64(s.sim)
		results[i] = s.result
	}
	return results, nil
}
```

Add `"sort"` to imports in `search.go`.

**Step 4: Run test to verify it passes**

Run: `go test ./memory/ -run TestVectorSearch -v`
Expected: PASS

**Step 5: Commit**

```bash
git add memory/search.go memory/search_test.go
git commit -m "memory: add pure-Go vector search with cosine similarity"
```

---

### Task 7: Hybrid search (merge FTS + vector)

**Files:**
- Modify: `memory/search.go`
- Modify: `memory/search_test.go`

**Step 1: Write the failing test**

Add to `memory/search_test.go`:

```go
func TestHybridSearch(t *testing.T) {
	dir := t.TempDir()
	mdb, err := Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// c1: strong FTS match (keyword "authentication"), medium vector match
	// c2: no FTS match, strong vector match
	// c3: medium FTS match, medium vector match
	embed1 := SerializeEmbedding([]float32{0.5, 0.5, 0.0})
	embed2 := SerializeEmbedding([]float32{0.9, 0.1, 0.0})
	embed3 := SerializeEmbedding([]float32{0.6, 0.4, 0.0})

	mdb.InsertChunk("c1", "conversation", "conv1", "auth", 0, "authentication and login flow", embed1)
	mdb.InsertChunk("c2", "conversation", "conv2", "other", 0, "something completely unrelated to search terms", embed2)
	mdb.InsertChunk("c3", "conversation", "conv3", "partial", 0, "partial authentication reference", embed3)

	queryVec := []float32{1.0, 0.0, 0.0}
	results, err := mdb.HybridSearch("authentication", queryVec, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	// Results should include both FTS and vector matches
	foundC1, foundC2 := false, false
	for _, r := range results {
		if r.ChunkID == "c1" {
			foundC1 = true
		}
		if r.ChunkID == "c2" {
			foundC2 = true
		}
	}
	if !foundC1 {
		t.Error("expected c1 (FTS match) in results")
	}
	if !foundC2 {
		t.Error("expected c2 (vector match) in results")
	}
}

func TestHybridSearchFTSOnly(t *testing.T) {
	dir := t.TempDir()
	mdb, err := Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// No embeddings — should fall back to FTS only
	mdb.InsertChunk("c1", "conversation", "conv1", "test", 0, "authentication flow", nil)
	results, err := mdb.HybridSearch("authentication", nil, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected FTS-only results")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./memory/ -run TestHybridSearch -v`
Expected: FAIL — `HybridSearch` not defined

**Step 3: Write minimal implementation**

Add to `memory/search.go`:

```go
const (
	ftsWeight    = 0.4
	vectorWeight = 0.6
)

// HybridSearch combines FTS5 and vector search results with weighted scoring.
// If queryVec is nil, falls back to FTS-only.
func (d *DB) HybridSearch(query string, queryVec []float32, sourceType string, limit int) ([]SearchResult, error) {
	// FTS search
	ftsResults, err := d.SearchFTS(query, sourceType, 20)
	if err != nil {
		return nil, err
	}

	// Vector search (skip if no query vector)
	var vecResults []SearchResult
	if len(queryVec) > 0 {
		vecResults, err = d.SearchVector(queryVec, sourceType, 20)
		if err != nil {
			return nil, err
		}
	}

	// If only FTS results, return them directly
	if len(vecResults) == 0 {
		if limit > 0 && len(ftsResults) > limit {
			ftsResults = ftsResults[:limit]
		}
		return ftsResults, nil
	}

	// Normalize and merge
	merged := mergeResults(ftsResults, vecResults, limit)
	return merged, nil
}

func mergeResults(ftsResults, vecResults []SearchResult, limit int) []SearchResult {
	// Normalize FTS scores to [0,1] (BM25 ranks are negative, lower = better)
	ftsNorm := normalizeScores(ftsResults, true)
	// Normalize vector scores to [0,1] (cosine similarity, higher = better)
	vecNorm := normalizeScores(vecResults, false)

	// Merge by chunk_id
	scores := make(map[string]float64)
	byID := make(map[string]SearchResult)

	for id, score := range ftsNorm {
		scores[id] += score * ftsWeight
		// Find the result to keep
		for _, r := range ftsResults {
			if r.ChunkID == id {
				byID[id] = r
				break
			}
		}
	}
	for id, score := range vecNorm {
		scores[id] += score * vectorWeight
		if _, ok := byID[id]; !ok {
			for _, r := range vecResults {
				if r.ChunkID == id {
					byID[id] = r
					break
				}
			}
		}
	}

	// Collect and sort
	type entry struct {
		id    string
		score float64
	}
	var entries []entry
	for id, score := range scores {
		entries = append(entries, entry{id, score})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].score > entries[j].score
	})

	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}

	results := make([]SearchResult, len(entries))
	for i, e := range entries {
		r := byID[e.id]
		r.Score = e.score
		results[i] = r
	}
	return results
}

// normalizeScores normalizes result scores to [0,1].
// If invert is true, treats lower raw scores as better (BM25).
func normalizeScores(results []SearchResult, invert bool) map[string]float64 {
	norm := make(map[string]float64, len(results))
	if len(results) == 0 {
		return norm
	}

	minScore, maxScore := results[0].Score, results[0].Score
	for _, r := range results[1:] {
		if r.Score < minScore {
			minScore = r.Score
		}
		if r.Score > maxScore {
			maxScore = r.Score
		}
	}

	rng := maxScore - minScore
	for _, r := range results {
		var normalized float64
		if rng == 0 {
			normalized = 1.0 // all same score
		} else if invert {
			normalized = 1.0 - (r.Score-minScore)/rng
		} else {
			normalized = (r.Score - minScore) / rng
		}
		norm[r.ChunkID] = normalized
	}
	return norm
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./memory/ -run TestHybridSearch -v`
Expected: PASS

**Step 5: Commit**

```bash
git add memory/search.go memory/search_test.go
git commit -m "memory: add hybrid FTS+vector search with weighted merge"
```

---

### Task 8: Indexing pipeline

**Files:**
- Create: `memory/index.go`
- Create: `memory/index_test.go`

**Step 1: Write the failing test**

Create `memory/index_test.go`:

```go
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
		{Role: "user", Text: "How should we handle authentication?"},
		{Role: "agent", Text: "I recommend using JWT tokens with refresh token rotation."},
	}

	err = mdb.IndexConversation(context.Background(), "conv1", "auth-discussion", messages, &NoneEmbedder{})
	if err != nil {
		t.Fatal(err)
	}

	// Verify chunks were created
	results, err := mdb.SearchFTS("authentication", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected searchable chunks after indexing")
	}
	if results[0].SourceName != "auth-discussion" {
		t.Fatalf("expected source_name 'auth-discussion', got '%s'", results[0].SourceName)
	}

	// Verify index_state was recorded
	indexed, err := mdb.IsIndexed("conversation", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if indexed {
		t.Fatal("IsIndexed should return false with empty hash (hash won't match)")
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
		{Role: "user", Text: "test message"},
	}

	// Index once
	err = mdb.IndexConversation(context.Background(), "conv1", "test", messages, &NoneEmbedder{})
	if err != nil {
		t.Fatal(err)
	}

	// Index again with same content — should skip
	err = mdb.IndexConversation(context.Background(), "conv1", "test", messages, &NoneEmbedder{})
	if err != nil {
		t.Fatal(err)
	}

	// Should still have chunks from the first indexing
	results, err := mdb.SearchFTS("test message", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected chunks after re-indexing")
	}
}

func TestIndexFile(t *testing.T) {
	dir := t.TempDir()
	mdb, err := Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	content := "# Project\n\nThis project uses Go and React.\n"
	err = mdb.IndexFile(context.Background(), "/path/to/CLAUDE.md", "CLAUDE.md", content, &NoneEmbedder{})
	if err != nil {
		t.Fatal(err)
	}

	results, err := mdb.SearchFTS("Go React", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected searchable chunks after file indexing")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./memory/ -run "TestIndex" -v`
Expected: FAIL — functions not defined

**Step 3: Write minimal implementation**

Create `memory/index.go`:

```go
package memory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
)

// IndexConversation indexes a conversation's messages into the memory database.
// Skips indexing if the content hash hasn't changed since last index.
func (d *DB) IndexConversation(ctx context.Context, conversationID, slug string, messages []MessageText, embedder Embedder) error {
	// Compute content hash
	hash := hashMessages(messages)

	// Check if already indexed with same hash
	indexed, err := d.IsIndexed("conversation", conversationID, hash)
	if err != nil {
		return err
	}
	if indexed {
		return nil
	}

	// Chunk messages
	chunks := ChunkMessages(messages, 1024)
	if len(chunks) == 0 {
		return nil
	}

	// Delete old chunks for this conversation
	if err := d.DeleteChunksBySource("conversation", conversationID); err != nil {
		return fmt.Errorf("index conversation: delete old: %w", err)
	}

	// Generate embeddings if available
	var embeddings [][]float32
	if embedder != nil {
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.Text
		}
		embeddings, _ = embedder.Embed(ctx, texts) // ignore error — degrade gracefully
	}

	// Insert chunks
	for i, c := range chunks {
		chunkID := fmt.Sprintf("%s_%d", conversationID, i)
		var embBlob []byte
		if embeddings != nil && i < len(embeddings) {
			embBlob = SerializeEmbedding(embeddings[i])
		}
		if err := d.InsertChunk(chunkID, "conversation", conversationID, slug, c.Index, c.Text, embBlob); err != nil {
			return fmt.Errorf("index conversation: insert chunk %d: %w", i, err)
		}
	}

	// Update index state
	return d.SetIndexState("conversation", conversationID, hash)
}

// IndexFile indexes a workspace file into the memory database.
func (d *DB) IndexFile(ctx context.Context, filePath, fileName, content string, embedder Embedder) error {
	hash := hashString(content)

	indexed, err := d.IsIndexed("file", filePath, hash)
	if err != nil {
		return err
	}
	if indexed {
		return nil
	}

	chunks := ChunkMarkdown(content, 1024)
	if len(chunks) == 0 {
		return nil
	}

	if err := d.DeleteChunksBySource("file", filePath); err != nil {
		return fmt.Errorf("index file: delete old: %w", err)
	}

	var embeddings [][]float32
	if embedder != nil {
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.Text
		}
		embeddings, _ = embedder.Embed(ctx, texts)
	}

	for i, c := range chunks {
		chunkID := fmt.Sprintf("file_%s_%d", hashString(filePath)[:8], i)
		var embBlob []byte
		if embeddings != nil && i < len(embeddings) {
			embBlob = SerializeEmbedding(embeddings[i])
		}
		if err := d.InsertChunk(chunkID, "file", filePath, fileName, c.Index, c.Text, embBlob); err != nil {
			return fmt.Errorf("index file: insert chunk %d: %w", i, err)
		}
	}

	return d.SetIndexState("file", filePath, hash)
}

// IsIndexed returns true if the source has been indexed with the given hash.
func (d *DB) IsIndexed(sourceType, sourceID, hash string) (bool, error) {
	var storedHash string
	err := d.db.QueryRow(
		`SELECT hash FROM index_state WHERE source_type = ? AND source_id = ?`,
		sourceType, sourceID,
	).Scan(&storedHash)
	if err != nil {
		return false, nil // not indexed yet
	}
	return storedHash == hash && hash != "", nil
}

// SetIndexState records that a source has been indexed.
func (d *DB) SetIndexState(sourceType, sourceID, hash string) error {
	_, err := d.db.Exec(
		`INSERT OR REPLACE INTO index_state (source_type, source_id, indexed_at, hash)
		 VALUES (?, ?, datetime('now'), ?)`,
		sourceType, sourceID, hash,
	)
	return err
}

func hashMessages(messages []MessageText) string {
	var buf strings.Builder
	for _, m := range messages {
		buf.WriteString(m.Role)
		buf.WriteString(m.Text)
	}
	return hashString(buf.String())
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./memory/ -run "TestIndex" -v`
Expected: PASS

**Step 5: Commit**

```bash
git add memory/index.go memory/index_test.go
git commit -m "memory: add indexing pipeline for conversations and files"
```

---

### Task 9: The `memory_search` tool

**Files:**
- Create: `claudetool/memory/tool.go`
- Create: `claudetool/memory/tool_test.go`

**Step 1: Write the failing test**

Create `claudetool/memory/tool_test.go`:

```go
package memory

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	memdb "shelley.exe.dev/memory"
)

func TestMemorySearchTool(t *testing.T) {
	dir := t.TempDir()
	mdb, err := memdb.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Seed with test data
	mdb.InsertChunk("c1", "conversation", "conv1", "auth-chat", 0, "We discussed JWT authentication", nil)
	mdb.InsertChunk("c2", "file", "CLAUDE.md", "CLAUDE.md", 0, "This project uses Go and React", nil)

	tool := NewMemorySearchTool(mdb, nil)
	llmTool := tool.Tool()

	if llmTool.Name != "memory_search" {
		t.Fatalf("unexpected tool name: %s", llmTool.Name)
	}

	// Search
	input, _ := json.Marshal(map[string]any{"query": "authentication"})
	result := llmTool.Run(context.Background(), input)
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if len(result.LLMContent) == 0 {
		t.Fatal("expected LLMContent")
	}
	if result.LLMContent[0].Text == "" {
		t.Fatal("expected non-empty text result")
	}
}

func TestMemorySearchToolNoDatabase(t *testing.T) {
	tool := NewMemorySearchTool(nil, nil)
	llmTool := tool.Tool()

	input, _ := json.Marshal(map[string]any{"query": "test"})
	result := llmTool.Run(context.Background(), input)
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	// Should return a helpful message about no memory index
	if len(result.LLMContent) == 0 {
		t.Fatal("expected a message about missing index")
	}
}

func TestMemorySearchToolEmptyResults(t *testing.T) {
	dir := t.TempDir()
	mdb, err := memdb.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	tool := NewMemorySearchTool(mdb, nil)
	llmTool := tool.Tool()

	input, _ := json.Marshal(map[string]any{"query": "nonexistent"})
	result := llmTool.Run(context.Background(), input)
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	// Should return "no results" message
	if len(result.LLMContent) == 0 || result.LLMContent[0].Text == "" {
		t.Fatal("expected a 'no results' message")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./claudetool/memory/ -v`
Expected: FAIL — package doesn't exist

**Step 3: Write minimal implementation**

Create `claudetool/memory/tool.go`:

```go
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"shelley.exe.dev/llm"
	memdb "shelley.exe.dev/memory"
)

const (
	toolName        = "memory_search"
	toolDescription = `Search past conversations and workspace files for relevant context.
Use this when you need to recall previous discussions, decisions, or information from earlier sessions.`
	toolInputSchema = `{
  "type": "object",
  "required": ["query"],
  "properties": {
    "query": {
      "type": "string",
      "description": "Natural language search query"
    },
    "source_type": {
      "type": "string",
      "enum": ["conversation", "file", "all"],
      "description": "Filter by source type. Default: all."
    },
    "limit": {
      "type": "integer",
      "description": "Maximum number of results to return. Default: 10, max: 25."
    }
  }
}`
)

type MemorySearchTool struct {
	db       *memdb.DB
	embedder memdb.Embedder
}

func NewMemorySearchTool(db *memdb.DB, embedder memdb.Embedder) *MemorySearchTool {
	return &MemorySearchTool{db: db, embedder: embedder}
}

func (t *MemorySearchTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        toolName,
		Description: toolDescription,
		InputSchema: llm.MustSchema(toolInputSchema),
		Run:         t.Run,
	}
}

type searchInput struct {
	Query      string `json:"query"`
	SourceType string `json:"source_type"`
	Limit      int    `json:"limit"`
}

func (t *MemorySearchTool) Run(ctx context.Context, input json.RawMessage) llm.ToolOut {
	var in searchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return llm.ErrorfToolOut("invalid input: %v", err)
	}

	if in.Query == "" {
		return llm.ErrorfToolOut("query is required")
	}

	if t.db == nil {
		return llm.ToolOut{
			LLMContent: llm.TextContent("No memory index found. Memory will be indexed after conversations complete."),
		}
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 25 {
		limit = 25
	}

	sourceType := in.SourceType
	if sourceType == "all" {
		sourceType = ""
	}

	// Get query embedding if embedder is available
	var queryVec []float32
	if t.embedder != nil {
		vecs, err := t.embedder.Embed(ctx, []string{in.Query})
		if err == nil && len(vecs) > 0 {
			queryVec = vecs[0]
		}
	}

	results, err := t.db.HybridSearch(in.Query, queryVec, sourceType, limit)
	if err != nil {
		return llm.ErrorfToolOut("search failed: %v", err)
	}

	if len(results) == 0 {
		return llm.ToolOut{
			LLMContent: llm.TextContent(fmt.Sprintf("No relevant memories found for: %s", in.Query)),
		}
	}

	// Format results for LLM
	var buf strings.Builder
	fmt.Fprintf(&buf, "Found %d results:\n\n", len(results))
	for i, r := range results {
		sourceLabel := r.SourceName
		if sourceLabel == "" {
			sourceLabel = r.SourceID
		}
		fmt.Fprintf(&buf, "--- Result %d [%s: %s] (score: %.2f) ---\n%s\n\n",
			i+1, r.SourceType, sourceLabel, r.Score, r.Text)
	}

	// Display data for UI
	type displayResult struct {
		ChunkID    string  `json:"chunk_id"`
		SourceType string  `json:"source_type"`
		SourceID   string  `json:"source_id"`
		SourceName string  `json:"source_name"`
		Score      float64 `json:"score"`
		Preview    string  `json:"preview"`
	}
	display := make([]displayResult, len(results))
	for i, r := range results {
		preview := r.Text
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		display[i] = displayResult{
			ChunkID:    r.ChunkID,
			SourceType: r.SourceType,
			SourceID:   r.SourceID,
			SourceName: r.SourceName,
			Score:      r.Score,
			Preview:    preview,
		}
	}

	return llm.ToolOut{
		LLMContent: llm.TextContent(buf.String()),
		Display:    display,
	}
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./claudetool/memory/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add claudetool/memory/
git commit -m "claudetool: add memory_search tool"
```

---

### Task 10: Register tool in ToolSet and wire config

**Files:**
- Modify: `claudetool/toolset.go:39-74` (add MemoryDB/Embedder to ToolSetConfig)
- Modify: `claudetool/toolset.go:108-197` (register tool in NewToolSet)

**Step 1: Add config fields to ToolSetConfig**

In `claudetool/toolset.go`, add to the `ToolSetConfig` struct:

```go
// MemoryDB is the memory search database. If set, the memory_search tool is available.
MemoryDB    MemoryDB
// MemoryEmbedder is the embedder for memory search queries. May be nil (FTS-only).
MemoryEmbedder MemoryEmbedder
```

Also add the interface definitions (to avoid importing the memory package directly):

```go
// MemoryDB is the interface for the memory search database, satisfied by *memory.DB.
type MemoryDB interface {
	HybridSearch(query string, queryVec []float32, sourceType string, limit int) ([]memtool.SearchResult, error)
}

// MemoryEmbedder is the interface for query-time embedding, satisfied by memory.Embedder.
type MemoryEmbedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}
```

Wait — to keep this simple and avoid circular imports, we should pass the already-constructed `*llm.Tool` directly. Follow the pattern used by browser and LSP tools.

**Revised approach:** Add `MemorySearchTool *llm.Tool` to `ToolSetConfig`, and have the server construct it.

In `claudetool/toolset.go`, add to `ToolSetConfig`:

```go
// MemorySearchTool is the pre-built memory search tool. If set, it's added to the tool set.
MemorySearchTool *llm.Tool
```

In `NewToolSet()`, after the subagent tool block (~line 158), add:

```go
if cfg.MemorySearchTool != nil {
    tools = append(tools, cfg.MemorySearchTool)
}
```

**Step 2: Run existing tests**

Run: `go test ./claudetool/ -v`
Expected: PASS (no behavior change, just a new optional field)

**Step 3: Commit**

```bash
git add claudetool/toolset.go
git commit -m "claudetool: add MemorySearchTool slot in ToolSetConfig"
```

---

### Task 11: Server integration — startup and post-conversation indexing

**Files:**
- Modify: `server/server.go` — add memory DB opening at startup
- Modify: `server/convo.go` — trigger indexing when loop ends
- Modify: `server/system_prompt.txt:73-88` — replace SQL examples with tool reference

This is the wiring task that connects the memory package to the server. The specifics depend on how the server is constructed (look at `cmd/shelley/` for the startup flow).

**Step 1: Read the server startup code**

Read `cmd/shelley/` to understand where `db.New()` is called and how the server is configured. The memory DB should be opened right after `shelley.db`, and the memory search tool should be passed through `toolSetConfig`.

**Step 2: Open memory.db at startup**

In the server startup (likely `cmd/shelley/serve.go` or `server/server.go`), after opening the main DB:

```go
memoryDB, err := memory.Open(memory.MemoryDBPath(dbPath))
if err != nil {
    logger.Warn("Failed to open memory database", "error", err)
    // Non-fatal — memory search just won't be available
}
```

Create the embedder based on config, create the tool, and pass it to `toolSetConfig.MemorySearchTool`.

**Step 3: Trigger indexing after conversation loop ends**

In `server/convo.go`, in the goroutine that runs `loopInstance.Go()` (~line 483), after the loop returns:

```go
go func() {
    if err := loopInstance.Go(processCtx); err != nil && err != context.DeadlineExceeded && err != context.Canceled {
        // existing error logging...
    }
    // Index conversation for memory search
    cm.indexForMemory(context.Background())
}()
```

Add `indexForMemory` method that:
1. Loads messages from DB
2. Extracts text from `llm_data` JSON
3. Calls `memoryDB.IndexConversation()`

**Step 4: Update system prompt**

In `server/system_prompt.txt`, replace lines 73-88 (`<previous_conversations>` block) with:

```
{{if .ShelleyDBPath}}
<previous_conversations>
You have a memory_search tool that can search your past conversations and workspace files.
Use it when the user references previous discussions or when you need to recall earlier decisions.
</previous_conversations>
{{end}}
```

**Step 5: Run tests**

Run: `make test-go`
Expected: PASS

**Step 6: Commit**

```bash
git add cmd/ server/ memory/
git commit -m "server: wire memory search — startup, post-conversation indexing, system prompt"
```

---

### Task 12: End-to-end integration test

**Files:**
- Create: `memory/integration_test.go`

**Step 1: Write the integration test**

Create `memory/integration_test.go`:

```go
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

	// Index a conversation
	messages := []MessageText{
		{Role: "user", Text: "How should we handle user authentication in our API?"},
		{Role: "agent", Text: "I recommend JWT with refresh tokens. Store the refresh token in HttpOnly cookies."},
		{Role: "user", Text: "What about session management?"},
		{Role: "agent", Text: "For sessions, use server-side storage with Redis. Each session gets a unique ID."},
	}
	err = mdb.IndexConversation(context.Background(), "conv1", "auth-discussion", messages, &NoneEmbedder{})
	if err != nil {
		t.Fatal(err)
	}

	// Index a workspace file
	claude := "# Project\n\nBackend is Go with SQLite. Frontend is React/TypeScript.\n\n# Auth\n\nUses JWT tokens.\n"
	err = mdb.IndexFile(context.Background(), "/project/CLAUDE.md", "CLAUDE.md", claude, &NoneEmbedder{})
	if err != nil {
		t.Fatal(err)
	}

	// Search should find conversation content
	results, err := mdb.HybridSearch("authentication JWT", nil, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'authentication JWT'")
	}

	// Search with source_type filter
	results, err = mdb.HybridSearch("JWT", nil, "file", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected file results for 'JWT'")
	}
	for _, r := range results {
		if r.SourceType != "file" {
			t.Fatalf("expected source_type 'file', got '%s'", r.SourceType)
		}
	}

	// Re-index same content should be a no-op (same hash)
	err = mdb.IndexConversation(context.Background(), "conv1", "auth-discussion", messages, &NoneEmbedder{})
	if err != nil {
		t.Fatal(err)
	}

	// Add a second conversation and verify both are searchable
	messages2 := []MessageText{
		{Role: "user", Text: "Let's set up the database schema"},
		{Role: "agent", Text: "I'll create the migrations with SQLite tables for users and sessions."},
	}
	err = mdb.IndexConversation(context.Background(), "conv2", "db-setup", messages2, &NoneEmbedder{})
	if err != nil {
		t.Fatal(err)
	}

	results, err = mdb.HybridSearch("database schema", nil, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'database schema'")
	}
}
```

**Step 2: Run test**

Run: `go test ./memory/ -run TestIntegration -v`
Expected: PASS

**Step 3: Commit**

```bash
git add memory/integration_test.go
git commit -m "memory: add end-to-end integration test"
```

---

### Task 13: Run full test suite and fix any issues

**Step 1: Run all memory tests**

Run: `go test ./memory/ -v`
Expected: All PASS

**Step 2: Run all claudetool tests**

Run: `go test ./claudetool/... -v`
Expected: All PASS

**Step 3: Build the project**

Run: `make build`
Expected: Clean build

**Step 4: Run full test suite**

Run: `make test-go`
Expected: All PASS

**Step 5: Fix any issues found, then commit**

```bash
git add -A
git commit -m "memory: fix issues found in full test suite"
```
