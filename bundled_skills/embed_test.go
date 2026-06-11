package bundled_skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedSkillsReturnsAll(t *testing.T) {
	skills, err := EmbeddedSkills()
	if err != nil {
		t.Fatalf("EmbeddedSkills() error: %v", err)
	}
	if len(skills) != 22 {
		t.Fatalf("expected 22 skills, got %d", len(skills))
	}

	want := map[string]bool{
		"claude-code":                  false,
		"codex-cli":                    false,
		"co-brainstorm":               false,
		"co-plan":                     false,
		"co-validate":                 false,
		"opencode":                     false,
		"gemini-cli":                   false,
		"handoff":                      false,
		"brainstorming":                false,
		"dispatching-parallel-agents":  false,
		"executing-plans":              false,
		"finishing-a-development-branch": false,
		"receiving-code-review":        false,
		"requesting-code-review":       false,
		"subagent-driven-development":  false,
		"systematic-debugging":         false,
		"test-driven-development":      false,
		"using-git-worktrees":          false,
		"using-superpowers":            false,
		"verification-before-completion": false,
		"writing-plans":                false,
		"writing-skills":               false,
	}
	for _, s := range skills {
		if _, ok := want[s.Name]; !ok {
			t.Errorf("unexpected skill name: %q", s.Name)
			continue
		}
		want[s.Name] = true
	}
	for name, found := range want {
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
			t.Errorf("skill %q has empty Description", s.Name)
		}
		if s.Path == "" {
			t.Errorf("skill %q has empty Path", s.Name)
		}
	}
}

func TestEmbeddedSkillsIdempotent(t *testing.T) {
	first, err := EmbeddedSkills()
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}
	second, err := EmbeddedSkills()
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}

	if len(first) != len(second) {
		t.Fatalf("first call returned %d skills, second returned %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Name != second[i].Name {
			t.Errorf("skill %d: first=%q, second=%q", i, first[i].Name, second[i].Name)
		}
		if first[i].Description != second[i].Description {
			t.Errorf("skill %d description mismatch", i)
		}
		if first[i].Path != second[i].Path {
			t.Errorf("skill %d path mismatch: first=%q, second=%q", i, first[i].Path, second[i].Path)
		}
	}
}

func TestSupportingFilesExtracted(t *testing.T) {
	skills, err := EmbeddedSkills()
	if err != nil {
		t.Fatalf("EmbeddedSkills() error: %v", err)
	}

	// Find subagent-driven-development and check its supporting files exist.
	for _, s := range skills {
		if s.Name != "subagent-driven-development" {
			continue
		}
		dir := filepath.Dir(s.Path)
		for _, f := range []string{"implementer-prompt.md", "spec-reviewer-prompt.md", "code-quality-reviewer-prompt.md"} {
			path := filepath.Join(dir, f)
			if _, err := os.Stat(path); err != nil {
				t.Errorf("supporting file %s not extracted: %v", f, err)
			}
		}
		return
	}
	t.Fatal("subagent-driven-development skill not found")
}
