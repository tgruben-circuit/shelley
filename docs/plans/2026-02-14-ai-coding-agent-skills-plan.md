# Bundled AI Coding Agent Skills — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Embed 3 AI coding agent skills (Claude Code, OpenCode, Gemini CLI) in the Shelley binary so they're always available to the agent.

**Architecture:** New `bundled_skills/` package uses `//go:embed` to bundle SKILL.md files. An `EmbeddedSkills()` function extracts them to a temp directory and parses them. `server/system_prompt.go:discoverSkills()` appends these at lowest priority so user/project skills can override by name.

**Tech Stack:** Go embed, existing `skills.Parse()`, temp directory for extraction

**Design doc:** `docs/plans/2026-02-14-ai-coding-agent-skills-design.md`

---

### Task 1: Create `bundled_skills/embed.go` with embed and extraction logic

**Files:**
- Create: `bundled_skills/embed.go`

**Step 1: Write the failing test**

Create `bundled_skills/embed_test.go`:

```go
package bundled_skills

import (
	"testing"
)

func TestEmbeddedSkillsReturnsAllThree(t *testing.T) {
	skills, err := EmbeddedSkills()
	if err != nil {
		t.Fatalf("EmbeddedSkills() error: %v", err)
	}

	if len(skills) != 3 {
		t.Fatalf("expected 3 skills, got %d", len(skills))
	}

	expected := map[string]bool{
		"claude-code": false,
		"opencode":    false,
		"gemini-cli":  false,
	}

	for _, s := range skills {
		if _, ok := expected[s.Name]; !ok {
			t.Errorf("unexpected skill name: %q", s.Name)
		}
		expected[s.Name] = true
	}

	for name, found := range expected {
		if !found {
			t.Errorf("missing expected skill: %q", name)
		}
	}
}

func TestEmbeddedSkillsHaveDescriptions(t *testing.T) {
	skills, err := EmbeddedSkills()
	if err != nil {
		t.Fatalf("EmbeddedSkills() error: %v", err)
	}

	for _, s := range skills {
		if s.Description == "" {
			t.Errorf("skill %q has empty description", s.Name)
		}
		if s.Path == "" {
			t.Errorf("skill %q has empty path", s.Name)
		}
	}
}

func TestEmbeddedSkillsIdempotent(t *testing.T) {
	skills1, err := EmbeddedSkills()
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}
	skills2, err := EmbeddedSkills()
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}

	if len(skills1) != len(skills2) {
		t.Fatalf("got different counts: %d vs %d", len(skills1), len(skills2))
	}
	for i := range skills1 {
		if skills1[i].Name != skills2[i].Name {
			t.Errorf("skill %d name mismatch: %q vs %q", i, skills1[i].Name, skills2[i].Name)
		}
		if skills1[i].Path != skills2[i].Path {
			t.Errorf("skill %d path mismatch: %q vs %q", i, skills1[i].Path, skills2[i].Path)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/toddgruben/Projects/shelley && go test ./bundled_skills/`
Expected: FAIL — package doesn't exist yet

**Step 3: Write the implementation**

Create `bundled_skills/embed.go`:

```go
// Package bundled_skills provides AI coding agent skills embedded in the binary.
package bundled_skills

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"shelley.exe.dev/skills"
)

//go:embed */SKILL.md
var skillsFS embed.FS

var (
	cachedSkills []skills.Skill
	cacheErr     error
	once         sync.Once
)

// EmbeddedSkills returns the bundled skills extracted to a temp directory.
// Results are cached after the first call. The temp directory is cleaned up
// when the process exits.
func EmbeddedSkills() ([]skills.Skill, error) {
	once.Do(func() {
		cachedSkills, cacheErr = loadEmbeddedSkills()
	})
	return cachedSkills, cacheErr
}

func loadEmbeddedSkills() ([]skills.Skill, error) {
	entries, err := skillsFS.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("read embedded skills: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "shelley-bundled-skills-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	var result []skills.Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillName := entry.Name()
		content, err := skillsFS.ReadFile(filepath.Join(skillName, "SKILL.md"))
		if err != nil {
			continue
		}

		// Write to temp directory so skills.Parse can read it
		skillDir := filepath.Join(tmpDir, skillName)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			continue
		}
		skillPath := filepath.Join(skillDir, "SKILL.md")
		if err := os.WriteFile(skillPath, content, 0o644); err != nil {
			continue
		}

		skill, err := skills.Parse(skillPath)
		if err != nil {
			continue
		}
		if skill.Name != skillName {
			continue
		}
		result = append(result, skill)
	}

	return result, nil
}
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/toddgruben/Projects/shelley && go test ./bundled_skills/`
Expected: FAIL — skill files don't exist yet (0 skills found). That's OK, we'll fix in Tasks 2-4.

**Step 5: Commit**

```bash
git add bundled_skills/embed.go bundled_skills/embed_test.go
git commit -m "bundled_skills: add embed package for bundled AI coding agent skills"
```

---

### Task 2: Create `claude-code` SKILL.md

