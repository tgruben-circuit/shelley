# Design: Bundled AI Coding Agent Skills

**Date:** 2026-02-14
**Status:** Draft

## Summary

Add 3 embedded skills to Shelley that let the agent launch Claude Code, OpenCode, and Gemini CLI for one-shot tasks, and generate project configuration files for each tool. Skills are embedded in the binary via `//go:embed` and available in every conversation.

## Motivation

Shelley is a coding agent that works on projects. Those projects often need to be configured for other AI coding tools (Claude Code, OpenCode, Gemini CLI). Shelley should be able to:

1. **Run these tools** as one-shot delegates for specific tasks
2. **Generate config files** so projects are ready for direct use with each tool

## Architecture

### Embedded Skills via `//go:embed`

New directory in the repo:

```
bundled_skills/
├── embed.go              # //go:embed + EmbeddedSkills() function
├── claude-code/
│   └── SKILL.md
├── opencode/
│   └── SKILL.md
└── gemini-cli/
    └── SKILL.md
```

`embed.go` uses Go's `embed.FS` to bundle all skill directories. `EmbeddedSkills()` writes them to a temp directory (created once per process), parses them with `skills.Parse()`, and returns `[]skills.Skill`.

`server/system_prompt.go:discoverSkills()` is modified to append embedded skills at lowest priority.

### Skill Discovery Priority (highest to lowest)

1. Project `.skills/` directories (team-shared overrides)
2. Project tree SKILL.md files (in-tree skills)
3. User `~/.config/shelley/` and `~/.config/agents/skills` (personal skills)
4. **Bundled embedded skills** (new, lowest priority)

A user or project skill with the same name as a bundled skill overrides it. Deduplication is by skill name (first wins).

## Skills

### Skill 1: `claude-code`

**Description:** Run Claude Code CLI for one-shot tasks and generate CLAUDE.md project configuration.

**Launcher:**
- Command: `claude -p "task"`
- Key flags: `--model`, `--max-turns N`, `--output-format text|json`, `--allowedTools`, `--dangerously-skip-permissions`
- Stdin piping: `cat file | claude -p "explain"`
- Budget control: `--max-budget-usd N`

**Config generation:**
- `CLAUDE.md` at project root — build commands, architecture, conventions
- `.claude/settings.json` — permissions, allowed tools, model preferences
- `.claude/CLAUDE.local.md` — personal overrides (gitignored)

### Skill 2: `opencode`

**Description:** Run OpenCode CLI for one-shot tasks and generate opencode.json project configuration.

**Launcher:**
- Command: `opencode run "task"`
- Key flags: `-m provider/model`, `--format default|json`, `--file path`
- All permissions auto-approved in run mode

**Config generation:**
- `opencode.json` at project root — model, tools, permissions, compaction
- `AGENTS.md` at project root — project instructions (OpenCode's primary context file)
- `instructions` array for including additional files/globs

### Skill 3: `gemini-cli`

**Description:** Run Gemini CLI for one-shot tasks and generate GEMINI.md project configuration.

**Launcher:**
- Command: `gemini -p "task"`
- Key flags: `-m model`, `--output-format text|json`, `--yolo`
- Stdin piping: `cat file | gemini -p "summarize"`

**Config generation:**
- `GEMINI.md` at project root — project instructions, supports `@./path` imports
- `.gemini/settings.json` — model, context settings, tool config
- Hierarchical GEMINI.md in subdirectories for component-specific context

### Common Patterns Across All Skills

Each SKILL.md follows the same structure:
1. YAML frontmatter (name, description, allowed-tools)
2. Quick reference table of CLI flags
3. One-shot execution examples
4. Config file generation guidance with schemas/examples
5. Tmux workaround for long-running tasks (see Phase 2 note)

## Code Changes

| File | Change |
|------|--------|
| `bundled_skills/embed.go` | **New** — `//go:embed` directive, `EmbeddedSkills()` |
| `bundled_skills/claude-code/SKILL.md` | **New** — Claude Code skill content |
| `bundled_skills/opencode/SKILL.md` | **New** — OpenCode skill content |
| `bundled_skills/gemini-cli/SKILL.md` | **New** — Gemini CLI skill content |
| `server/system_prompt.go` | Modify `discoverSkills()` to append embedded skills |
| `bundled_skills/embed_test.go` | **New** — tests for parsing and discovery |

## Out of Scope

- **Interactive/PTY sessions** — Shelley's bash tool is foreground-only
- **Background process monitoring** — Planned for Phase 2
- **CLI tool installation** — User responsibility
- **API key management** — Each tool handles its own auth

## Phase 2: Process Monitoring (Future)

A follow-up feature that would enable background process management:

- Extend bash tool with `background: true` parameter
- Add session registry (per-conversation process tracking)
- New `process` tool with `list`, `poll`, `log`, `kill` actions
- Output streaming and storage for long-running processes
- Optional PTY support for interactive terminal applications
- Cleanup lifecycle for orphaned processes

Once Phase 2 ships, the skills would be updated to include background execution patterns alongside the one-shot commands.

## Testing

- `bundled_skills/embed_test.go`: Verify all 3 skills parse correctly from embedded FS
- `bundled_skills/embed_test.go`: Verify `EmbeddedSkills()` returns valid skills with correct names
- `server/system_prompt_test.go`: Verify embedded skills appear in `discoverSkills()` output
- Integration: Verify user/project skills override embedded skills by name
- Existing `skill_load` tests continue to pass unchanged

## Reference: CLI Comparison

| Feature | Claude Code | OpenCode | Gemini CLI |
|---------|-------------|----------|------------|
| One-shot command | `claude -p "prompt"` | `opencode run "prompt"` | `gemini -p "prompt"` |
| Output formats | text, json, stream-json | default, json | text, json |
| Model override | `--model` | `-m provider/model` | `-m model` |
| Auto-approve | `--allowedTools` | Auto in run mode | `--yolo` |
| Turn limit | `--max-turns N` | — | — |
| Budget limit | `--max-budget-usd N` | — | — |
| Project context | CLAUDE.md | AGENTS.md | GEMINI.md |
| Project config | .claude/settings.json | opencode.json | .gemini/settings.json |
| Stdin piping | Yes | — | Yes |
