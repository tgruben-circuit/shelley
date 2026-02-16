# Topic-Consolidated Memory System Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace flat chunk-based memory with LLM-extracted structured cells organized into topics with incremental consolidation, solving stale context in memory search.

**Architecture:** New schema with `cells` (typed, scored knowledge units) and `topics` (auto-discovered themes with consolidated summaries). Post-conversation indexing uses an LLM to extract cells, assign them to topics, and conditionally consolidate. Two-tier search returns topic summaries first, then individual cells with time-decay.

**Tech Stack:** Go, SQLite/FTS5, existing embedding providers (OpenAI/Ollama), existing LLM service interface (`llm.Service`)

**Design doc:** `docs/plans/2026-02-15-topic-consolidated-memory-design.md`

---

### Task 1: New Schema

**Files:**
- Modify: `memory/schema.sql`
- Modify: `memory/db.go`
- Test: `memory/db_test.go`

**Step 1: Write failing test for new schema**

Add to `memory/db_test.go`:

```go
func TestNewSchemaCreatesCellsAndTopics(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "memory.db")
	mdb, err := memory.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Verify cells table exists
	var cellsCount int
	err = mdb.QueryRow("SELECT COUNT(*) FROM cells").Scan(&cellsCount)
	if err != nil {
		t.Fatalf("cells table should exist: %v", err)
	}

	// Verify topics table exists
	var topicsCount int
	err = mdb.QueryRow("SELECT COUNT(*) FROM topics").Scan(&topicsCount)
	if err != nil {
		t.Fatalf("topics table should exist: %v", err)
	}

	// Verify FTS tables exist
	var ftsCellsCount int
	err = mdb.QueryRow("SELECT COUNT(*) FROM cells_fts").Scan(&ftsCellsCount)
	if err != nil {
		t.Fatalf("cells_fts table should exist: %v", err)
	}

	var ftsTopicsCount int
	err = mdb.QueryRow("SELECT COUNT(*) FROM topics_fts").Scan(&ftsTopicsCount)
	if err != nil {
		t.Fatalf("topics_fts table should exist: %v", err)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./memory -run TestNewSchemaCreatesCellsAndTopics -v`
Expected: FAIL — `cells` table does not exist

**Step 3: Implement new schema**

Replace `memory/schema.sql` with:

```sql
CREATE TABLE IF NOT EXISTS cells (
    cell_id     TEXT PRIMARY KEY,
    topic_id    TEXT,
    source_type TEXT NOT NULL CHECK(source_type IN ('conversation', 'file')),
    source_id   TEXT NOT NULL,
    source_name TEXT,
    cell_type   TEXT NOT NULL CHECK(cell_type IN ('fact', 'decision', 'preference', 'task', 'risk', 'code_ref')),
    salience    REAL NOT NULL DEFAULT 0.5,
    content     TEXT NOT NULL,
    embedding   BLOB,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    superseded  BOOLEAN DEFAULT FALSE
);
CREATE INDEX IF NOT EXISTS idx_cells_source ON cells(source_type, source_id);
CREATE INDEX IF NOT EXISTS idx_cells_topic ON cells(topic_id);

CREATE VIRTUAL TABLE IF NOT EXISTS cells_fts USING fts5(
    content,
    content='cells',
    content_rowid='rowid'
);

CREATE TRIGGER IF NOT EXISTS cells_ai AFTER INSERT ON cells BEGIN
    INSERT INTO cells_fts(rowid, content) VALUES (new.rowid, new.content);
END;
CREATE TRIGGER IF NOT EXISTS cells_ad AFTER DELETE ON cells BEGIN
    INSERT INTO cells_fts(cells_fts, rowid, content) VALUES('delete', old.rowid, old.content);
END;
CREATE TRIGGER IF NOT EXISTS cells_au AFTER UPDATE ON cells BEGIN
    INSERT INTO cells_fts(cells_fts, rowid, content) VALUES('delete', old.rowid, old.content);
    INSERT INTO cells_fts(rowid, content) VALUES (new.rowid, new.content);
END;

CREATE TABLE IF NOT EXISTS topics (
    topic_id    TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    summary     TEXT,
    embedding   BLOB,
    cell_count  INTEGER DEFAULT 0,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE VIRTUAL TABLE IF NOT EXISTS topics_fts USING fts5(
    summary,
    content='topics',
    content_rowid='rowid'
);

CREATE TRIGGER IF NOT EXISTS topics_au AFTER UPDATE OF summary ON topics BEGIN
    INSERT INTO topics_fts(topics_fts, rowid, summary) VALUES('delete', old.rowid, COALESCE(old.summary, ''));
    INSERT INTO topics_fts(rowid, summary) VALUES (new.rowid, COALESCE(new.summary, ''));
END;
CREATE TRIGGER IF NOT EXISTS topics_ai AFTER INSERT ON topics BEGIN
    INSERT INTO topics_fts(rowid, summary) VALUES (new.rowid, COALESCE(new.summary, ''));
END;
CREATE TRIGGER IF NOT EXISTS topics_ad AFTER DELETE ON topics BEGIN
    INSERT INTO topics_fts(topics_fts, rowid, summary) VALUES('delete', old.rowid, COALESCE(old.summary, ''));
END;

CREATE TABLE IF NOT EXISTS index_state (
    source_type TEXT NOT NULL,
    source_id   TEXT NOT NULL,
    indexed_at  DATETIME NOT NULL,
    hash        TEXT,
    PRIMARY KEY (source_type, source_id)
);
```

Expose a `QueryRow` method on `DB` for testing:

In `memory/db.go`, add:

```go
func (d *DB) QueryRow(query string, args ...any) *sql.Row {
	return d.db.QueryRow(query, args...)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./memory -run TestNewSchemaCreatesCellsAndTopics -v`
Expected: PASS

**Step 5: Commit**

```bash
git add memory/schema.sql memory/db.go memory/db_test.go
git commit -m "feat(memory): new schema with cells, topics, and FTS5 tables"
```

---

### Task 2: Cell and Topic DB Operations

**Files:**
- Create: `memory/cell.go`
- Test: `memory/cell_test.go`

**Step 1: Write failing tests for cell CRUD**

Create `memory/cell_test.go`:

