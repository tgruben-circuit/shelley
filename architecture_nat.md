# Percy Architecture: Current State & Road Ahead

**Date:** 2026-02-17
**Repo:** `github.com/tgruben-circuit/percy` (codename: Shelley)

---

## What Percy Is

Percy is a **mobile-friendly, web-based, multi-conversation, multi-modal, multi-model, single-user AI coding agent**. Go backend, SQLite storage, React/TypeScript UI, single-binary deployment. No external runtime dependencies.

Percy's thesis: the best coding agent is one you can use from anywhere (phone, tablet, laptop), that talks to any model (Anthropic, OpenAI, Gemini, Fireworks, custom), manages long-running coding sessions, and can coordinate multiple instances to parallelize work across machines.

---

## System Architecture

```
┌──────────────────────────────────────────────────────────────────────────┐
│                              Percy Binary                                │
│                                                                          │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  │
│  │  cmd/     │  │  server/ │  │  loop/   │  │  llm/    │  │  cluster/ │  │
│  │  percy/   │  │          │  │          │  │  ant/    │  │          │  │
│  │          │──▶│  HTTP    │──▶│  Agent   │──▶│  oai/    │  │  NATS    │  │
│  │  CLI     │  │  SSE     │  │  Loop    │  │  gem/    │  │  Tasks   │  │
│  │  Config  │  │  ConvoMgr│  │  Tools   │  │          │  │  Merge   │  │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘  └──────────┘  │
│                      │                            │                      │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  │
│  │  db/     │  │ subpub/  │  │claudetool│  │  memory/ │  │  models/ │  │
│  │  SQLite  │  │  PubSub  │  │  bash    │  │  cells   │  │  registry│  │
│  │  sqlc    │  │  for SSE │  │  patch   │  │  topics  │  │  factory │  │
│  │  migrate │  │          │  │  browse  │  │  embed   │  │          │  │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘  └──────────┘  │
│                                                                          │
│  ┌──────────────────────────────────┐  ┌──────────────────────────────┐  │
│  │  ui/ (embedded React SPA)        │  │  templates/ (embedded .tar.gz)│  │
│  │  ChatInterface, PatchTool,       │  │  Go project boilerplate       │  │
│  │  BashTool, ConversationDrawer,   │  │                              │  │
│  │  ClusterDashboard, ...           │  │                              │  │
│  └──────────────────────────────────┘  └──────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────────┘
```

---

## Package-by-Package Breakdown

### `cmd/percy/` — CLI Entry Point

Single `main.go` (475 lines). Three subcommands:

- **`serve`** — Starts the HTTP server. The main entrypoint for everything. Flags: `--port`, `--cluster`, `--agent-name`, `--capabilities`, `--systemd-activation`, `--require-header`.
- **`unpack-template`** — Extracts project boilerplate to a directory.
- **`version`** — Prints version info as JSON.

Wiring responsibilities: opens databases, configures LLM providers from env/config, creates embedder, builds tool set config, starts cluster node if `--cluster` is set, seeds notification channels.

### `server/` — HTTP API & Conversation Management

The largest package. Key files:

| File | Role |
|------|------|
| `server.go` | HTTP mux, Server struct, types (APIMessage, StreamResponse), LLMProvider interface |
| `handlers.go` | All HTTP handlers: conversations CRUD, chat, messages, uploads, file reads, models, settings |
| `convo.go` | `ConversationManager` — one per active conversation, owns the Loop, manages working state, SSE broadcasting |
| `system_prompt.go` | Generates system prompts from working directory (git info, codebase files, skills XML) |
| `distill.go` | Conversation distillation — LLM-powered summarization to continue work in a fresh context window |
| `llmconfig.go` | LLM configuration from env vars + config file, gateway support |
| `custom_models.go` | DB-backed custom model definitions |
| `cluster_worker.go` | Cluster task execution: creates worktree, runs conversation, polls until done |
| `cluster_monitor.go` | Starts orchestrator monitor: merge worktree, LLM conflict resolver, dependency watcher |
| `notification_channels.go` | Discord/email notification management |
| `middleware.go` | Logging, CORS, optional header-based auth |
| `exec_terminal.go` | WebSocket-based terminal (PTY) for interactive shell |
| `git_handlers.go` | Git state tracking, working directory changes |
| `debug_handlers.go` | Debug endpoints |
| `versioncheck.go` | Auto-update checking |

