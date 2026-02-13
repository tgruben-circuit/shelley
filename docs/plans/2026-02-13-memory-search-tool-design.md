# Memory Search Tool Design

**Date**: 2026-02-13
**Status**: Approved

## Overview

Add a dedicated `memory_search` tool to Shelley that lets the agent search past conversation transcripts and workspace files using hybrid FTS5 + vector search. Replaces the current approach of shelling out to `sqlite3` with raw SQL.

## Approach

Separate `memory.db` database alongside `shelley.db`. Clean separation — memory is a derived index, not source-of-truth data. Can be rebuilt without touching conversation data.

## 1. Memory Database Schema

`memory.db` lives alongside `shelley.db` (same directory, derived path). Three tables:

### `chunks` — core unit of indexed content

```sql
CREATE TABLE chunks (
    chunk_id    TEXT PRIMARY KEY,
    source_type TEXT NOT NULL CHECK(source_type IN ('conversation', 'file')),
    source_id   TEXT NOT NULL,          -- conversation_id or file path
    source_name TEXT,                   -- conversation slug or filename
    chunk_index INTEGER NOT NULL,       -- ordering within source
    text        TEXT NOT NULL,          -- the actual chunk content
    token_count INTEGER,               -- estimated tokens
    embedding   BLOB,                  -- float32 vector, NULL until embedded
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_chunks_source ON chunks(source_type, source_id);
```

### `chunks_fts` — FTS5 virtual table over chunk text

```sql
CREATE VIRTUAL TABLE chunks_fts USING fts5(
    text,
    content='chunks',
    content_rowid='rowid'
);
```

### `index_state` — tracks what's been indexed

```sql
CREATE TABLE index_state (
    source_type TEXT NOT NULL,
    source_id   TEXT NOT NULL,
    indexed_at  DATETIME NOT NULL,
    hash        TEXT,                   -- content hash for change detection
    PRIMARY KEY (source_type, source_id)
);
```

### Chunking strategy

- ~1024 tokens per chunk
- Conversations: split at message boundaries (don't split mid-message), concatenate user/agent messages with role prefixes
- Workspace files: split by markdown headings or fixed token window

## 2. Embedding & Search

### Embedding interface

Extend the `llm` package:

```go
type EmbeddingService interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    EmbeddingDimension() int
}
```

### Embedding providers

Three backends, selected via config:

- **`"auto"`** (default) — reuse the active LLM provider's embedding endpoint (OpenAI, Gemini). If the provider has no embedding support (Anthropic today), skip vectors and use FTS5 only.
- **`"ollama"`** — POST to `localhost:11434/api/embed`. Default model: `nomic-embed-text`. Local, no API key needed.
- **`"none"`** — FTS5 only, zero external dependencies.

### Search flow

1. **FTS5 query** — BM25-ranked full-text search, top 20 candidates
2. **Vector query** — embed the query string, cosine similarity via sqlite-vec, top 20 candidates (skip if no embeddings)
3. **Merge** — normalize both score sets to [0,1], combine with 0.4 FTS + 0.6 vector weighting, deduplicate by chunk_id
4. **Return** top N results with chunk text, source info, and relevance score

### Graceful degradation

- No embeddings available → FTS5-only results, no error
- sqlite-vec extension unavailable → FTS5-only with logged warning

## 3. Indexing Pipeline

New `memory` package at `shelley/memory/`.

### Trigger

Post-conversation hook: when a conversation's loop ends and no pending messages, fire-and-forget indexing in a goroutine.

### Flow

1. **Detect** — compare `conversations.updated_at` and workspace file mtimes against `index_state`. Skip if hash matches.
2. **Chunk** — extract text from `llm_data` JSON, split per strategy above.
3. **Write FTS5** — insert chunks and sync to `chunks_fts`. Immediate, no external calls.
4. **Embed** — batch-generate embeddings via configured provider. Write `[]float32` vectors to `chunks.embedding` as BLOBs.
5. **Update index_state** — record source as indexed with content hash.

### Configuration

```go
type MemoryConfig struct {
    Enabled           bool   // default: true
    EmbeddingProvider string // "auto" | "ollama" | "none", default: "auto"
    OllamaModel       string // default: "nomic-embed-text"
    OllamaURL         string // default: "http://localhost:11434"
}
```

### Workspace file discovery

Reuse existing `collectCodebaseInfo()` logic from `server/system_prompt.go` that finds `CLAUDE.md`, `AGENTS.md`, etc.

## 4. The `memory_search` Tool

New tool in `claudetool/memory/`, registered in `NewToolSet()` when memory is enabled.

### Definition

```
Name: "memory_search"
Description: "Search past conversations and workspace files for relevant context.
              Use this when you need to recall previous discussions, decisions,
              or information from earlier sessions."
```

### Input schema

```json
{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "Natural language search query"
    },
    "source_type": {
      "type": "string",
      "enum": ["conversation", "file", "all"],
      "default": "all",
      "description": "Filter by source type"
    },
    "limit": {
      "type": "integer",
      "default": 10,
      "maximum": 25
    }
  },
  "required": ["query"]
}
```

### Output

- **LLMContent**: formatted text — each result shows source name, source type, relevance score, and chunk text
- **Display**: structured JSON for UI to render as collapsible list with conversation links

### Error cases

- No `memory.db` → "No memory index found. Memory will be indexed after conversations complete."
- No embeddings → FTS5-only results, no error
- Empty results → "No relevant memories found for: {query}"

## 5. Integration Points

Three touch points in existing code:

1. **`server/convo.go`** — after conversation loop ends, call `memory.Index()` in a goroutine
2. **`claudetool/toolset.go`** — register `memory_search` in `NewToolSet()`, gated on `MemoryConfig.Enabled`
3. **`server/system_prompt.txt`** — replace `<previous_conversations>` SQL examples with a note about the `memory_search` tool

### sqlite-vec

Use CGO bindings (`github.com/asg017/sqlite-vec-go-bindings/cgo`) since Shelley already uses `mattn/go-sqlite3`. Call `sqlite_vec.Auto()` once at startup. Only needed for `memory.db` connection.

### Startup sequence

1. Open `shelley.db` (existing)
2. Open/create `memory.db`, run schema migrations
3. Load sqlite-vec extension
4. Start server with memory handle available to toolsets

## 6. Testing Strategy

### Unit tests (`memory/`)

- Chunking: message-boundary splitting, token count targeting, heading-based splits
- FTS5: insert, query, verify BM25 ranking
- Vector: mock embedding provider, verify storage/retrieval with cosine distance
- Hybrid merge: score normalization and weighted combination with known inputs
- Index state: skip-on-matching-hash, re-index-on-changed-hash
- Graceful degradation: no embeddings, no memory.db, empty results

### Integration tests (`memory/`)

- End-to-end: create conversation → index → search → verify results
- Provider switching: verify `"auto"`, `"ollama"`, `"none"` all work

### Tool tests (`claudetool/memory/`)

- Input validation, output format, error messages
- Mock memory DB for isolated tool logic testing

All tests use predictable model or mocked providers — no real API calls.

## What This Does NOT Include

- UI changes beyond existing tool result rendering
- Automatic context injection (agent must explicitly call the tool)
- Code file indexing (only conversations + workspace guidance files)
- Real-time indexing during active conversations
