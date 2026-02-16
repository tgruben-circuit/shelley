package memory

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestNewSchemaCreatesCellsAndTopics(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "memory.db")
	mdb, err := Open(dbPath)
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

func TestOpenCreatesDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "memory.db")
	mdb, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database file not created: %v", err)
	}

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

func TestMigrateFromOldSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "memory.db")

	// Create old-schema database manually.
	sqldb, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	// Create old tables (just chunks and index_state, no cells/topics).
	_, err = sqldb.Exec(`CREATE TABLE chunks (
		chunk_id TEXT PRIMARY KEY, source_type TEXT NOT NULL, source_id TEXT NOT NULL,
		source_name TEXT, chunk_index INTEGER NOT NULL, text TEXT NOT NULL, token_count INTEGER,
		embedding BLOB, created_at DATETIME, updated_at DATETIME
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sqldb.Exec(`CREATE TABLE index_state (
		source_type TEXT NOT NULL, source_id TEXT NOT NULL, indexed_at DATETIME NOT NULL, hash TEXT,
		PRIMARY KEY (source_type, source_id)
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sqldb.Exec(`INSERT INTO chunks (chunk_id, source_type, source_id, chunk_index, text)
		VALUES ('c1', 'conversation', 'conv_1', 0, 'old data')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sqldb.Exec(`INSERT INTO index_state VALUES('conversation', 'conv_1', datetime('now'), 'abc')`)
	if err != nil {
		t.Fatal(err)
	}
	sqldb.Close()

	// Open with new code — should detect old schema and migrate.
	mdb, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	// Old chunks data should be gone (schemaSQL recreates chunks table empty,
	// which will be removed in a later cleanup task).
	var count int
	err = mdb.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("chunks table should be empty after migration")
	}

	// New cells table should exist.
	err = mdb.QueryRow("SELECT COUNT(*) FROM cells").Scan(&count)
	if err != nil {
		t.Fatalf("cells table should exist: %v", err)
	}

	// New topics table should exist.
	err = mdb.QueryRow("SELECT COUNT(*) FROM topics").Scan(&count)
	if err != nil {
		t.Fatalf("topics table should exist: %v", err)
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
