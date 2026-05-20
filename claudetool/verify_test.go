package claudetool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeVerifier(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".percy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	script := filepath.Join(dir, "verify")
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestFindVerifier_WalksUp(t *testing.T) {
	root := t.TempDir()
	writeVerifier(t, root, "#!/bin/sh\nexit 0\n")
	sub := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, gotRoot := findVerifier(sub)
	if got == "" {
		t.Fatal("expected to find verifier")
	}
	if gotRoot != root {
		t.Errorf("root = %q, want %q", gotRoot, root)
	}
}

func TestFindVerifier_NotExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm bits differ on windows")
	}
	root := t.TempDir()
	dir := filepath.Join(root, ".percy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "verify"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ := findVerifier(root)
	if got != "" {
		t.Errorf("expected no verifier for non-exec file, got %q", got)
	}
}

func TestRunVerifier_PassAndFail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script not portable")
	}
	root := t.TempDir()
	writeVerifier(t, root, "#!/bin/sh\necho hello-verifier\nexit 0\n")
	out := runVerifier(context.Background(), root)
	if !strings.Contains(out, "hello-verifier") {
		t.Errorf("missing stdout: %q", out)
	}
	if !strings.Contains(out, `status="PASS"`) {
		t.Errorf("expected PASS: %q", out)
	}

	writeVerifier(t, root, "#!/bin/sh\necho boom 1>&2\nexit 3\n")
	out = runVerifier(context.Background(), root)
	if !strings.Contains(out, "boom") {
		t.Errorf("missing stderr: %q", out)
	}
	if !strings.Contains(out, "FAIL (exit 3)") {
		t.Errorf("expected FAIL: %q", out)
	}
}

func TestRunVerifier_NoneFound(t *testing.T) {
	root := t.TempDir()
	if got := runVerifier(context.Background(), root); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestPatchTool_AppendsVerifierOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script not portable")
	}
	root := t.TempDir()
	writeVerifier(t, root, "#!/bin/sh\necho VERIFIED_OK\nexit 0\n")
	wd := NewMutableWorkingDir(root)
	pt := &PatchTool{WorkingDir: wd, Callback: appendVerifierToOutput(wd)}

	input := PatchInput{
		Path: filepath.Join(root, "hello.txt"),
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "hi\n",
		}},
	}
	msg, _ := json.Marshal(input)
	out := pt.Run(context.Background(), msg)
	if out.Error != nil {
		t.Fatalf("patch error: %v", out.Error)
	}
	// Last content block should contain the verifier output.
	if len(out.LLMContent) == 0 {
		t.Fatal("no llm content")
	}
	last := out.LLMContent[len(out.LLMContent)-1].Text
	if !strings.Contains(last, "VERIFIED_OK") || !strings.Contains(last, `status="PASS"`) {
		t.Errorf("verifier output not appended: %q", last)
	}
}

func TestPatchTool_SkipsVerifierOnPatchError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script not portable")
	}
	root := t.TempDir()
	writeVerifier(t, root, "#!/bin/sh\necho VERIFIED_OK\n")
	wd := NewMutableWorkingDir(root)
	pt := &PatchTool{WorkingDir: wd, Callback: appendVerifierToOutput(wd)}

	// Replace on a file that doesn't exist -> patch error.
	input := PatchInput{
		Path: filepath.Join(root, "missing.txt"),
		Patches: []PatchRequest{{
			Operation: "replace",
			OldText:   "a",
			NewText:   "b",
		}},
	}
	msg, _ := json.Marshal(input)
	out := pt.Run(context.Background(), msg)
	if out.Error == nil {
		t.Fatal("expected patch error")
	}
	for _, c := range out.LLMContent {
		if strings.Contains(c.Text, "VERIFIED_OK") {
			t.Error("verifier should not run on patch error")
		}
	}
}
