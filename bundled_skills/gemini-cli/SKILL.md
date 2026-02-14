---
name: gemini-cli
description: Run Gemini CLI for one-shot tasks and generate GEMINI.md project configuration. Use when delegating work to Gemini or setting up a project for Gemini CLI usage.
allowed-tools: bash, patch
---

# Gemini CLI

## One-Shot Execution

Run Gemini non-interactively with `-p` (prompt mode):

```bash
gemini -p "your task here"
```

### Key Flags

| Flag | Description |
|------|-------------|
| `-p "prompt"` | Non-interactive headless mode |
| `-m model` | Specify model (e.g., `gemini-2.5-flash`) |
| `--output-format text\|json` | Output format |
| `--yolo` / `-y` | Auto-approve all tool actions |
| `--all-files` / `-a` | Include all project files in context |
| `--debug` / `-d` | Enable debug mode |

### Examples

```bash
# Quick task
gemini -p "Add unit tests for the utils package"

# With model selection
gemini -p "Explain the authentication flow" -m gemini-2.5-pro

# Auto-approve all actions
gemini -p "Fix all linting errors" --yolo

# Pipe content for analysis
cat api.go | gemini -p "Review this API for REST best practices"

# JSON output
gemini -p "List all error types" --output-format json
```

### Long-Running Tasks

For tasks that may take a while, use tmux:

```bash
tmux new-session -d -s gemini-task 'gemini -p "Refactor the entire test suite" --yolo; echo "DONE"'
tmux attach -t gemini-task
```

## Project Configuration

### GEMINI.md

Create `GEMINI.md` at the project root. Gemini CLI reads this for project context. Supports hierarchical placement — subdirectory `GEMINI.md` files provide component-specific context.

```
project/
├── GEMINI.md              # Project-wide context
├── api/
│   └── GEMINI.md          # API-specific guidance
└── frontend/
    └── GEMINI.md          # Frontend-specific guidance
```

GEMINI.md supports `@./path/file.md` import syntax for modular context:

```markdown
# My Project

@./docs/architecture.md
@./docs/conventions.md

## Build Commands
make build
make test
```

### .gemini/settings.json

Create `.gemini/settings.json` for project-level settings:

```json
{
  "model": {
    "model": "gemini-2.5-pro"
  },
  "context": {
    "fileName": ["GEMINI.md"],
    "importFormat": "tree"
  },
  "tools": {
    "sandbox": true
  }
}
```

### File Precedence (lowest to highest)

1. Defaults
2. System: `/Library/Application Support/GeminiCli/settings.json` (macOS)
3. User: `~/.gemini/settings.json`
4. Project: `.gemini/settings.json`
5. Environment variables
6. CLI arguments