**Files:**
- Create: `bundled_skills/claude-code/SKILL.md`

**Step 1: Write the skill file**

Create `bundled_skills/claude-code/SKILL.md` with this content:

```markdown
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
```

**Step 2: Run tests**

Run: `cd /Users/toddgruben/Projects/shelley && go test ./bundled_skills/`
Expected: Tests should now find at least 1 skill; the "all three" test still fails (only 1 of 3).

**Step 3: Commit**

```bash
git add bundled_skills/claude-code/SKILL.md
git commit -m "bundled_skills: add claude-code skill"
```

---

### Task 3: Create `opencode` SKILL.md

**Files:**
- Create: `bundled_skills/opencode/SKILL.md`

**Step 1: Write the skill file**

Create `bundled_skills/opencode/SKILL.md`:

```markdown
---
name: opencode
description: Run OpenCode CLI for one-shot tasks and generate opencode.json project configuration. Use when delegating work to OpenCode or setting up a project for OpenCode usage.
allowed-tools: bash, patch
---

# OpenCode

## One-Shot Execution

Run OpenCode non-interactively with the `run` subcommand:

```bash
opencode run "your task here"
```

All permissions are auto-approved in run mode.

### Key Flags

| Flag | Description |
|------|-------------|
| `"prompt"` | Positional argument, the task to execute |
| `-m provider/model` | Select model (e.g., `anthropic/claude-sonnet-4-5`) |
| `--format default\|json` | Output format |
| `--file path` | Attach file(s) to the message |
| `--continue` | Resume most recent session |
| `--session id` | Continue a specific session |
| `--agent name` | Choose a specific agent |

### Examples

```bash
# Quick task
opencode run "Add input validation to the user registration endpoint"

# With model selection
opencode run -m anthropic/claude-sonnet-4-5 "Refactor the database layer"

# Attach files for context
opencode run --file schema.sql "Generate Go structs from this SQL schema"

# JSON output
opencode run --format json "List all exported functions in the api package"
```

### Long-Running Tasks

For tasks that may take a while, use tmux:

```bash
tmux new-session -d -s opencode-task 'opencode run "Build comprehensive test coverage"; echo "DONE"'
tmux attach -t opencode-task
```

## Project Configuration

### opencode.json

Create `opencode.json` at the project root:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "model": "anthropic/claude-sonnet-4-5",
  "small_model": "anthropic/claude-haiku-4-5",
  "instructions": ["CONTRIBUTING.md", "docs/guidelines.md"],
  "tools": {
    "write": true,
    "bash": true,
    "edit": true
  },
  "permission": {
    "edit": "auto",
    "bash": "ask"
  },
  "compaction": {
    "auto": true,
    "prune": true,
    "reserved": 10000
  },
  "watcher": {
    "ignore": ["node_modules/**", "dist/**", ".git/**"]
  }
}
```

### AGENTS.md

OpenCode's primary context file is `AGENTS.md` at the project root. It falls back to `CLAUDE.md` if `AGENTS.md` doesn't exist. Include the same kind of content as CLAUDE.md:

- Build and test commands
- Architecture overview
- Code conventions
- Important paths

The `instructions` array in `opencode.json` can reference additional files:

```json
{
  "instructions": ["CONTRIBUTING.md", "docs/arch.md", ".cursor/rules/*.md"]
}
```

### File Precedence (lowest to highest)

1. Global: `~/.config/opencode/opencode.json`
2. Custom: `OPENCODE_CONFIG` env var
3. Project: `opencode.json` at project root
4. Inline: `OPENCODE_CONFIG_CONTENT` env var
```

**Step 2: Run tests**

Run: `cd /Users/toddgruben/Projects/shelley && go test ./bundled_skills/`
Expected: 2 of 3 skills found; "all three" test still fails.

**Step 3: Commit**

```bash
git add bundled_skills/opencode/SKILL.md
git commit -m "bundled_skills: add opencode skill"
```

---

### Task 4: Create `gemini-cli` SKILL.md

**Files:**
- Create: `bundled_skills/gemini-cli/SKILL.md`

**Step 1: Write the skill file**

Create `bundled_skills/gemini-cli/SKILL.md`:

```markdown
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
```

**Step 2: Run tests — all should pass now**

Run: `cd /Users/toddgruben/Projects/shelley && go test ./bundled_skills/ -v`
Expected: PASS — all 3 skills found, all tests green.

**Step 3: Commit**

```bash
git add bundled_skills/gemini-cli/SKILL.md
git commit -m "bundled_skills: add gemini-cli skill"
```

---

### Task 5: Integrate embedded skills into `discoverSkills()`

**Files:**
- Modify: `server/system_prompt.go:311-337` (the `discoverSkills` function)

**Step 1: Write the failing test**

Add to `server/system_prompt_test.go`:

```go
func TestSystemPromptIncludesBundledSkills(t *testing.T) {
	// Generate system prompt from a completely empty directory
	// (no user skills, no project skills)
	tmpHome := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() { os.Setenv("HOME", oldHome) })

	emptyDir := t.TempDir()
	prompt, err := GenerateSystemPrompt(emptyDir)
	if err != nil {
		t.Fatalf("GenerateSystemPrompt failed: %v", err)
	}

	// All three bundled skills should appear
	for _, name := range []string{"claude-code", "opencode", "gemini-cli"} {
		if !strings.Contains(prompt, name) {
			t.Errorf("system prompt should contain bundled skill %q", name)
		}
	}
}