**Request flow:**

```
Browser ──POST /api/conversation/{id}/chat──▶ Server
  │                                              │
  │  ◀──SSE /api/conversation/{id}/stream────────│
  │                                              ▼
  │                                     ConversationManager
  │                                              │
  │                                         AcceptUserMessage()
  │                                              │
  │                                              ▼
  │                                          Loop.Go()
  │                                         ┌────┴────┐
  │                                    LLM Call   Tool Exec
  │                                         │         │
  │  ◀──StreamResponse via SubPub────── Record to DB ──┘
```

### `loop/` — The Agentic Loop

`loop.go` (783 lines) is the core execution engine. One `Loop` per active conversation.

**Key behaviors:**
- **Message queue** — user messages queue while LLM is thinking; checked between tool calls for interruptions
- **Tool execution** — finds matching tool by name, calls `tool.Run()`, collects results, sends back to LLM
- **Prompt caching** — sets cache flags on last tool and last user message for Anthropic prompt caching
- **Truncation handling** — if LLM hits max tokens, retries up to 2x with guidance to be more concise; records truncated messages (excluded from context) for cost tracking
- **Missing tool results repair** — fixes conversation history when tool_use blocks lack corresponding tool_results (happens on cancellation)
- **Context window monitoring** — warns at 80% usage, suggests distillation
- **Git state tracking** — detects branch/commit changes at end of turn, records as notification
- **Retryable errors** — retries EOF, connection reset, etc. with backoff

Two entry points:
- `Go()` — runs continuously, waiting for queued messages (used for interactive conversations)
- `ProcessOneTurn()` — processes one user→assistant exchange and stops (used for subagents and cluster tasks)

### `llm/` — LLM Abstraction Layer

Core interface in `llm.go`:

```go
type Service interface {
    Do(context.Context, *Request) (*Response, error)
    TokenContextWindow() int
    MaxImageDimension() int
}
```

**Types:** Message, Content, Tool, ToolOut, Usage, Request, Response, StopReason, ThinkingLevel.

**Providers:**

| Package | Provider | Notes |
|---------|----------|-------|
| `llm/ant/` | Anthropic Claude | Streaming, thinking, prompt caching, image support |
| `llm/oai/` | OpenAI | Two modes: Chat Completions API and Responses API (for o-series reasoning) |
| `llm/gem/` | Google Gemini | Via REST API with custom `gemini/` client |

All providers normalize to the shared `llm.Response` format. Gateway support allows proxying all requests through an LLM gateway URL.

**Additional packages:**
- `llm/imageutil/` — image resizing, HEIC conversion
- `llm/llmhttp/` — shared HTTP client with logging and dumping
- `llm/conversation/` — higher-level conversation manager (budget, multi-turn)

### `claudetool/` — Tool Implementations

Each conversation gets its own `ToolSet` with a shared, mutable working directory.

| Tool | File | Description |
|------|------|-------------|
| `bash` | `bash.go` | Shell command execution via PTY, JIT package installation, timeout, output capture |
| `patch` | `patch.go` | File creation/editing with search-and-replace; simplified schema for weaker models |
| `keyword_search` | `keyword.go` | Code search using ripgrep + tree-sitter; falls back to grep |
| `change_directory` | `changedir.go` | Changes working directory with validation |
| `output_iframe` | `output_iframe.go` | Serves HTML/JS output in an iframe in the UI |
| `subagent` | `subagent.go` | Spawns child conversations for parallelizable subtasks |
| `dispatch_tasks` | `dispatch.go` | Cluster-mode tool for breaking work into tasks for worker agents |
| `todo_write` | `todowrite.go` | Manages a TODO.md checklist file |
| `skill_load` | `skill_load.go` | Loads skill content by name from discovered skills |
| `memory_search` | `memory/` | Vector + FTS search over conversation memory (separate subpackage) |
| `browse` | `browse/` | Chromedp-based browser automation (navigate, screenshot, click, eval) |
| `lsp` | `lsp/` | LSP-based code intelligence (go to definition, find references, hover) |