```go
package memory_test

import (
	"path/filepath"
	"testing"

	"github.com/tgruben-circuit/percy/memory"
)

func TestInsertAndSearchCells(t *testing.T) {
	dir := t.TempDir()
	mdb, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	cell := memory.Cell{
		CellID:     "cell_1",
		TopicID:    "topic_auth",
		SourceType: "conversation",
		SourceID:   "conv_123",
		SourceName: "auth-discussion",
		CellType:   "decision",
		Salience:   0.9,
		Content:    "Authentication uses JWT with RS256 signing",
	}

	if err := mdb.InsertCell(cell); err != nil {
		t.Fatal(err)
	}

	results, err := mdb.SearchCellsFTS("JWT authentication", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	if results[0].CellID != "cell_1" {
		t.Errorf("expected cell_1, got %s", results[0].CellID)
	}
}

func TestSupersedeCells(t *testing.T) {
	dir := t.TempDir()
	mdb, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	mdb.InsertCell(memory.Cell{
		CellID: "cell_old", TopicID: "topic_1", SourceType: "conversation",
		SourceID: "c1", CellType: "decision", Salience: 0.8,
		Content: "Using OAuth for auth",
	})
	mdb.InsertCell(memory.Cell{
		CellID: "cell_new", TopicID: "topic_1", SourceType: "conversation",
		SourceID: "c2", CellType: "decision", Salience: 0.9,
		Content: "Switched to JWT for auth",
	})

	if err := mdb.SupersedeCells([]string{"cell_old"}); err != nil {
		t.Fatal(err)
	}

	results, err := mdb.SearchCellsFTS("auth", "", 10)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range results {
		if r.CellID == "cell_old" {
			t.Error("superseded cell should not appear in search")
		}
	}
}

func TestUpsertAndSearchTopic(t *testing.T) {
	dir := t.TempDir()
	mdb, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	topic := memory.Topic{
		TopicID: "topic_auth",
		Name:    "Authentication",
		Summary: "Authentication uses JWT with RS256. Tokens stored in httpOnly cookies.",
	}
	if err := mdb.UpsertTopic(topic); err != nil {
		t.Fatal(err)
	}

	results, err := mdb.SearchTopicsFTS("JWT authentication", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one topic result")
	}
	if results[0].TopicID != "topic_auth" {
		t.Errorf("expected topic_auth, got %s", results[0].TopicID)
	}
}

func TestGetCellsByTopic(t *testing.T) {
	dir := t.TempDir()
	mdb, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	mdb.InsertCell(memory.Cell{
		CellID: "c1", TopicID: "t1", SourceType: "conversation",
		SourceID: "conv1", CellType: "fact", Salience: 0.5,
		Content: "Percy uses SQLite",
	})
	mdb.InsertCell(memory.Cell{
		CellID: "c2", TopicID: "t1", SourceType: "conversation",
		SourceID: "conv1", CellType: "fact", Salience: 0.7,
		Content: "Percy uses sqlc for codegen",
	})
	mdb.InsertCell(memory.Cell{
		CellID: "c3", TopicID: "t2", SourceType: "conversation",
		SourceID: "conv1", CellType: "fact", Salience: 0.6,
		Content: "UI uses React",
	})

	cells, err := mdb.GetCellsByTopic("t1", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 2 {
		t.Fatalf("expected 2 cells for topic t1, got %d", len(cells))
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./memory -run "TestInsertAndSearchCells|TestSupersedeCells|TestUpsertAndSearchTopic|TestGetCellsByTopic" -v`
Expected: FAIL — `Cell` type and methods not defined

**Step 3: Implement cell and topic operations**

Create `memory/cell.go`:

```go
package memory

import (
	"fmt"
)

// Cell is an atomic knowledge unit extracted from a conversation or file.
type Cell struct {
	CellID     string
	TopicID    string
	SourceType string
	SourceID   string
	SourceName string
	CellType   string
	Salience   float64
	Content    string
	Embedding  []byte
}

// CellResult is a search result for a cell query.
type CellResult struct {
	CellID     string
	TopicID    string
	SourceType string
	SourceID   string
	SourceName string
	CellType   string
	Salience   float64
	Content    string
	Score      float64
}

// Topic groups related cells under a theme.
type Topic struct {
	TopicID   string
	Name      string
	Summary   string
	Embedding []byte
	CellCount int
}

// TopicResult is a search result for a topic query.
type TopicResult struct {
	TopicID   string
	Name      string
	Summary   string
	Score     float64
	UpdatedAt string
}

// InsertCell inserts a cell into the cells table.
func (d *DB) InsertCell(c Cell) error {
	_, err := d.db.Exec(
		`INSERT OR REPLACE INTO cells (cell_id, topic_id, source_type, source_id, source_name, cell_type, salience, content, embedding)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.CellID, c.TopicID, c.SourceType, c.SourceID, c.SourceName, c.CellType, c.Salience, c.Content, c.Embedding,
	)
	if err != nil {
		return fmt.Errorf("memory: insert cell: %w", err)
	}
	// Update topic cell count.
	if c.TopicID != "" {
		_, _ = d.db.Exec(`UPDATE topics SET cell_count = (SELECT COUNT(*) FROM cells WHERE topic_id = ? AND NOT superseded) WHERE topic_id = ?`, c.TopicID, c.TopicID)
	}
	return nil
}

// SupersedeCells marks the given cell IDs as superseded.
func (d *DB) SupersedeCells(cellIDs []string) error {
	for _, id := range cellIDs {
		if _, err := d.db.Exec(`UPDATE cells SET superseded = TRUE WHERE cell_id = ?`, id); err != nil {
			return fmt.Errorf("memory: supersede cell %s: %w", id, err)
		}
	}
	return nil
}

// DeleteCellsBySource removes all cells for a given source.
func (d *DB) DeleteCellsBySource(sourceType, sourceID string) error {
	_, err := d.db.Exec(`DELETE FROM cells WHERE source_type = ? AND source_id = ?`, sourceType, sourceID)
	if err != nil {
		return fmt.Errorf("memory: delete cells: %w", err)
	}
	return nil
}

// GetCellsByTopic returns all cells for a topic, ordered by salience descending.
// If includeSuperseded is false, superseded cells are excluded.
func (d *DB) GetCellsByTopic(topicID string, includeSuperseded bool) ([]Cell, error) {
	q := `SELECT cell_id, topic_id, source_type, source_id, source_name, cell_type, salience, content
		FROM cells WHERE topic_id = ?`
	if !includeSuperseded {
		q += ` AND NOT superseded`
	}
	q += ` ORDER BY salience DESC`

	rows, err := d.db.Query(q, topicID)
	if err != nil {
		return nil, fmt.Errorf("memory: get cells by topic: %w", err)
	}
	defer rows.Close()

	var cells []Cell
	for rows.Next() {
		var c Cell
		if err := rows.Scan(&c.CellID, &c.TopicID, &c.SourceType, &c.SourceID, &c.SourceName, &c.CellType, &c.Salience, &c.Content); err != nil {
			return nil, fmt.Errorf("memory: scan cell: %w", err)
		}
		cells = append(cells, c)
	}
	return cells, rows.Err()
}

// UnsummarizedCellCount returns the number of non-superseded cells in a topic
// created after the topic's last summary update.
func (d *DB) UnsummarizedCellCount(topicID string) (int, error) {
	var count int
	err := d.db.QueryRow(`
		SELECT COUNT(*) FROM cells c
		JOIN topics t ON c.topic_id = t.topic_id
		WHERE c.topic_id = ? AND NOT c.superseded
		AND c.created_at > COALESCE(t.updated_at, '1970-01-01')
	`, topicID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("memory: unsummarized cell count: %w", err)
	}
	return count, nil
}

// UpsertTopic inserts or updates a topic.
func (d *DB) UpsertTopic(t Topic) error {
	_, err := d.db.Exec(`
		INSERT INTO topics (topic_id, name, summary, embedding, cell_count, updated_at)
		VALUES (?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(topic_id) DO UPDATE SET
			name = excluded.name,
			summary = excluded.summary,
			embedding = COALESCE(excluded.embedding, topics.embedding),
			cell_count = excluded.cell_count,
			updated_at = datetime('now')
	`, t.TopicID, t.Name, t.Summary, t.Embedding, t.CellCount)
	if err != nil {
		return fmt.Errorf("memory: upsert topic: %w", err)
	}
	return nil
}

// GetTopic returns a topic by ID, or nil if not found.
func (d *DB) GetTopic(topicID string) (*Topic, error) {
	var t Topic
	err := d.db.QueryRow(`SELECT topic_id, name, summary, embedding, cell_count FROM topics WHERE topic_id = ?`, topicID).
		Scan(&t.TopicID, &t.Name, &t.Summary, &t.Embedding, &t.CellCount)
	if err != nil {
		return nil, nil // not found
	}
	return &t, nil
}

