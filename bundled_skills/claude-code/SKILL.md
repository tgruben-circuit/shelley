---
name: claude-code
description: Run Claude Code CLI for one-shot tasks and generate CLAUDE.md project configuration. Use when delegating work to Claude Code or setting up a project for Claude Code usage.
allowed-tools: bash, patch
---

# Claude Code

## One-Shot Execution

Run Claude Code non-interactively with `-p` (print mode):

```bash
claude -p "your task here"
```

### Key Flags

| Flag | Description |
|------|-------------|
| `-p "prompt"` | Non-interactive mode, print result and exit |
| `--model sonnet\|opus` | Override model |
| `--max-turns N` | Limit agentic turns |
| `--max-budget-usd N` | Maximum spend before stopping |
| `--output-format text\|json\|stream-json` | Output format |
| `--allowedTools "Bash,Read,Edit"` | Auto-approve specific tools |
| `--dangerously-skip-permissions` | Skip all permission prompts |
| `--system-prompt "..."` | Replace system prompt |
| `--append-system-prompt "..."` | Append to system prompt |
| `--continue` | Continue most recent conversation |

### Examples

```bash
# Quick task
claude -p "Add error handling to the API endpoints in server.go"

# With model selection and budget
claude -p "Refactor the auth module" --model opus --max-budget-usd 5

# Pipe content for analysis
cat server.go | claude -p "Review this code for security issues"

# JSON output for parsing
claude -p "List all TODO comments" --output-format json
```

### Long-Running Tasks

For tasks that may take a while, use tmux:

```bash
tmux new-session -d -s claude-task 'claude -p "Build the entire test suite" --max-turns 50; echo "DONE"'
tmux attach -t claude-task  # Check progress
```

## Project Configuration

### CLAUDE.md

Create `CLAUDE.md` at the project root. This is the primary context file Claude Code reads on every conversation start. Include:

- Build and test commands
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

### .claude/settings.json

Create `.claude/settings.json` for shared team settings:

```json
{
  "permissions": {
    "allow": [
      "Bash(make *)",
      "Bash(go test *)",
      "Bash(npm run *)"
    ],
    "deny": [
      "Bash(rm -rf *)",
      "Bash(git push --force*)"
    ]
  },
  "env": {
    "GOFLAGS": "-count=1"
  }
}
```

### .claude/CLAUDE.local.md

For personal overrides (add to `.gitignore`):

```markdown
# Personal preferences
- I prefer verbose test output
- Always run `make lint` before committing
```

### File Precedence (highest to lowest)

1. CLI arguments
2. `.claude/CLAUDE.local.md` (personal, gitignored)
3. `.claude/settings.json` (team-shared)
4. `CLAUDE.md` (project root)
5. `~/.claude/CLAUDE.md` (global user-level)