### `cluster/` — Multi-Agent Coordination

NATS-based distributed coordination for running multiple Percy instances in parallel. ~4,800 lines across 14 implementation files + 13 test files.

**Components:**

| Component | Files | Purpose |
|-----------|-------|---------|
| Embedded NATS | `nats.go` | Starts/connects to NATS server with JetStream |
| JetStream Setup | `jetstream.go` | Creates KV buckets (agents, locks, tasks, cluster) and TASKS stream |
| Agent Registry | `agent.go`, `heartbeat.go` | Agent identity, capabilities, status, heartbeat, stale detection |
| Task Queue | `task.go` | Task lifecycle with CAS-based claiming to prevent double-assignment |
| Orchestrator | `orchestrator.go` | Dependency-aware task plan submission and resolution |
| Worker | `worker.go` | Polls for matching tasks, claims and executes via callback |
| Monitor | `monitor.go` | Event-driven: subscribes to task status, resolves deps, detects stale agents |
| Merge Pipeline | `merge.go` | Git worktree-based merging with LLM conflict resolution |
| LLM Resolver | `llm_resolver.go` | Resolves merge conflicts by providing base/ours/theirs to an LLM |
| File Locks | `locks.go` | Distributed file locking via JetStream KV |
| Node | `node.go` | Integration point tying all components together |

**Task lifecycle:**

```
submitted ──claim──▶ assigned ──work──▶ working ──done──▶ completed ──merge──▶ merged
                                                    │
                                                    ▼
                                                  failed ──requeue──▶ submitted
```

**Cluster modes:**

| Mode | Flag | Behavior |
|------|------|----------|
| Solo | (none) | Normal single-agent Percy |
| Orchestrator | `--cluster :4222` | Embeds NATS, runs full server + coordinator + merge pipeline |
| Worker | `--cluster nats://host:4222` | Connects to NATS, runs full server + task executor |

### `db/` — SQLite Storage

Pure Go SQLite (`modernc.org/sqlite`), code-generated queries via sqlc.

**Schema** (16 migrations):

| Table | Purpose |
|-------|---------|
| `conversations` | Chat sessions: ID, slug, user_initiated, cwd, archived, parent_conversation, model |
| `messages` | All messages: user, agent, tool, system, error, gitinfo types; llm_data, user_data, usage_data, display_data as JSON |
| `llm_requests` | Raw LLM request/response logging for debugging |
| `custom_models` | User-defined model configurations (provider, model_id, api_key, base_url) |
| `notification_channels` | Discord/email notification settings |
| `settings` | Key-value store for app settings |
| `migrations` | Schema version tracking |

Key design: messages store `llm_data` (raw LLM format), `user_data` (UI display), and `display_data` (rich tool output) as separate JSON columns. This allows reconstructing LLM history without the display overhead, and displaying tool results without parsing LLM format.

### `memory/` — Conversation Memory System

Separate SQLite database (`memory.db`) with topic-consolidated memory.

**Architecture (3-layer):**

1. **Extraction** (`extract.go`) — LLM-powered: converts conversation transcripts into typed memory cells (fact, decision, preference, task, risk, code_ref)
2. **Topic Organization** (`topic.go`, `cell.go`) — groups cells into auto-discovered themes with FTS5 search
3. **Consolidation** (`consolidate.go`) — periodically merges topic cells into stable summaries, marks superseded cells

**Embedding support** (`embed.go`, `embed_ollama.go`, `embed_openai.go`):
- Ollama (nomic-embed-text, local)
- OpenAI (text-embedding-3-small, remote)
- Hybrid search: FTS5 keyword matching + optional vector similarity

**Indexing** (`index.go`):
- Content-hash based: skips re-indexing unchanged conversations
- Falls back to chunk-based indexing if no LLM service available
- Post-conversation hook with backpressure queue