// AllTopics returns all topics with their embeddings for similarity matching.
func (d *DB) AllTopics() ([]Topic, error) {
	rows, err := d.db.Query(`SELECT topic_id, name, summary, embedding, cell_count FROM topics`)
	if err != nil {
		return nil, fmt.Errorf("memory: all topics: %w", err)
	}
	defer rows.Close()

	var topics []Topic
	for rows.Next() {
		var t Topic
		var summary *string
		if err := rows.Scan(&t.TopicID, &t.Name, &summary, &t.Embedding, &t.CellCount); err != nil {
			return nil, fmt.Errorf("memory: scan topic: %w", err)
		}
		if summary != nil {
			t.Summary = *summary
		}
		topics = append(topics, t)
	}
	return topics, rows.Err()
}

// SearchCellsFTS performs full-text search on non-superseded cells.
func (d *DB) SearchCellsFTS(query, sourceType string, limit int) ([]CellResult, error) {
	q := `SELECT c.cell_id, c.topic_id, c.source_type, c.source_id, c.source_name, c.cell_type, c.salience, c.content, f.rank
		FROM cells_fts f
		JOIN cells c ON c.rowid = f.rowid
		WHERE cells_fts MATCH ? AND NOT c.superseded`

	var args []any
	args = append(args, query)
	if sourceType != "" {
		q += ` AND c.source_type = ?`
		args = append(args, sourceType)
	}
	q += ` ORDER BY f.rank LIMIT ?`
	args = append(args, limit)

	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("memory: search cells fts: %w", err)
	}
	defer rows.Close()

	var results []CellResult
	for rows.Next() {
		var r CellResult
		if err := rows.Scan(&r.CellID, &r.TopicID, &r.SourceType, &r.SourceID, &r.SourceName, &r.CellType, &r.Salience, &r.Content, &r.Score); err != nil {
			return nil, fmt.Errorf("memory: scan cell result: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchTopicsFTS performs full-text search on topic summaries.
func (d *DB) SearchTopicsFTS(query string, limit int) ([]TopicResult, error) {
	rows, err := d.db.Query(`
		SELECT t.topic_id, t.name, t.summary, f.rank, t.updated_at
		FROM topics_fts f
		JOIN topics t ON t.rowid = f.rowid
		WHERE topics_fts MATCH ?
		ORDER BY f.rank
		LIMIT ?
	`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("memory: search topics fts: %w", err)
	}
	defer rows.Close()

	var results []TopicResult
	for rows.Next() {
		var r TopicResult
		var summary, updatedAt *string
		if err := rows.Scan(&r.TopicID, &r.Name, &summary, &r.Score, &updatedAt); err != nil {
			return nil, fmt.Errorf("memory: scan topic result: %w", err)
		}
		if summary != nil {
			r.Summary = *summary
		}
		if updatedAt != nil {
			r.UpdatedAt = *updatedAt
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./memory -run "TestInsertAndSearchCells|TestSupersedeCells|TestUpsertAndSearchTopic|TestGetCellsByTopic" -v`
Expected: PASS

**Step 5: Commit**

```bash
git add memory/cell.go memory/cell_test.go
git commit -m "feat(memory): add cell and topic DB operations with FTS search"
```

---

### Task 3: LLM Cell Extraction

**Files:**
- Create: `memory/extract.go`
- Test: `memory/extract_test.go`

**Step 1: Write failing test for extraction**

Create `memory/extract_test.go`:

```go
package memory_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/tgruben-circuit/percy/llm"
	"github.com/tgruben-circuit/percy/memory"
)

// mockLLMService returns a fixed response for testing extraction.
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
	mockResponse, _ := json.Marshal([]memory.ExtractedCell{
		{CellType: "decision", Salience: 0.9, Content: "Auth uses JWT with RS256", TopicHint: "authentication"},
		{CellType: "code_ref", Salience: 0.7, Content: "server/auth.go handles JWT middleware", TopicHint: "authentication"},
	})

	svc := &mockLLMService{response: string(mockResponse)}
	messages := []memory.MessageText{
		{Role: "user", Text: "Let's use JWT for authentication"},
		{Role: "assistant", Text: "I'll implement JWT with RS256 signing in server/auth.go"},
	}

	cells, err := memory.ExtractCells(context.Background(), svc, messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 2 {
		t.Fatalf("expected 2 cells, got %d", len(cells))
	}
	if cells[0].CellType != "decision" {
		t.Errorf("expected decision, got %s", cells[0].CellType)
	}
	if cells[0].TopicHint != "authentication" {
		t.Errorf("expected authentication, got %s", cells[0].TopicHint)
	}
}

func TestExtractCellsFallbackOnBadJSON(t *testing.T) {
	svc := &mockLLMService{response: "not valid json"}
	messages := []memory.MessageText{
		{Role: "user", Text: "Hello"},
		{Role: "assistant", Text: "Hi there"},
	}

	cells, err := memory.ExtractCells(context.Background(), svc, messages)
	if err != nil {
		t.Fatal("should not error on bad JSON, should return empty")
	}
	if len(cells) != 0 {
		t.Fatalf("expected 0 cells on bad JSON, got %d", len(cells))
	}
}

func TestExtractCellsFallbackChunking(t *testing.T) {
	// When no LLM service is provided, fall back to raw chunking.
	dir := t.TempDir()
	mdb, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	messages := []memory.MessageText{
		{Role: "user", Text: "Tell me about the project"},
		{Role: "assistant", Text: "Percy is a multi-model AI coding agent with Go backend"},
	}

	cells := memory.FallbackChunkToCells("conv_123", "test-convo", messages)
	if len(cells) == 0 {
		t.Fatal("expected at least one cell from fallback chunking")
	}
	if cells[0].CellType != "fact" {
		t.Errorf("fallback cells should be type 'fact', got %s", cells[0].CellType)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./memory -run "TestExtractCells" -v`
Expected: FAIL — `ExtractCells` not defined

**Step 3: Implement extraction**

Create `memory/extract.go`:

```go
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/tgruben-circuit/percy/llm"
)

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

// ExtractedCell is the structured output from LLM extraction.
type ExtractedCell struct {
	CellType  string  `json:"cell_type"`
	Salience  float64 `json:"salience"`
	Content   string  `json:"content"`
	TopicHint string  `json:"topic_hint"`
}

var codeBlockRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

// ExtractCells uses an LLM to extract structured memory cells from a conversation.
// Returns nil, nil if the LLM returns unparseable output (graceful degradation).
func ExtractCells(ctx context.Context, svc llm.Service, messages []MessageText) ([]ExtractedCell, error) {
	transcript := formatTranscript(messages)

	resp, err := svc.Do(ctx, &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: fmt.Sprintf(extractionPrompt, transcript)},
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("memory: extraction LLM call: %w", err)
	}

	var text string
	for _, c := range resp.Content {
		if c.Type == llm.ContentTypeText {
			text += c.Text
		}
	}

	return parseExtractedCells(text), nil
}

// parseExtractedCells parses the LLM response, handling code fences and bad JSON gracefully.
func parseExtractedCells(text string) []ExtractedCell {
	// Strip code fences if present.
	if m := codeBlockRe.FindStringSubmatch(text); len(m) > 1 {
		text = m[1]
	}
	text = strings.TrimSpace(text)

	var cells []ExtractedCell
	if err := json.Unmarshal([]byte(text), &cells); err != nil {
		return nil
	}

	// Validate cell types and clamp salience.
	validTypes := map[string]bool{
		"fact": true, "decision": true, "preference": true,
		"task": true, "risk": true, "code_ref": true,
	}
	var valid []ExtractedCell
	for _, c := range cells {
		if !validTypes[c.CellType] {
			c.CellType = "fact"
		}
		if c.Salience < 0 {
			c.Salience = 0
		}
		if c.Salience > 1 {
			c.Salience = 1
		}
		if c.Content == "" {
			continue
		}
		valid = append(valid, c)
	}
	return valid
}

// formatTranscript builds a plain-text transcript from messages.
func formatTranscript(messages []MessageText) string {
	var sb strings.Builder
	for _, m := range messages {
		sb.WriteString(roleLabel(m.Role))
		sb.WriteString(": ")
		sb.WriteString(m.Text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// FallbackChunkToCells converts messages to cells using the old chunking approach.
// Used when no LLM service is available for extraction.
func FallbackChunkToCells(sourceID, sourceName string, messages []MessageText) []ExtractedCell {
	chunks := ChunkMessages(messages, 1024)
	cells := make([]ExtractedCell, len(chunks))
	for i, c := range chunks {
		cells[i] = ExtractedCell{
			CellType:  "fact",
			Salience:  0.5,
			Content:   c.Text,
			TopicHint: sourceName,
		}
	}
	return cells
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./memory -run "TestExtractCells" -v`
Expected: PASS

**Step 5: Commit**

```bash
git add memory/extract.go memory/extract_test.go
git commit -m "feat(memory): LLM-powered cell extraction with fallback chunking"
```

---

### Task 4: Topic Discovery and Assignment

**Files:**
- Create: `memory/topic.go`
- Test: `memory/topic_test.go`

**Step 1: Write failing tests**

Create `memory/topic_test.go`:

```go
package memory_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tgruben-circuit/percy/memory"
)

func TestAssignCellsToTopics(t *testing.T) {
	dir := t.TempDir()
	mdb, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Pre-create a topic.
	mdb.UpsertTopic(memory.Topic{
		TopicID:   "topic_auth",
		Name:      "authentication",
		Embedding: memory.SerializeEmbedding([]float32{1, 0, 0}),
	})

	cells := []memory.ExtractedCell{
		{CellType: "decision", Salience: 0.9, Content: "Auth uses JWT", TopicHint: "authentication"},
		{CellType: "fact", Salience: 0.6, Content: "UI uses React with TypeScript", TopicHint: "frontend"},
	}

	assigned, err := memory.AssignCellsToTopics(context.Background(), mdb, cells, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(assigned) != 2 {
		t.Fatalf("expected 2 assigned cells, got %d", len(assigned))
	}

	// First cell should match existing topic via topic_hint.
	if assigned[0].TopicID != "topic_auth" {
		t.Errorf("expected topic_auth, got %s", assigned[0].TopicID)
	}

	// Second cell should get a new topic.
	if assigned[1].TopicID == "" {
		t.Error("second cell should have a topic assigned")
	}
	if assigned[1].TopicID == "topic_auth" {
		t.Error("second cell should not be in auth topic")
	}
}

func TestAssignToTopicByEmbeddingSimilarity(t *testing.T) {
	dir := t.TempDir()
	mdb, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Create topic with an embedding.
	mdb.UpsertTopic(memory.Topic{
		TopicID:   "topic_db",
		Name:      "database",
		Embedding: memory.SerializeEmbedding([]float32{0.9, 0.1, 0}),
	})

	// Use a mock embedder that returns a vector similar to the "database" topic.
	embedder := &fixedEmbedder{vec: []float32{0.85, 0.15, 0}}

	cells := []memory.ExtractedCell{
		{CellType: "fact", Salience: 0.7, Content: "Schema uses sqlc codegen", TopicHint: "data layer"},
	}

	assigned, err := memory.AssignCellsToTopics(context.Background(), mdb, cells, embedder)
	if err != nil {
		t.Fatal(err)
	}
	if assigned[0].TopicID != "topic_db" {
		t.Errorf("expected topic_db via embedding similarity, got %s", assigned[0].TopicID)
	}
}

type fixedEmbedder struct {
	vec []float32
}

func (f *fixedEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vecs := make([][]float32, len(texts))
	for i := range texts {
		vecs[i] = f.vec
	}
	return vecs, nil
}
func (f *fixedEmbedder) Dimension() int { return len(f.vec) }
```

**Step 2: Run tests to verify they fail**

Run: `go test ./memory -run "TestAssignCellsToTopics|TestAssignToTopicByEmbedding" -v`
Expected: FAIL — `AssignCellsToTopics` not defined

**Step 3: Implement topic assignment**

Create `memory/topic.go`:

```go
package memory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
)

const topicSimilarityThreshold = 0.7

// AssignedCell is an ExtractedCell with a resolved topic ID.
type AssignedCell struct {
	ExtractedCell
	TopicID string
}

// AssignCellsToTopics assigns each extracted cell to a topic.
// It first tries to match by topic name (from topic_hint), then by embedding similarity.
// If no match is found, a new topic is created.
func AssignCellsToTopics(ctx context.Context, db *DB, cells []ExtractedCell, embedder Embedder) ([]AssignedCell, error) {
	existingTopics, err := db.AllTopics()
	if err != nil {
		return nil, err
	}

	// Build a name index for fast lookup.
	nameIndex := make(map[string]string) // normalized name -> topic_id
	for _, t := range existingTopics {
		nameIndex[normalizeName(t.Name)] = t.TopicID
	}

	// Batch-embed cell contents if embedder is available.
	var cellEmbeddings [][]float32
	if embedder != nil {
		texts := make([]string, len(cells))
		for i, c := range cells {
			texts[i] = c.Content
		}
		cellEmbeddings, _ = embedder.Embed(ctx, texts)
	}

	assigned := make([]AssignedCell, len(cells))
	for i, cell := range cells {
		assigned[i].ExtractedCell = cell
		hint := normalizeName(cell.TopicHint)

		// Try 1: exact name match on topic_hint.
		if topicID, ok := nameIndex[hint]; ok {
			assigned[i].TopicID = topicID
			continue
		}

		// Try 2: embedding similarity against existing topics.
		if i < len(cellEmbeddings) && cellEmbeddings[i] != nil {
			bestID, bestSim := findBestTopic(cellEmbeddings[i], existingTopics)
			if bestSim >= topicSimilarityThreshold {
				assigned[i].TopicID = bestID
				continue
			}
		}

		// Try 3: create a new topic.
		topicID := generateTopicID(cell.TopicHint)
		newTopic := Topic{
			TopicID: topicID,
			Name:    cell.TopicHint,
		}
		if i < len(cellEmbeddings) && cellEmbeddings[i] != nil {
			newTopic.Embedding = SerializeEmbedding(cellEmbeddings[i])
		}
		if err := db.UpsertTopic(newTopic); err != nil {
			return nil, fmt.Errorf("memory: create topic: %w", err)
		}
		existingTopics = append(existingTopics, newTopic)
		nameIndex[hint] = topicID
		assigned[i].TopicID = topicID
	}

	return assigned, nil
}

// findBestTopic returns the topic with highest cosine similarity to the query vector.
func findBestTopic(queryVec []float32, topics []Topic) (string, float32) {
	var bestID string
	var bestSim float32
	for _, t := range topics {
		if t.Embedding == nil {
			continue
		}
		topicVec := DeserializeEmbedding(t.Embedding)
		sim := CosineSimilarity(queryVec, topicVec)
		if sim > bestSim {
			bestSim = sim
			bestID = t.TopicID
		}
	}
	return bestID, bestSim
}

func normalizeName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func generateTopicID(hint string) string {
	h := sha256.Sum256([]byte(normalizeName(hint)))
	return fmt.Sprintf("topic_%x", h[:8])
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./memory -run "TestAssignCellsToTopics|TestAssignToTopicByEmbedding" -v`
Expected: PASS

**Step 5: Commit**

```bash
git add memory/topic.go memory/topic_test.go
git commit -m "feat(memory): topic discovery and cell assignment"
```

---

### Task 5: Topic Consolidation

**Files:**
- Create: `memory/consolidate.go`
- Test: `memory/consolidate_test.go`

**Step 1: Write failing tests**

Create `memory/consolidate_test.go`:

```go
package memory_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/tgruben-circuit/percy/llm"
	"github.com/tgruben-circuit/percy/memory"
)

func TestConsolidateTopic(t *testing.T) {
	dir := t.TempDir()
	mdb, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Create topic and cells.
	mdb.UpsertTopic(memory.Topic{TopicID: "topic_auth", Name: "authentication"})
	for i := 0; i < 6; i++ {
		mdb.InsertCell(memory.Cell{
			CellID: fmt.Sprintf("cell_%d", i), TopicID: "topic_auth",
			SourceType: "conversation", SourceID: "conv_1",
			CellType: "fact", Salience: 0.5 + float64(i)*0.05,
			Content: fmt.Sprintf("Auth fact %d", i),
		})
	}

	consolidationResp, _ := json.Marshal(memory.ConsolidationResult{
		Summary:          "Authentication uses JWT with RS256. Tokens in httpOnly cookies.",
		SupersededCellIDs: []string{"cell_0", "cell_1"},
	})
	svc := &mockLLMService{response: string(consolidationResp)}

	err = memory.ConsolidateTopic(context.Background(), mdb, svc, nil, "topic_auth")
	if err != nil {
		t.Fatal(err)
	}

	// Verify topic summary was updated.
	topic, err := mdb.GetTopic("topic_auth")
	if err != nil || topic == nil {
		t.Fatal("topic should exist")
	}
	if topic.Summary != "Authentication uses JWT with RS256. Tokens in httpOnly cookies." {
		t.Errorf("unexpected summary: %s", topic.Summary)
	}

	// Verify superseded cells.
	cells, err := mdb.GetCellsByTopic("topic_auth", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cells {
		if c.CellID == "cell_0" || c.CellID == "cell_1" {
			t.Errorf("cell %s should be superseded", c.CellID)
		}
	}
}

func TestConsolidateSkipsSmallTopics(t *testing.T) {
	dir := t.TempDir()
	mdb, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	mdb.UpsertTopic(memory.Topic{TopicID: "topic_small", Name: "tiny topic"})
	mdb.InsertCell(memory.Cell{
		CellID: "c1", TopicID: "topic_small", SourceType: "conversation",
		SourceID: "conv_1", CellType: "fact", Salience: 0.5, Content: "one fact",
	})

	needsConsolidation, err := memory.NeedsConsolidation(mdb, "topic_small")
	if err != nil {
		t.Fatal(err)
	}
	if needsConsolidation {
		t.Error("topic with 1 cell should not need consolidation")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./memory -run "TestConsolidate" -v`
Expected: FAIL — `ConsolidateTopic` not defined

**Step 3: Implement consolidation**

Create `memory/consolidate.go`:

```go
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tgruben-circuit/percy/llm"
)

const (
	consolidationThreshold = 5
	consolidationPrompt    = `You are a memory consolidation engine for Percy, an AI coding assistant. Given a topic and its memory cells, produce an updated summary.

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
)

// ConsolidationResult is the structured output from LLM consolidation.
type ConsolidationResult struct {
	Summary           string   `json:"summary"`
	SupersededCellIDs []string `json:"superseded_cell_ids"`
}

// NeedsConsolidation returns true if a topic has accumulated enough
// unsummarized cells to warrant consolidation.
func NeedsConsolidation(db *DB, topicID string) (bool, error) {
	count, err := db.UnsummarizedCellCount(topicID)
	if err != nil {
		return false, err
	}
	return count >= consolidationThreshold, nil
}

// ConsolidateTopic uses an LLM to consolidate a topic's cells into a summary.
// It updates the topic summary and marks superseded cells.
func ConsolidateTopic(ctx context.Context, db *DB, svc llm.Service, embedder Embedder, topicID string) error {
	topic, err := db.GetTopic(topicID)
	if err != nil || topic == nil {
		return fmt.Errorf("memory: topic %s not found", topicID)
	}

	cells, err := db.GetCellsByTopic(topicID, false)
	if err != nil {
		return err
	}
	if len(cells) == 0 {
		return nil
	}

	existingSummary := topic.Summary
	if existingSummary == "" {
		existingSummary = "None"
	}

	cellsJSON := formatCellsForPrompt(cells)
	prompt := fmt.Sprintf(consolidationPrompt, topic.Name, existingSummary, cellsJSON)

	resp, err := svc.Do(ctx, &llm.Request{
		Messages: []llm.Message{
			{
				Role:    llm.MessageRoleUser,
				Content: []llm.Content{{Type: llm.ContentTypeText, Text: prompt}},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("memory: consolidation LLM call: %w", err)
	}

	var text string
	for _, c := range resp.Content {
		if c.Type == llm.ContentTypeText {
			text += c.Text
		}
	}

	result := parseConsolidationResult(text)
	if result.Summary == "" {
		return fmt.Errorf("memory: consolidation returned empty summary")
	}

	// Update topic summary.
	updatedTopic := *topic
	updatedTopic.Summary = result.Summary
	if embedder != nil {
		vecs, err := embedder.Embed(ctx, []string{result.Summary})
		if err == nil && len(vecs) > 0 {
			updatedTopic.Embedding = SerializeEmbedding(vecs[0])
		}
	}
	updatedTopic.CellCount = len(cells) - len(result.SupersededCellIDs)
	if err := db.UpsertTopic(updatedTopic); err != nil {
		return err
	}

	// Mark superseded cells.
	if len(result.SupersededCellIDs) > 0 {
		if err := db.SupersedeCells(result.SupersededCellIDs); err != nil {
			return err
		}
	}

	return nil
}

func formatCellsForPrompt(cells []Cell) string {
	var sb strings.Builder
	for _, c := range cells {
		sb.WriteString(fmt.Sprintf(`{"cell_id": %q, "type": %q, "salience": %.1f, "content": %q}`, c.CellID, c.CellType, c.Salience, c.Content))
		sb.WriteByte('\n')
	}
	return sb.String()
}

func parseConsolidationResult(text string) ConsolidationResult {
	text = strings.TrimSpace(text)
	if m := codeBlockRe.FindStringSubmatch(text); len(m) > 1 {
		text = m[1]
	}

	var result ConsolidationResult
	json.Unmarshal([]byte(text), &result)
	return result
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./memory -run "TestConsolidate" -v`
Expected: PASS

Note: `TestConsolidateTopic` test needs `fmt` import. Update test file to include `"fmt"` in imports.

**Step 5: Commit**

```bash
git add memory/consolidate.go memory/consolidate_test.go
git commit -m "feat(memory): topic consolidation with LLM summarization"
```

---

### Task 6: Two-Tier Search

**Files:**
- Modify: `memory/search.go`
- Test: `memory/search_test.go` (update existing tests)

**Step 1: Write failing test for two-tier search**

Add to `memory/search_test.go`:

```go
func TestTwoTierSearch(t *testing.T) {
	dir := t.TempDir()
	mdb, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Create topic with summary.
	mdb.UpsertTopic(memory.Topic{
		TopicID: "topic_auth", Name: "authentication",
		Summary: "Authentication uses JWT with RS256 signing. Tokens in httpOnly cookies.",
	})

	// Create cells.
	mdb.InsertCell(memory.Cell{
		CellID: "c1", TopicID: "topic_auth", SourceType: "conversation",
		SourceID: "conv1", CellType: "decision", Salience: 0.9,
		Content: "Switched from bcrypt to argon2id for password hashing",
	})
	mdb.InsertCell(memory.Cell{
		CellID: "c2", TopicID: "topic_auth", SourceType: "conversation",
		SourceID: "conv1", CellType: "code_ref", Salience: 0.7,
		Content: "server/auth.go handles JWT validation middleware",
	})

	results, err := mdb.TwoTierSearch("JWT authentication", nil, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}

	// First result should be the topic summary.
	found := false
	for _, r := range results {
		if r.ResultType == "topic_summary" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected at least one topic_summary result")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./memory -run TestTwoTierSearch -v`
Expected: FAIL — `TwoTierSearch` not defined

**Step 3: Implement two-tier search**

Replace the content of `memory/search.go` with the new implementation. Keep the existing `SearchResult`, `normalizeScores`, `mergeResults`, `CosineSimilarity`, `SerializeEmbedding`, `DeserializeEmbedding` functions and types. Add new types and the two-tier search.

Add to `memory/search.go` (keeping existing code and adding):

```go
// MemoryResult is a unified search result that can be either a topic summary or a cell.
type MemoryResult struct {
	ResultType string  // "topic_summary" or "cell"
	TopicID    string
	TopicName  string
	CellID     string
	CellType   string
	SourceType string
	SourceID   string
	SourceName string
	Salience   float64
	Content    string // summary text for topics, cell content for cells
	Score      float64
	UpdatedAt  string
}

// TwoTierSearch performs a two-tier search: topic summaries first, then individual cells.
// queryVec can be nil for FTS-only search.
func (d *DB) TwoTierSearch(query string, queryVec []float32, sourceType string, limit int) ([]MemoryResult, error) {
	topicLimit := 3
	cellLimit := limit - topicLimit
	if cellLimit < 1 {
		cellLimit = 1
	}

	var results []MemoryResult

	// Tier 1: Topic summaries via FTS.
	topicResults, err := d.SearchTopicsFTS(query, topicLimit)
	if err == nil {
		for _, tr := range topicResults {
			results = append(results, MemoryResult{
				ResultType: "topic_summary",
				TopicID:    tr.TopicID,
				TopicName:  tr.Name,
				Content:    tr.Summary,
				Score:      tr.Score,
				UpdatedAt:  tr.UpdatedAt,
			})
		}
	}

	// Tier 2: Individual cells via FTS (with optional vector boost).
	cellResults, err := d.SearchCellsFTS(query, sourceType, cellLimit)
	if err == nil {
		for _, cr := range cellResults {
			results = append(results, MemoryResult{
				ResultType: "cell",
				CellID:     cr.CellID,
				TopicID:    cr.TopicID,
				CellType:   cr.CellType,
				SourceType: cr.SourceType,
				SourceID:   cr.SourceID,
				SourceName: cr.SourceName,
				Salience:   cr.Salience,
				Content:    cr.Content,
				Score:      cr.Score,
			})
		}
	}

	// TODO: Add vector search tier for cells when queryVec is provided.
	// For now, FTS-only on the new schema. Vector search can be added
	// once the basic pipeline is working.

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./memory -run TestTwoTierSearch -v`
Expected: PASS

**Step 5: Commit**

```bash
git add memory/search.go memory/search_test.go
git commit -m "feat(memory): two-tier search with topic summaries and cells"
```

---

### Task 7: Updated Indexing Pipeline

**Files:**
- Modify: `memory/index.go`
- Test: `memory/index_test.go`

**Step 1: Write failing test for new indexing pipeline**

Update `memory/index_test.go`:

```go
func TestIndexConversationWithExtraction(t *testing.T) {
	dir := t.TempDir()
	mdb, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	mockResp, _ := json.Marshal([]memory.ExtractedCell{
		{CellType: "decision", Salience: 0.9, Content: "Auth uses JWT", TopicHint: "authentication"},
		{CellType: "code_ref", Salience: 0.7, Content: "server/auth.go handles JWT", TopicHint: "authentication"},
	})
	svc := &mockLLMService{response: string(mockResp)}

	messages := []memory.MessageText{
		{Role: "user", Text: "Let's use JWT"},
		{Role: "assistant", Text: "I'll implement JWT in server/auth.go"},
	}

	err = mdb.IndexConversationV2(context.Background(), "conv_123", "auth-chat", messages, nil, svc)
	if err != nil {
		t.Fatal(err)
	}

	// Should be searchable.
	results, err := mdb.TwoTierSearch("JWT", nil, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results after indexing")
	}

	// Topic should have been created.
	topics, err := mdb.AllTopics()
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) == 0 {
		t.Fatal("expected at least one topic")
	}
}

func TestIndexConversationFallbackWithoutLLM(t *testing.T) {
	dir := t.TempDir()
	mdb, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	messages := []memory.MessageText{
		{Role: "user", Text: "Tell me about the project"},
		{Role: "assistant", Text: "Percy is a multi-model AI coding agent"},
	}

	// No LLM service — should fall back to chunking.
	err = mdb.IndexConversationV2(context.Background(), "conv_456", "project-intro", messages, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	results, err := mdb.TwoTierSearch("Percy coding agent", nil, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results from fallback indexing")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./memory -run "TestIndexConversationWithExtraction|TestIndexConversationFallbackWithoutLLM" -v`
Expected: FAIL — `IndexConversationV2` not defined

**Step 3: Implement new indexing pipeline**

Update `memory/index.go` — add `IndexConversationV2` alongside the existing `IndexConversation` (keep old one for backward compat during migration):

```go
// IndexConversationV2 indexes a conversation using LLM-powered extraction.
// Falls back to chunk-based indexing if svc is nil.
func (d *DB) IndexConversationV2(ctx context.Context, conversationID, slug string, messages []MessageText, embedder Embedder, svc llm.Service) error {
	hash := hashMessages(messages)

	indexed, err := d.IsIndexed("conversation", conversationID, hash)
	if err != nil {
		return err
	}
	if indexed {
		return nil
	}

	// Extract cells (LLM or fallback).
	var extracted []ExtractedCell
	if svc != nil {
		extracted, err = ExtractCells(ctx, svc, messages)
		if err != nil || len(extracted) == 0 {
			// Graceful degradation to fallback.
			extracted = FallbackChunkToCells(conversationID, slug, messages)
		}
	} else {
		extracted = FallbackChunkToCells(conversationID, slug, messages)
	}

	if len(extracted) == 0 {
		return d.SetIndexState("conversation", conversationID, hash)
	}

	// Delete old cells for this conversation.
	if err := d.DeleteCellsBySource("conversation", conversationID); err != nil {
		return err
	}

	// Assign cells to topics.
	assigned, err := AssignCellsToTopics(ctx, d, extracted, embedder)
	if err != nil {
		return err
	}

	// Insert cells.
	for i, ac := range assigned {
		cellID := fmt.Sprintf("conv_%s_%d", conversationID, i)
		var embBlob []byte
		if embedder != nil {
			vecs, _ := embedder.Embed(ctx, []string{ac.Content})
			if len(vecs) > 0 {
				embBlob = SerializeEmbedding(vecs[0])
			}
		}
		cell := Cell{
			CellID:     cellID,
			TopicID:    ac.TopicID,
			SourceType: "conversation",
			SourceID:   conversationID,
			SourceName: slug,
			CellType:   ac.CellType,
			Salience:   ac.Salience,
			Content:    ac.Content,
			Embedding:  embBlob,
		}
		if err := d.InsertCell(cell); err != nil {
			return err
		}
	}

	// Check if any affected topics need consolidation.
	topicsSeen := make(map[string]bool)
	for _, ac := range assigned {
		topicsSeen[ac.TopicID] = true
	}
	if svc != nil {
		for topicID := range topicsSeen {
			needs, _ := NeedsConsolidation(d, topicID)
			if needs {
				// Best-effort consolidation — don't fail the whole index.
				_ = ConsolidateTopic(ctx, d, svc, embedder, topicID)
			}
		}
	}

	return d.SetIndexState("conversation", conversationID, hash)
}
```

Add `"github.com/tgruben-circuit/percy/llm"` to the imports in `index.go`.

**Step 4: Run tests to verify they pass**

Run: `go test ./memory -run "TestIndexConversationWithExtraction|TestIndexConversationFallbackWithoutLLM" -v`
Expected: PASS

**Step 5: Commit**

```bash
git add memory/index.go memory/index_test.go
git commit -m "feat(memory): v2 indexing pipeline with extraction and topic assignment"
```

---

### Task 8: Updated Memory Search Tool

**Files:**
- Modify: `claudetool/memory/tool.go`
- Test: Add test for new output format

**Step 1: Write failing test**

Create `claudetool/memory/tool_test.go` if it doesn't exist, or add:

```go
func TestMemorySearchToolUsesNewSchema(t *testing.T) {
	dir := t.TempDir()
	mdb, err := memdb.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Insert topic with summary.
	mdb.UpsertTopic(memdb.Topic{
		TopicID: "t1", Name: "authentication",
		Summary: "Auth uses JWT with RS256.",
	})
	mdb.InsertCell(memdb.Cell{
		CellID: "c1", TopicID: "t1", SourceType: "conversation",
		SourceID: "conv1", CellType: "decision", Salience: 0.9,
		Content: "Chose JWT over OAuth for simplicity",
	})

	tool := memory.NewMemorySearchTool(mdb, memdb.NoneEmbedder{})
	input, _ := json.Marshal(map[string]string{"query": "JWT authentication"})
	result := tool.Run(context.Background(), input)

	text := result.LLMContent.(llm.TextContent)
	if !strings.Contains(string(text), "Topic Summary") {
		t.Error("expected output to contain 'Topic Summary' section")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./claudetool/memory -run TestMemorySearchToolUsesNewSchema -v`
Expected: FAIL — output doesn't contain "Topic Summary"

**Step 3: Update tool to use two-tier search**

Update `claudetool/memory/tool.go`:

- Change `Run()` to call `t.db.TwoTierSearch()` instead of `t.db.HybridSearch()`
- Update `formatResults` to format `MemoryResult` types with proper labels
- Add `detail_level` parameter to input schema

Key changes to `tool.go`:

```go
// Updated input schema with detail_level.
const memorySearchInputSchema = `{
  "type": "object",
  "required": ["query"],
  "properties": {
    "query": {
      "type": "string",
      "description": "The search query to find relevant memories"
    },
    "source_type": {
      "type": "string",
      "enum": ["conversation", "file", "all"],
      "description": "Filter results by source type. Defaults to all."
    },
    "detail_level": {
      "type": "string",
      "enum": ["summary", "full"],
      "description": "summary: topic summaries only. full: summaries + individual cells. Defaults to full."
    },
    "limit": {
      "type": "integer",
      "description": "Maximum number of results to return (default 10, max 25)"
    }
  }
}`

// Updated searchInput.
type searchInput struct {
	Query       string `json:"query"`
	SourceType  string `json:"source_type"`
	DetailLevel string `json:"detail_level"`
	Limit       int    `json:"limit"`
}

// Updated Run to use TwoTierSearch.
func (t *MemorySearchTool) Run(ctx context.Context, input json.RawMessage) llm.ToolOut {
	// ... parse input same as before ...

	results, err := t.db.TwoTierSearch(in.Query, queryVec, sourceType, in.Limit)
	// ... handle errors ...

	// Filter by detail level.
	if in.DetailLevel == "summary" {
		var summaryOnly []memdb.MemoryResult
		for _, r := range results {
			if r.ResultType == "topic_summary" {
				summaryOnly = append(summaryOnly, r)
			}
		}
		results = summaryOnly
	}

	return llm.ToolOut{
		LLMContent: llm.TextContent(formatMemoryResults(results)),
		Display:    results,
	}
}

// formatMemoryResults formats results with type labels.
func formatMemoryResults(results []memdb.MemoryResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d relevant memories:\n\n", len(results))
	for i, r := range results {
		switch r.ResultType {
		case "topic_summary":
			fmt.Fprintf(&b, "--- Topic Summary: %q (updated %s) ---\n%s\n\n", r.TopicName, r.UpdatedAt, r.Content)
		case "cell":
			fmt.Fprintf(&b, "--- Result %d [%s] (score: %.2f, salience: %.1f) ---\n%s\n\n", i+1, r.CellType, r.Score, r.Salience, r.Content)
		}
	}
	return b.String()
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./claudetool/memory -run TestMemorySearchToolUsesNewSchema -v`
Expected: PASS

**Step 5: Commit**

```bash
git add claudetool/memory/tool.go claudetool/memory/tool_test.go
git commit -m "feat(memory): update search tool for two-tier results with detail_level"
```

---

### Task 9: Server Integration — Pass LLM Service to Indexer

**Files:**
- Modify: `server/server.go` (the `indexConversation` method)

**Step 1: Write failing test**

Add to `server/server_test.go` (or the appropriate test file):

```go
func TestIndexConversationUsesLLMExtraction(t *testing.T) {
	// This is an integration concern — verify the indexConversation method
	// calls IndexConversationV2 with the LLM service.
	// The actual test is that the server compiles with the new signature.
	// Unit tests for extraction live in memory/ package.
}
```

This task is primarily a wiring change — the real testing happens in the memory package tests.

**Step 2: Update indexConversation to use V2**

In `server/server.go`, update the `indexConversation` method (~line 311):

Change the final call from:
```go
if err := s.memoryDB.IndexConversation(ctx, conversationID, slug, messages, s.embedder); err != nil {
```

To:
```go
// Get an LLM service for extraction. Use the conversation's model if available,
// otherwise use the server's default model.
var llmSvc llm.Service
modelID := s.defaultModel
if conv.Model != nil && *conv.Model != "" {
	modelID = *conv.Model
}
if modelID != "" {
	llmSvc, _ = s.llmManager.GetService(modelID) // best-effort
}

if err := s.memoryDB.IndexConversationV2(ctx, conversationID, slug, messages, s.embedder, llmSvc); err != nil {
```

Also increase the timeout from 30s to 60s since extraction involves an LLM call:
```go
ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
```

**Step 3: Verify it compiles and existing tests pass**

Run: `go build ./... && go test ./server -v -count=1`
Expected: PASS (compilation and existing tests)

**Step 4: Commit**

```bash
git add server/server.go
git commit -m "feat(memory): wire LLM service into post-conversation indexing"
```

---

### Task 10: Schema Migration

**Files:**
- Modify: `memory/db.go`
- Test: `memory/db_test.go`

**Step 1: Write failing test for migration**

Add to `memory/db_test.go`:

```go
func TestMigrateFromOldSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "memory.db")

	// Create old-schema database manually.
	sqldb, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	sqldb.Exec(`CREATE TABLE chunks (
		chunk_id TEXT PRIMARY KEY, source_type TEXT, source_id TEXT,
		source_name TEXT, chunk_index INTEGER, text TEXT, token_count INTEGER,
		embedding BLOB, created_at DATETIME, updated_at DATETIME
	)`)
	sqldb.Exec(`CREATE TABLE index_state (
		source_type TEXT, source_id TEXT, indexed_at DATETIME, hash TEXT,
		PRIMARY KEY (source_type, source_id)
	)`)
	sqldb.Exec(`INSERT INTO index_state VALUES('conversation', 'conv_1', datetime('now'), 'abc')`)
	sqldb.Close()

	// Open with new code — should migrate.
	mdb, err := memory.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Old chunks table should be gone.
	var count int
	err = mdb.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='chunks'").Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("chunks table should have been dropped during migration")
	}

	// New cells table should exist.
	err = mdb.QueryRow("SELECT COUNT(*) FROM cells").Scan(&count)
	if err != nil {
		t.Fatalf("cells table should exist: %v", err)
	}

	// index_state should be cleared (forces re-indexing).
	err = mdb.QueryRow("SELECT COUNT(*) FROM index_state").Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("index_state should be cleared during migration")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./memory -run TestMigrateFromOldSchema -v`
Expected: FAIL — migration not implemented

**Step 3: Implement migration in db.go**

Update `memory/db.go` `Open()` function to detect and migrate:

```go
func Open(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("memory: create dir: %w", err)
		}
	}

	dsn := path + "?_journal_mode=WAL"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("memory: open: %w", err)
	}
	sqldb.SetMaxOpenConns(1)

	// Check if migration from old schema is needed.
	if needsMigration(sqldb) {
		if err := migrateFromOldSchema(sqldb); err != nil {
			sqldb.Close()
			return nil, fmt.Errorf("memory: migrate: %w", err)
		}
	}

	if _, err := sqldb.Exec(schemaSQL); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("memory: migrate: %w", err)
	}

	return &DB{db: sqldb, path: path}, nil
}

// needsMigration returns true if the old schema (chunks table, no cells table) is detected.
func needsMigration(db *sql.DB) bool {
	var hasChunks, hasCells int
	db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='chunks'").Scan(&hasChunks)
	db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='cells'").Scan(&hasCells)
	return hasChunks > 0 && hasCells == 0
}

// migrateFromOldSchema drops old tables and clears index state.
func migrateFromOldSchema(db *sql.DB) error {
	stmts := []string{
		"DROP TABLE IF EXISTS chunks_fts",
		"DROP TABLE IF EXISTS chunks",
		"DELETE FROM index_state",
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("migration: %s: %w", stmt, err)
		}
	}
	return nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./memory -run TestMigrateFromOldSchema -v`
Expected: PASS

**Step 5: Run full test suite**

Run: `go test ./memory/... -v`
Expected: All PASS (including existing tests — existing `IndexConversation` still works for files)

**Step 6: Commit**

```bash
git add memory/db.go memory/db_test.go
git commit -m "feat(memory): auto-migrate from old chunks schema to cells+topics"
```

---

### Task 11: Clean Up Old Code

**Files:**
- Modify: `memory/search.go` — remove old `SearchResult`, `InsertChunk`, `DeleteChunksBySource`, `SearchFTS`, `SearchVector`, `HybridSearch` that reference the old `chunks` table
- Modify: `memory/index.go` — remove old `IndexConversation` (replace calls with V2), remove `ChunkMessages` calls from main path
- Modify: `memory/chunk.go` — keep `ChunkMessages` and `ChunkMarkdown` (used by fallback and file indexing)

**Step 1: Identify all callers of old functions**

Run: `grep -rn "InsertChunk\|SearchFTS\b\|SearchVector\|HybridSearch\|IndexConversation[^V]" --include="*.go"`

**Step 2: Update all callers to use new APIs**

- `server/server.go`: Already updated in Task 9 to use `IndexConversationV2`
- `claudetool/memory/tool.go`: Already updated in Task 8 to use `TwoTierSearch`
- Rename `IndexConversationV2` to `IndexConversation` (old one removed)

**Step 3: Remove dead code**

Remove from `memory/search.go`:
- `SearchResult` struct
- `InsertChunk`
- `DeleteChunksBySource`
- `SearchFTS` (for old chunks table)
- `SearchVector` (for old chunks table)
- `HybridSearch` (for old chunks table)
- `mergeResults` and `normalizeScores` (if no longer used)

**Step 4: Verify all tests pass**

Run: `go test ./... -count=1`
Expected: All PASS

**Step 5: Commit**

```bash
git add -A
git commit -m "refactor(memory): remove old chunk-based search code"
```

---

### Task 12: Integration Test

**Files:**
- Modify: `memory/integration_test.go`

**Step 1: Write end-to-end integration test**

Replace or extend `memory/integration_test.go`:

```go
func TestFullMemoryPipeline(t *testing.T) {
	dir := t.TempDir()
	mdb, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Mock LLM that returns extraction results.
	extractResp, _ := json.Marshal([]memory.ExtractedCell{
		{CellType: "decision", Salience: 0.9, Content: "Auth uses JWT with RS256", TopicHint: "authentication"},
		{CellType: "code_ref", Salience: 0.7, Content: "server/auth.go handles JWT middleware", TopicHint: "authentication"},
		{CellType: "fact", Salience: 0.6, Content: "UI built with React and TypeScript", TopicHint: "frontend"},
	})
	svc := &mockLLMService{response: string(extractResp)}

	// Index a conversation.
	messages := []memory.MessageText{
		{Role: "user", Text: "Implement JWT auth"},
		{Role: "assistant", Text: "Done — JWT with RS256 in server/auth.go. UI updated."},
	}
	err = mdb.IndexConversation(context.Background(), "conv_1", "auth-impl", messages, nil, svc)
	if err != nil {
		t.Fatal(err)
	}

	// Search should find results.
	results, err := mdb.TwoTierSearch("JWT authentication", nil, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results")
	}

	// Verify topics were created.
	topics, err := mdb.AllTopics()
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) < 2 {
		t.Errorf("expected at least 2 topics, got %d", len(topics))
	}

	// Re-indexing with same content should be a no-op.
	err = mdb.IndexConversation(context.Background(), "conv_1", "auth-impl", messages, nil, svc)
	if err != nil {
		t.Fatal(err)
	}
}
```

**Step 2: Run integration test**

Run: `go test ./memory -run TestFullMemoryPipeline -v`
Expected: PASS

**Step 3: Run full test suite**

Run: `make test-go`
Expected: All PASS

**Step 4: Commit**

```bash
git add memory/integration_test.go
git commit -m "test(memory): add end-to-end integration test for topic-consolidated pipeline"
```

---

### Summary of Tasks

| Task | Description | Key Files |
|------|-------------|-----------|
| 1 | New schema | `memory/schema.sql`, `memory/db.go` |
| 2 | Cell and topic DB operations | `memory/cell.go` |
| 3 | LLM cell extraction | `memory/extract.go` |
| 4 | Topic discovery and assignment | `memory/topic.go` |
| 5 | Topic consolidation | `memory/consolidate.go` |
| 6 | Two-tier search | `memory/search.go` |
| 7 | Updated indexing pipeline | `memory/index.go` |
| 8 | Updated memory search tool | `claudetool/memory/tool.go` |
| 9 | Server integration | `server/server.go` |
| 10 | Schema migration | `memory/db.go` |
| 11 | Clean up old code | Multiple files |
| 12 | Integration test | `memory/integration_test.go` |
