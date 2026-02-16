# Topic-Consolidated Memory System

**Date:** 2026-02-15
**Status:** Approved
**Problem:** Memory search returns stale context — old conversation chunks persist with equal weight to current knowledge, surfacing outdated decisions and abandoned approaches.

## Solution

Replace flat chunking with structured, topic-organized memory that consolidates over time. Three components:

1. **LLM-powered extraction** — convert conversation transcripts into typed memory cells
2. **Topic grouping** — organize cells into auto-discovered themes
3. **Incremental consolidation** — periodically merge topic cells into stable summaries

## Schema

Three tables in `memory.db` (replaces `chunks`):

```sql
CREATE TABLE cells (
    cell_id     TEXT PRIMARY KEY,
    topic_id    TEXT REFERENCES topics(topic_id),
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

CREATE TABLE topics (
    topic_id    TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    summary     TEXT,
    embedding   BLOB,
    cell_count  INTEGER DEFAULT 0,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE index_state (
    source_type TEXT NOT NULL,
    source_id   TEXT NOT NULL,
    indexed_at  DATETIME NOT NULL,
    hash        TEXT,
    PRIMARY KEY (source_type, source_id)
);
```

Plus FTS5 virtual tables on `cells.content` and `topics.summary`.

### Cell Types

| Type | Description | Example |
|------|-------------|---------|
| `fact` | Concrete information | "Percy uses SQLite with sqlc codegen" |
| `decision` | Choice + rationale | "Switched to argon2id because bcrypt was too slow" |
| `preference` | User style/tooling | "User prefers tabs over spaces" |
| `task` | Work item status | "Authentication middleware is blocked on JWT library choice" |
| `risk` | Gotcha/constraint | "pkill -f percy kills the parent process" |
| `code_ref` | File path + role | "server/auth.go handles JWT validation middleware" |

### Superseding

When consolidation detects a newer cell contradicts an older one, the old cell is marked `superseded=TRUE`. Superseded cells are excluded from search but kept for audit. Cells older than 90 days that are superseded may be pruned.

## Extraction Pipeline

Runs post-conversation (same hook as current indexing):

### Step 1: Extract Cells

LLM call with conversation transcript. Prompt asks for JSON array of cells:

```
System: You are a memory extraction engine. Convert this conversation
into structured memory cells. Return a JSON array where each object has:
- cell_type: one of (fact, decision, preference, task, risk, code_ref)
- salience: 0.0-1.0 (how important is this for future work?)
- content: compressed, factual statement (1-2 sentences)
- topic_hint: short topic label (e.g., "authentication", "database schema")

Focus on information useful in FUTURE conversations.
Discard: greetings, debugging dead-ends, intermediate states overwritten,
routine acknowledgments.
```

### Step 2: Assign to Topics

For each cell:
1. Embed the cell content
2. Compare against existing topic embeddings (cosine similarity)
3. If similarity > 0.7 → assign to that topic
4. Otherwise → create new topic from `topic_hint`, embed its name

### Step 3: Lazy Consolidation

After inserting cells, check each affected topic. Consolidate only when 5+ unsummarized cells have accumulated. This avoids unnecessary LLM calls for rarely-changing topics.

## Consolidation

LLM call per topic when triggered:

```
System: You are a memory consolidation engine. Given a topic and its
cells, produce an updated summary.

Rules:
- Newer cells win over older contradictions. List superseded cell IDs.
- Keep summary under 150 words.
- Focus on current state, not history.
- Preserve specific values: file paths, config keys, API shapes.

Return JSON: { "summary": "...", "superseded_cell_ids": [...] }
```

After consolidation:
1. Update `topics.summary`
2. Re-embed the summary
3. Mark superseded cells
4. Update `topics.updated_at`

## Search & Retrieval

Two-tier search strategy:

### Tier 1: Topic Summaries

Search `topics.summary` via FTS5 + vector similarity. Return top 3 matching summaries. Compact (~150 words each), represent current consolidated knowledge.

### Tier 2: Individual Cells

Search non-superseded `cells.content` via hybrid search (FTS + vector), weighted by `salience * exp(-0.01 * age_days)`. Return top 7 cells. Provide specific details summaries may have compressed.

### Combined Output

Up to 10 results (3 summaries + 7 cells), labeled by type:

```
Found 8 relevant memories:

--- Topic Summary: "Authentication" (updated 3 days ago) ---
Auth uses JWT with RS256. Tokens stored in httpOnly cookies...

--- Cell [decision] (2 days ago, salience: 0.9) ---
Switched from bcrypt to argon2id for password hashing because...
```

### Tool Schema

```json
{
  "query": "string (required)",
  "source_type": "conversation | file | all",
  "detail_level": "summary | full (default: full)",
  "limit": "integer (default 10, max 25)"
}
```

- `summary`: topic summaries only
- `full`: summaries + individual cells

## Integration Points

| Component | Change |
|-----------|--------|
| `memory/schema.sql` | New `cells` + `topics` tables, FTS5 on both |
| `memory/extract.go` | **New** — LLM-powered cell extraction |
| `memory/topic.go` | **New** — topic discovery + assignment |
| `memory/consolidate.go` | **New** — topic consolidation + superseding |
| `memory/search.go` | Rewritten — two-tier search on new schema |
| `memory/index.go` | Rewritten — extraction pipeline replaces chunking |
| `memory/db.go` | Migration logic for old → new schema |
| `claudetool/memory/tool.go` | Updated — new output format, `detail_level` param |
| `server/convo.go` | Updated — passes LLM service to indexer |

### What stays unchanged

- Distillation (`distill.go`) — operates on conversation messages, not memory
- System prompt generation
- Context window tracking
- Embedding providers (Ollama, OpenAI)

### Graceful Degradation

- No embedder → FTS-only search on cells + topic summaries
- No LLM for extraction → fall back to current chunking (cells become raw chunks with `cell_type='fact'`, `salience=0.5`)
- Empty memory.db → "No memory index found"

### Schema Migration

On `memory.Open()`, detect old schema (has `chunks`, no `cells`) and:
1. Create new tables
2. Drop old tables (memory.db is a derived index)
3. Clear `index_state` to force full re-indexing

## Inspiration

Adapted from [MarkTechPost: Self-Organizing Agent Memory](https://www.marktechpost.com/2026/02/14/how-to-build-a-self-organizing-agent-memory-system-for-long-term-ai-reasoning/) with modifications for Percy's coding-agent context: `code_ref` cell type, lazy consolidation, hybrid FTS+vector search, graceful degradation.