### `models/` — Model Discovery & Selection

Registry of all known models with factory functions.

**Known models:** Claude (Sonnet 4.5, Haiku 4.5, Opus 4.5, Opus 4.6), GPT (5.2 Codex), Gemini (2.5 Pro/Flash), Qwen3 Coder (Fireworks), custom DB-backed models.

Each model entry includes: ID, display name, provider, factory function (creates `llm.Service`), context window, thinking support.

Model selection: strong models (sonnet/opus) get full patch schema; weaker models get simplified schema.

### `subpub/` — Pub/Sub for SSE

Generic `SubPub[T]` — in-process pub/sub for broadcasting SSE events to UI subscribers. Each conversation has its own SubPub instance. Subscribers block until new data arrives or context is cancelled.

### `ui/` — React/TypeScript Frontend

Built with esbuild, no framework overhead. ~34 components.

**Key components:**

| Component | Purpose |
|-----------|---------|
| `App.tsx` | Root: routing, conversation list, drawer, command palette |
| `ChatInterface.tsx` | Main chat view: message list, SSE streaming, working state |
| `MessageInput.tsx` | Text input with model picker, image upload, submit |
| `Message.tsx` | Renders a message with tool-specific components |
| `BashTool.tsx` | Terminal-style bash output with syntax highlighting |
| `PatchTool.tsx` | File diff viewer with @pierre/diffs |
| `KeywordSearchTool.tsx` | Search results display |
| `ConversationDrawer.tsx` | Sidebar: conversation list, create, archive, rename |
| `ModelPicker.tsx` | Model selection dropdown |
| `ClusterDashboard.tsx` | Cluster agent/task status panel |
| `TerminalPanel.tsx` | Interactive terminal (xterm.js + WebSocket PTY) |
| `SubagentTool.tsx` | Subagent conversation viewer |
| `CommandPalette.tsx` | Keyboard-driven command palette |
| `NotificationsModal.tsx` | Notification channel management |
| `SystemPromptView.tsx` | Shows current system prompt |
| `DiffViewer.tsx` | Inline diff rendering |
| `DirectoryPickerModal.tsx` | Working directory selection |
| `VersionChecker.tsx` | Auto-update notification |

**Type generation:** `go2ts` binary converts Go structs → TypeScript interfaces, output in `ui/src/generated-types.ts`.

### `skills/` — Skill Discovery & Parsing

Discovers SKILL.md files from project directories, user config, and bundled skills. Skills are instructions that modify the system prompt, giving the agent specialized workflows (like TDD, debugging, code review).

**Discovery priority** (highest wins on name conflict):
1. Project `.skills/` directories
2. In-tree SKILL.md files
3. User `~/.config/shelley/` and `~/.config/agents/skills`
4. Bundled embedded skills (in `bundled_skills/`)

### `templates/` — Project Boilerplate

Currently one template (`go`). Embedded as `.tar.gz` via `//go:embed`. Unpacked via CLI `percy unpack-template go /path`.

### Other Packages

| Package | Purpose |
|---------|---------|
| `gitstate/` | Detects git state (branch, commit, worktree) for change tracking |
| `slug/` | Human-readable slug generation for conversations |
| `version/` | Build version info injection via ldflags |

---

## What's Built and Working

### Core Agent (Mature)
- Multi-model LLM abstraction (Anthropic, OpenAI, Gemini, Fireworks, custom)
- Agentic loop with tool execution, retries, truncation recovery
- Tool suite: bash, patch, keyword search, browser, LSP, subagent, change dir, output iframe, todo
- Conversation management: create, archive, rename, distill, multi-conversation
- Real-time SSE streaming to UI
- Prompt caching for Anthropic
- Context window monitoring and distillation
- System prompt generation from project context (git, files, skills)
- WebSocket PTY terminal
- Image upload and multi-modal input

### UI (Mature)
- Mobile-friendly chat interface
- Rich tool output rendering (diffs, terminals, screenshots, iframes)
- Conversation drawer with search
- Model picker with custom model support
- Command palette
- Notification channel management
- Settings and system prompt viewer
- Version checking

