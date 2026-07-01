# Percy: a coding agent

Percy is a fork of [Shelley](https://github.com/boldsoftware/shelley) — an awesome foundation built by [Bold Software](https://github.com/boldsoftware) — with a bunch of new features on top.

Percy is a mobile-friendly, multi-conversation, multi-modal,
multi-model, single-user coding agent with both a web UI and a terminal UI. It does not come with authorization or sandboxing:
bring your own.

*Mobile-friendly* because ideas can come any time.

*Web and terminal UI*, because you should use whichever fits your workflow — browser, SSH session, or headless server.

*Multi-modal* because screenshots, charts, and graphs are necessary, not to mention delightful.

*Multi-model* to benefit from all the innovation going on.

*Single-user* because it makes sense to bring the agent to the compute.

## What's new in Percy

### Memory Search

Percy remembers past conversations. After each conversation ends, messages are automatically chunked and indexed into a separate memory database. The agent can recall earlier decisions, code changes, and context using the `memory_search` tool. Supports hybrid search combining FTS5 keyword matching with optional vector embeddings (Ollama, or FTS5-only with zero dependencies).

### LSP Code Intelligence

Compiler-accurate code navigation powered by Language Server Protocol. The `code_intelligence` tool gives the agent five operations: **definition**, **references**, **hover**, **symbols**, and **diagnostics**. Works with Go (gopls), TypeScript, Python (pyright), Rust (rust-analyzer), and other LSP-enabled languages.

### Bundled Skills

14 workflow skills ship embedded in the binary, covering test-driven development, systematic debugging, brainstorming, plan writing and execution, code review, git worktrees, parallel agent dispatch, and more. Skills follow the [Agent Skills](https://agentskills.io) specification and can be overridden by user or project-level skills.

### Skills Discovery

Percy discovers skills from multiple sources: bundled skills, user config directories (`~/.config/percy/`, `~/.percy/`), shared agent skills (`~/.config/agents/skills/`), project `.skills/` directories, and SKILL.md files anywhere in the project tree. Use the `/skills` command to see what's available.

### Programmatic Tool Calling

Inspired by [Anthropic's programmatic tool calling](https://platform.claude.com/docs/en/agents-and-tools/tool-use/programmatic-tool-calling), but provider-independent. The `scripted_tools` tool lets the agent write Python scripts that call Percy's tools programmatically — intermediate tool results stay in the script and only the final `print()` output enters the conversation context. This dramatically reduces token consumption for multi-tool workflows like scanning many files, running bulk checks, or aggregating data across tools.

```python
# Agent writes this to check 20 files — only the summary enters context
results = []
for path in file_list:
    content = await read_file(path=path)
    if "TODO" in content:
        results.append(path)
print(f"Found {len(results)} files with TODOs: {', '.join(results)}")
```

Requires `uv` (Python package manager) in PATH.

### Concurrent Tool Execution

When the model returns several tool calls in one turn, parallel-safe tools run concurrently via a goroutine pool sized to `GOMAXPROCS`, while side-effecting tools run sequentially after. Results merge back in original call order so the model never sees reordering.

Concurrent-safe: `read`, `keyword`, `subagent`, `browse`, `lsp`, `dispatch`. Sequential: `bash`, `patch`, `changedir`, `output_iframe`, `todo_write`, `skill_load`. Tool authors opt in by setting `Concurrent: true` on the `llm.Tool`.

### Deferred Tool Loading

Heavy or rarely-used tools (browser automation, LSP) are registered as `Deferred` and grouped by `Category`. They don't take up tokens in the system prompt by default — instead, the agent calls a `request_tools` meta-tool with a category name to activate them on demand. This keeps the default tool roster small and lets domain-specific capabilities load only when the conversation actually needs them.

### Conversation Model Switching

Switch the model on an existing conversation mid-flight via `POST /api/conversation/<id>/switch-model` (or the model picker in the UI, which stays visible during an active turn). The server force-sets the conversation's model, the next turn picks it up, and the picker is provider-independent — Anthropic, OpenAI, Gemini, Ollama all interchangeable on the same conversation history.

### Context Window Management

Proactive monitoring of LLM context usage with warnings at 80% capacity, automatic retry on response truncation (up to 2 retries), and increased max output tokens (16,384) for longer responses.

### Conversation Distillation

When a conversation gets long, Percy can distill it into an operational brief and continue in a fresh conversation. The distillation preserves files modified, decisions made, current state, and next steps — everything the agent needs to pick up where it left off.

### Terminal UI (TUI)

`percy tui` launches a Bubble Tea terminal client that connects to a running Percy server via HTTP/SSE. List conversations, chat with streaming responses, markdown rendering, and all the key bindings you'd expect. The TUI is a pure HTTP client — it runs as a peer to the web UI, and both can be open simultaneously against the same server.

```bash
# Start the server
./bin/percy serve

# In another terminal
./bin/percy tui
./bin/percy tui --server http://myhost:8080  # custom server URL
```

### MuninnDB Augmentation (Optional)

Percy can optionally push memories to a [MuninnDB](https://github.com/scrypster/muninndb) server and include its context-aware activations in search results. This enables cross-agent memory sharing on a LAN — what one Percy instance learns becomes available to all others. MuninnDB adds temporal decay (recently-used memories rank higher), Hebbian association (co-retrieved memories form automatic links), and predictive activation on top of Percy's local FTS5 search.

```bash
# Start MuninnDB (Docker)
docker run -d --name muninndb -p 8475:8475 ghcr.io/scrypster/muninndb:latest

# Configure Percy
export PERCY_MUNINN_URL=http://192.168.1.67:8475
export PERCY_MUNINN_VAULT=percy        # optional, default: percy
export PERCY_MUNINN_TOKEN=your-token   # optional, default: empty
```

Local SQLite memory remains primary. MuninnDB is additive — if unreachable, Percy operates normally.

### Markdown Table Rendering

Agent responses containing markdown tables are rendered as styled, readable HTML tables — bordered, with header highlighting, column alignment, hover rows, and horizontal scroll on mobile. No external markdown library needed; a lightweight built-in parser handles detection and rendering while leaving the rest of the text untouched.

### Notification Channels

Get notified when the agent finishes work or hits an error. Supports:

- **Discord** webhooks
- **Email** (via the local gateway)
- **Web Push** — true push notifications to macOS, iOS, Android, and any browser that implements the Web Push standard. Percy generates a VAPID keypair on first run, registers a service worker, and dispatches notifications via `webpush-go`. Tap a notification to jump straight to the conversation.

Channels are configurable via the API or the Notifications modal in the UI, and they persist in the database. A test endpoint verifies connectivity before you commit a configuration.

### User Hooks

Customize Percy's behavior by dropping executable scripts into `$HOME/.config/percy/hooks/<name>`. Four lifecycle hooks are supported:

- **`system-prompt`** — rewrite the rendered system prompt (fires for top-level and subagent conversations)
- **`new-conversation`** — rewrite the prompt, model, or working directory, or supply a slug, before a new conversation is created
- **`chat-message`** — rewrite a follow-up message to an existing conversation
- **`end-of-turn`** — fire-and-forget notification when the agent finishes a turn

Hooks are opt-in (they only run if the executable exists), bounded to 30 seconds, and receive sanitized input on stdin (sensitive request headers like `Authorization` and `Cookie` are stripped). A failing hook aborts its operation — except `end-of-turn`, whose failures are only logged. See [HOOKS.md](HOOKS.md) for the full stdin/stdout contracts and examples.

### Shell Tool

A companion to the bash tool for long-running commands. Instead of hard-killing a command on timeout, the `shell` tool *yields* after `yield_time_seconds`, returning the output so far, the PID/PGID, and a log-file path so the agent can poll, wait, or kill the still-running process. This lets the agent supervise builds, test suites, and big transfers without committing the whole tool call to a hard timeout. For truly long-lived processes (servers, watchers) it still recommends tmux.

### Conversation Tags

Conversations can be tagged for organization. Tags are stored as a JSON array on the conversation and rendered as chips in the drawer, with an inline add/remove editor next to the rename and archive actions. Tag input is normalized on the server (trimmed, de-duplicated, order-preserving).

### Resilient LLM Retries

All four provider backends (Anthropic, OpenAI, Gemini, and the OpenAI Responses API) honor the `Retry-After` header on `429` and `5xx` responses, backing off for at least the server-requested delay instead of a fixed schedule. The Gemini client surfaces a structured API error carrying the status code and headers so retries are status-driven rather than string-matched.

## Installation

### Pre-Built Binaries (macOS/Linux)

```bash
curl -Lo percy "https://github.com/tgruben-circuit/percy/releases/latest/download/percy_$(uname -s | tr '[:upper:]' '[:lower:]')_$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')" && chmod +x percy
```

The binaries are on the [releases page](https://github.com/tgruben-circuit/percy/releases/latest).

### Homebrew (macOS)

```bash
brew install --cask tgruben-circuit/tap/percy
```

### Build from Source

You'll need Go and Node.

```bash
git clone https://github.com/tgruben-circuit/percy.git
cd percy
make
```

## Architecture

The technical stack is Go for the backend, SQLite for storage, TypeScript
and React for the web UI, and Bubble Tea for the terminal UI.

The data model is that Conversations have Messages, which might be from the
user, the model, the tools, or the harness. All of that is stored in the
database, and we use a SSE endpoint to keep the UI updated.

## Acknowledgments

Percy stands on the shoulders of [Shelley](https://github.com/boldsoftware/shelley) by [Bold Software](https://github.com/boldsoftware). Shelley provided the solid core — the agentic loop, multi-model LLM integration, tool execution, SSE-based UI, and the mobile-friendly web interface. We're grateful for that foundation.

Shelley was itself partially based on [Sketch](https://github.com/boldsoftware/sketch).

## Open source

Percy is Apache licensed. We require a CLA for contributions.

## Building Percy

Run `make`. Run `make serve` to start Percy locally.

### Dev Tricks

If you want to see how mobile looks, and you're on your home
network where you've got mDNS working fine, you can
run

```
socat TCP-LISTEN:9001,fork TCP:localhost:9000
```
