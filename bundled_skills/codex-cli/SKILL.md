---
name: codex-cli
description: Run OpenAI Codex CLI for one-shot tasks and generate AGENTS.md project configuration. Use when delegating work to Codex or setting up a project for Codex CLI usage.
allowed-tools: bash, patch
---

# Codex CLI

## One-Shot Execution

Run Codex non-interactively with `codex exec`:

```bash
codex exec "your task here"
```

Codex reads the prompt from the argument, or from stdin if the prompt is `-` or omitted. If stdin is piped *and* a prompt is provided, stdin is appended as a `<stdin>` block.

### Key Flags

| Flag | Description |
|------|-------------|
| `codex exec "prompt"` | Non-interactive headless mode |
| `-m, --model <MODEL>` | Override model (e.g., `gpt-5-codex`, `gpt-5.3-codex-spark`) |
| `-s, --sandbox <MODE>` | `read-only`, `workspace-write`, or `danger-full-access` |
| `--dangerously-bypass-approvals-and-sandbox` | Skip all prompts and sandboxing (use only inside an external sandbox) |
| `-C, --cd <DIR>` | Run with `<DIR>` as the working root |
| `--add-dir <DIR>` | Additional writable directories |
| `-i, --image <FILE>` | Attach image(s) to the prompt (repeatable) |
| `-p, --profile <NAME>` | Use a config profile from `~/.codex/config.toml` |
| `-c key=value` | Override any config value inline (TOML syntax) |
| `--json` | Emit JSONL events to stdout |
| `-o, --output-last-message <FILE>` | Write the agent's final message to a file |
| `--output-schema <FILE>` | Constrain the final response to a JSON Schema |
| `--skip-git-repo-check` | Allow running outside a Git repo |
| `--ephemeral` | Don't persist a session file |
| `codex exec resume --last` | Continue the most recent session |
| `codex exec review` | Run a non-interactive code review |

### Examples

```bash
# Quick task (default sandbox: workspace-write)
codex exec "Add error handling to the API endpoints in server.go"

# Read-only investigation
codex exec -s read-only "Explain how auth tokens are validated"

# Override model and write the final message to a file
codex exec -m gpt-5-codex -o /tmp/codex-out.md "Refactor the cache layer"

# Pipe content for analysis
cat server.go | codex exec "Review this code for security issues"

# JSONL stream for programmatic parsing
codex exec --json "List all TODO comments" | jq -c 'select(.type=="agent_message")'

# Resume the previous session ("keep going")
codex exec resume --last "apply the top fix"

# Non-interactive code review
codex exec review
```

### Long-Running Tasks

For tasks that may take a while, use tmux:

```bash
tmux new-session -d -s codex-task 'codex exec "Build the entire test suite"; echo "DONE"'
tmux attach -t codex-task
```

## Project Configuration

### AGENTS.md

Create `AGENTS.md` at the project root. Codex reads it on every session as the primary project context. Codex also walks up from the working directory and merges `AGENTS.md` files it finds, so subdirectories can hold component-specific guidance.

Include:

- Build, test, and lint commands
- Architecture overview
- Code conventions and style rules
- Important file paths
- What NOT to do

Example structure:

```markdown
# Project Name

## Build Commands
make build    # Build the project
make test     # Run all tests
make lint     # Run linters

## Architecture
Brief description of the system architecture, key directories,
and how components interact.

## Code Conventions
- Error handling approach
- Naming conventions
- Testing requirements
```

### ~/.codex/config.toml

User-level Codex configuration. Common settings:

```toml
model = "gpt-5-codex"
approval_policy = "on-request"   # never | on-failure | on-request | untrusted
sandbox_mode = "workspace-write" # read-only | workspace-write | danger-full-access

[sandbox_workspace_write]
network_access = false

# Named profiles selectable with `codex exec -p <name>`
[profiles.review]
model = "gpt-5-codex"
sandbox_mode = "read-only"

[profiles.spark]
model = "gpt-5.3-codex-spark"
```

Inline overrides use the same dotted-path syntax:

```bash
codex exec -c model='"gpt-5-codex"' -c sandbox_mode='"read-only"' "audit deps"
```

### Authentication

Codex needs an authenticated session before `exec` will work:

```bash
codex login       # Opens browser-based login
codex logout      # Clear credentials
```

If `codex exec` fails with an auth error, run `codex login` — do not improvise alternate auth flows.

### File Precedence (highest to lowest)

1. CLI arguments and `-c key=value` overrides
2. `--profile` selection from `~/.codex/config.toml`
3. `~/.codex/config.toml` top-level values
4. Built-in defaults

## Delegation Notes

When Percy delegates a task to Codex:

- Prefer `codex exec` over the interactive TUI — always non-interactive from within Percy.
- Pass the user's task text through as-is; do not pre-solve or pre-analyze it.
- Default to `workspace-write` unless the user asked for read-only / review / diagnosis only.
- For "keep going", "resume", or "dig deeper" follow-ups, use `codex exec resume --last`.
- Surface Codex's output verbatim. If Codex returned review findings, present them ordered by severity and **stop** — do not auto-apply fixes. Ask the user which findings to address first.
- If `codex exec` fails or returns nothing, report the failure (including the most actionable stderr lines) and stop. Do not substitute a Percy-side implementation.