### Storage (Mature)
- SQLite with 16 migrations
- sqlc code generation
- Separate memory database
- Custom model persistence
- Notification channel persistence
- App settings

### Memory System (Functional, Evolving)
- Topic-consolidated memory with LLM extraction
- FTS5 + optional vector embedding search
- Ollama and OpenAI embedder support
- Post-conversation indexing with content-hash dedup
- Memory search tool available to the agent

### Cluster System (Recently Built, Under Active Development)
- NATS-based coordination (embedded or external)
- Agent registry with heartbeat and stale detection
- Task queue with CAS-based claiming
- Orchestrator with dependency-aware scheduling
- Worker task execution via git worktrees
- LLM-assisted merge conflict resolution
- Monitor with event-driven dependency resolution
- `dispatch_tasks` tool for LLM-driven task decomposition
- Cluster dashboard in UI
- Integration test coverage (e2e merge pipeline)

### Skills System (Functional)
- Skill discovery from project, user, and bundled sources
- `skill_load` tool for on-demand skill loading
- Bundled skills for Claude Code, OpenCode, Gemini CLI

### Deployment (Mature)
- Single binary, zero runtime dependencies
- GoReleaser for multi-platform builds
- Homebrew cask distribution
- Systemd socket activation support
- GitHub Actions CI/CD

---

## What Needs Work: The Road Ahead

### Cluster System — Hardening & Scale

The cluster system is the most recently built and has the most open work:

1. **No UI for plan creation** — The orchestrator LLM can dispatch tasks via the `dispatch_tasks` tool, but there's no way for a human to create, edit, or approve a task plan through the UI before dispatch. Right now the LLM decides everything.

2. **No task progress visibility** — The `ClusterDashboard.tsx` exists but is basic. Need real-time task status streaming (which tasks are running, what each worker is doing, merge results) so the orchestrator user can monitor and intervene.

3. **Worker capability matching is rudimentary** — Workers declare capabilities as string lists, but matching is basic. No affinity scoring, no learned routing, no workload balancing.

4. **No task cancellation** — Once a task is dispatched, there's no way to cancel it. Workers poll until completion. Need a cancel-task flow through NATS that interrupts the worker's conversation loop.

5. **Single-task workers** — Each worker can only execute one task at a time. For I/O-bound tasks (waiting on LLM responses), this leaves workers idle. Could allow concurrent task execution per worker.

6. **No result review before merge** — Completed task branches are automatically merged. The orchestrator should optionally review results (diff preview, test results) before merging.

7. **Merge pipeline lacks testing integration** — After merging a worker's branch, the system should run tests to verify the merge didn't break anything. Currently it just merges and moves on.

8. **Orchestrator state is ephemeral** — The `TaskPlan` and orchestrator state live in memory. If the orchestrator restarts, it loses track of the plan. Should persist plans to NATS KV or SQLite.

9. **No multi-repo support** — All workers must operate on the same repo. Context.Repo field exists but isn't used for routing.

### Memory System — Quality & Scale

10. **Consolidation is untested at scale** — The topic consolidation pipeline (merge cells into summaries, mark superseded) works but hasn't been stress-tested with hundreds of conversations.

11. **Embedding coverage is optional** — Vector search only works if an embedder is configured. Without it, memory search falls back to FTS5 only. Should work well enough without embeddings but hasn't been validated thoroughly.

12. **No memory pruning** — Old superseded cells accumulate. The design calls for pruning cells older than 90 days that are superseded, but it's not implemented.

13. **No memory UI** — Users can't browse, edit, or delete memories. The only interface is the agent's `memory_search` tool. A memory browser in the UI would help users understand and curate what Percy remembers.

### LLM Layer

14. **Gemini image dimension limits unknown** — `gem.go` has a TODO: "determine actual Gemini image dimension limits." Currently returns 0 (no limit enforced).

15. **OpenAI image dimension limits unknown** — Same TODO in `oai_responses.go`.

16. **Context window info is hardcoded** — `TokenContextWindow()` is implemented per-provider with hardcoded values. Should come from the model registry.