func TestUserSkillOverridesBundledSkill(t *testing.T) {
	// Create a user-level skill with the same name as a bundled one
	tmpHome := t.TempDir()
	skillDir := filepath.Join(tmpHome, ".config", "shelley", "claude-code")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	overrideContent := "---\nname: claude-code\ndescription: OVERRIDE_MARKER user override skill.\n---\nCustom content.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(overrideContent), 0o644); err != nil {
		t.Fatal(err)
	}

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() { os.Setenv("HOME", oldHome) })

	emptyDir := t.TempDir()
	prompt, err := GenerateSystemPrompt(emptyDir)
	if err != nil {
		t.Fatalf("GenerateSystemPrompt failed: %v", err)
	}

	// The override description should appear, not the bundled one
	if !strings.Contains(prompt, "OVERRIDE_MARKER") {
		t.Error("system prompt should contain the user override skill description")
	}

	// Other bundled skills should still be present
	if !strings.Contains(prompt, "opencode") {
		t.Error("system prompt should still contain non-overridden bundled skills")
	}
	if !strings.Contains(prompt, "gemini-cli") {
		t.Error("system prompt should still contain non-overridden bundled skills")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/toddgruben/Projects/shelley && go test ./server/ -run TestSystemPromptIncludesBundledSkills -v`
Expected: FAIL — bundled skills not yet wired in.

**Step 3: Modify `discoverSkills()` in `server/system_prompt.go`**

Add import at the top of `server/system_prompt.go`:

```go
import (
	// ... existing imports ...
	bundledskills "shelley.exe.dev/bundled_skills"
)
```

Replace the `discoverSkills` function (lines 311-337) with:

```go
func discoverSkills(workingDir, gitRoot string) []skills.Skill {
	// Start with default directories (user-level skills)
	dirs := skills.DefaultDirs()

	// Add .skills directories found in the project tree
	dirs = append(dirs, skills.ProjectSkillsDirs(workingDir, gitRoot)...)

	// Discover skills from all directories
	foundSkills := skills.Discover(dirs)

	// Also discover skills anywhere in the project tree
	treeSkills := skills.DiscoverInTree(workingDir, gitRoot)

	// Merge, avoiding duplicates by path
	seen := make(map[string]bool)
	for _, s := range foundSkills {
		seen[s.Path] = true
	}
	for _, s := range treeSkills {
		if !seen[s.Path] {
			foundSkills = append(foundSkills, s)
			seen[s.Path] = true
		}
	}

	// Append bundled skills at lowest priority, skipping any whose name
	// was already found (user/project skills override bundled ones).
	seenNames := make(map[string]bool)
	for _, s := range foundSkills {
		seenNames[s.Name] = true
	}
	if bundled, err := bundledskills.EmbeddedSkills(); err == nil {
		for _, s := range bundled {
			if !seenNames[s.Name] {
				foundSkills = append(foundSkills, s)
				seenNames[s.Name] = true
			}
		}
	}

	return foundSkills
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /Users/toddgruben/Projects/shelley && go test ./server/ -run "TestSystemPrompt" -v`
Expected: PASS — all system prompt tests pass including new ones.

**Step 5: Run all existing tests to check for regressions**

Run: `cd /Users/toddgruben/Projects/shelley && go test ./server/ ./bundled_skills/ ./skills/ ./claudetool/`
Expected: PASS

**Step 6: Commit**

```bash
git add server/system_prompt.go server/system_prompt_test.go
git commit -m "server: integrate bundled skills into skill discovery

Bundled skills (claude-code, opencode, gemini-cli) are now appended
at lowest priority. User or project skills with the same name override
the bundled versions."
```

---

### Task 6: Run full test suite and verify build

**Step 1: Build the UI (required for Go tests)**

Run: `cd /Users/toddgruben/Projects/shelley && make ui`

**Step 2: Run all Go tests**

Run: `cd /Users/toddgruben/Projects/shelley && make test-go`
Expected: PASS

**Step 3: Build the full binary**

Run: `cd /Users/toddgruben/Projects/shelley && make build`
Expected: Binary built at `bin/shelley` with embedded skills.

**Step 4: Verify skills appear in a test run**

Run: `cd /tmp && /Users/toddgruben/Projects/shelley/bin/shelley --model predictable serve --port 8099 &`
Then check the system prompt includes the skills by starting a conversation.

**Step 5: Commit any fixes if needed, then final commit**

```bash
git add -A
git commit -m "bundled_skills: verify full build with embedded AI coding agent skills"
```
