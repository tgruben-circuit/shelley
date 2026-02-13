# Memory Search

Shelley's memory search gives the agent the ability to recall past conversations and workspace files through a dedicated `memory_search` tool. It replaces the previous approach of shelling out to `sqlite3` with raw SQL.

## How It Works

### Indexing

After a conversation's agentic loop ends, Shelley automatically indexes the conversation's messages into a separate `memory.db` database alongside the main `shelley.db`. Workspace guidance files (`CLAUDE.md`, `AGENTS.md`, etc.) are also indexed.

The indexing pipeline:

1. Extracts user and agent text from conversation messages
2. Chunks text at message boundaries (~1024 tokens per chunk)
3. Computes a SHA-256 content hash to skip re-indexing unchanged conversations
4. Writes chunks to a SQLite FTS5 full-text index for keyword search
5. Optionally generates vector embeddings for semantic search

### Search

The `memory_search` tool performs **hybrid search** combining two strategies:

- **FTS5 keyword search** (BM25 ranking) — finds exact and stemmed word matches
- **Vector similarity search** (cosine similarity) — finds semantically related content even without shared keywords

Results from both are normalized to [0,1] and merged with weighted scoring (0.4 FTS + 0.6 vector). When no embeddings are available, the tool gracefully degrades to FTS-only.

### Embedding Providers

Vector embeddings are optional. Three backends are supported:

| Provider | Config | Description |
|----------|--------|-------------|
| `auto` (default) | Reuses configured LLM provider | Uses OpenAI/Gemini embedding endpoints. Falls back to FTS-only for Anthropic. |
| `ollama` | Local Ollama instance | Calls `localhost:11434/api/embed` with a model like `nomic-embed-text`. No API key needed. |
| `none` | No embeddings | FTS5-only search. Zero external dependencies. |

## Architecture

```
memory/
  db.go              Database open/close, migrations
  schema.sql         FTS5 tables, triggers, index_state
  chunk.go           Text chunking (messages + markdown)
  embed.go           Embedder interface, cosine similarity, BLOB serialization
  embed_ollama.go    Ollama embedding provider
  search.go          FTS5, vector, and hybrid search
  index.go           Indexing pipeline (conversations + files)

claudetool/memory/
  tool.go            memory_search tool (wraps HybridSearch for LLM use)
```

### Data Flow

```
Conversation ends
  -> server/convo.go extracts user/agent text
  -> memory.IndexConversation() chunks + embeds + stores
  -> memory.db (chunks table + FTS5 index + embeddings)

Agent calls memory_search
  -> claudetool/memory/tool.go parses query
  -> memory.HybridSearch() runs FTS5 + vector search
  -> Results returned to agent as formatted text
```

### Database Schema

`memory.db` contains three tables:

- **`chunks`** — indexed text segments with source metadata and optional embedding BLOBs
- **`chunks_fts`** — FTS5 virtual table kept in sync via triggers
- **`index_state`** — tracks what's been indexed (source type + ID + content hash)

The memory database is a derived index. It can be deleted and rebuilt from conversation history at any time.

## Tool Interface

The agent sees the `memory_search` tool with this schema:

```json
{
  "query": "string (required) — natural language search query",
  "source_type": "conversation | file | all (default: all)",
  "limit": "integer (default: 10, max: 25)"
}
```

Example agent usage:

```
memory_search(query="how did we handle authentication")
memory_search(query="database migration strategy", source_type="conversation")
memory_search(query="project conventions", source_type="file", limit=5)
```

## Design Decisions

- **Separate database** — `memory.db` lives alongside `shelley.db` so the index can be rebuilt without touching conversation data
- **Pure Go cosine similarity** — no `sqlite-vec` extension needed since Shelley uses `modernc.org/sqlite` (pure Go, no CGO). Brute-force scan is fast enough for a single-user agent with thousands of chunks
- **Post-conversation indexing** — indexing runs after the conversation loop ends, not during active conversations, to avoid runtime overhead
- **Graceful degradation** — if memory DB fails to open, embeddings aren't available, or search returns nothing, the agent gets a clear message and everything else works normally