17. **No streaming** — LLM responses are collected in full before being sent to the UI. Streaming would give much better perceived latency, especially for long responses.

### Tools

18. **No file read tool** — The agent uses `keyword_search` and `bash cat` to read files. A dedicated read tool would be cleaner and could enforce size limits, support pagination, and handle binary detection.

19. **Browser tool reliability** — Chromedp-based browsing can be flaky. Screenshot timing, page load detection, and error recovery could all be more robust.

### UI

20. **No conversation search** — The conversation drawer lists conversations but has no search/filter. With many conversations, finding old work is hard.

21. **Mobile experience gaps** — While mobile-friendly by design, some components (terminal, diff viewer, command palette) may not work well on small screens.

22. **No dark mode toggle** — The UI appears to have one theme. A dark/light toggle would be welcome.

### Infrastructure

23. **No auth beyond header check** — Percy is single-user by design, but the `--require-header` mechanism is minimal. For shared deployments (e.g., team server), need proper auth.

24. **No rate limiting** — No protection against runaway LLM costs. Should have per-conversation or per-hour budget limits.

25. **No observability** — No metrics, no tracing, no structured logging beyond slog. For production deployments, need Prometheus metrics or similar.

26. **Database is single-file SQLite** — Fine for single-user, but the cluster mode implies multiple instances. Each Percy instance has its own SQLite DB. There's no shared conversation state across the cluster — only task coordination via NATS.

### Testing

27. **Gemini provider tests need local replay** — `gemini_test.go` has a TODO: "replace with local replay endpoint."

28. **E2E tests are headless-only in CI** — No visual regression testing. Playwright is configured but only runs in headless mode.

---

## Design Principles

These run through every decision in the codebase and should guide future work:

1. **Brevity** — One way to do things. Refactor relentlessly. No compatibility shims.
2. **Propagate errors** — No fallbacks, no silent swallowing. Crash if you can't handle it.
3. **Single binary** — Everything embeds. No external runtime dependencies.
4. **Additive features** — Cluster mode is opt-in. Memory is opt-in. Skills are opt-in. Solo mode is the default and must always work.
5. **Git as source of truth** — Workers use worktrees, merges happen in git, branches are the unit of work.
6. **LLM-assisted everything** — Conflict resolution, conversation distillation, memory extraction, skill discovery — if an LLM can do it better than a heuristic, use the LLM.
7. **Test without API keys** — The predictable model enables full testing without touching any LLM provider.

---

## Key File Quick Reference

| What | Where |
|------|-------|
| Server startup | `cmd/percy/main.go:runServe()` |
| HTTP routing | `server/server.go:registerRoutes()` |
| Conversation lifecycle | `server/convo.go:ConversationManager` |
| Agent loop | `loop/loop.go:processLLMRequest()` |
| Tool registration | `claudetool/toolset.go:NewToolSet()` |
| System prompt | `server/system_prompt.go` + `server/system_prompt.txt` |
| LLM interface | `llm/llm.go:Service` |
| Anthropic provider | `llm/ant/ant.go` |
| OpenAI provider | `llm/oai/oai.go` + `llm/oai/oai_responses.go` |
| Gemini provider | `llm/gem/gem.go` |
| Cluster node | `cluster/node.go:StartNode()` |
| Task queue | `cluster/task.go:TaskQueue` |
| Merge pipeline | `cluster/merge.go:MergeWorktree` |
| Orchestrator | `cluster/orchestrator.go` |
| Worker execution | `server/cluster_worker.go:executeClusterTask()` |
| Memory indexing | `memory/index.go:IndexConversation()` |
| Memory search | `memory/search.go` |
| DB schema | `db/schema/*.sql` |
| DB queries | `db/query/*.sql` → `db/generated/` |
| UI entry | `ui/src/App.tsx` |
| Chat interface | `ui/src/components/ChatInterface.tsx` |
| Type generation | `cmd/go2ts.go` → `ui/src/generated-types.ts` |
| CI | `.github/workflows/test.yml`, `.github/workflows/release.yml` |
| Planning docs | `docs/plans/` |
