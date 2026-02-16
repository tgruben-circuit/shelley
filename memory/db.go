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

type DB struct {
	db   *sql.DB
	path string
}

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
		return nil, fmt.Errorf("memory: schema: %w", err)
	}

	return &DB{db: sqldb, path: path}, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) QueryRow(query string, args ...any) *sql.Row {
	return d.db.QueryRow(query, args...)
}

func MemoryDBPath(percyDBPath string) string {
	dir := filepath.Dir(percyDBPath)
	return filepath.Join(dir, "memory.db")
}

// needsMigration returns true if the old schema (chunks table, no cells table) is detected.
func needsMigration(db *sql.DB) bool {
	var hasChunks, hasCells int
	db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='chunks'").Scan(&hasChunks)
	db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='cells'").Scan(&hasCells)
	return hasChunks > 0 && hasCells == 0
}

// migrateFromOldSchema drops old tables and clears index state to force re-indexing.
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
