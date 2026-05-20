package claudetool

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/tgruben-circuit/percy/llm"
)

// verifyScriptRelPath is the conventional location for the project verifier.
const verifyScriptRelPath = ".percy/verify"

// verifyTimeout caps how long the verifier may run.
const verifyTimeout = 60 * time.Second

// findVerifier walks up from dir looking for an executable .percy/verify.
// Returns the absolute path of the executable and its containing project dir, or ("", "") if none.
func findVerifier(dir string) (script, root string) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", ""
	}
	for {
		candidate := filepath.Join(dir, verifyScriptRelPath)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ""
		}
		dir = parent
	}
}

// runVerifier executes .percy/verify (if any) reachable from workingDir.
// Returns a human-readable summary, or "" if no verifier was found.
func runVerifier(ctx context.Context, workingDir string) string {
	script, root := findVerifier(workingDir)
	if script == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, script)
	cmd.Dir = root
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start).Round(time.Millisecond)
	status := "PASS"
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			status = fmt.Sprintf("TIMEOUT after %s", verifyTimeout)
		} else if ee, ok := err.(*exec.ExitError); ok {
			status = fmt.Sprintf("FAIL (exit %d)", ee.ExitCode())
		} else {
			status = fmt.Sprintf("ERROR: %v", err)
		}
	}
	body := out.String()
	// Trim very large outputs so we don't blow the context window.
	const maxBytes = 8 * 1024
	truncated := ""
	if len(body) > maxBytes {
		body = body[:maxBytes]
		truncated = "\n[... output truncated ...]"
	}
	return fmt.Sprintf("<verifier script=%q status=%q duration=%q>\n%s%s</verifier>",
		filepath.Join(verifyScriptRelPath), status, dur.String(), body, truncated)
}

// appendVerifierToOutput is the PatchCallback that runs the verifier (if any)
// and appends its output to a successful patch result.
func appendVerifierToOutput(wd *MutableWorkingDir) PatchCallback {
	return func(_ PatchInput, out llm.ToolOut) llm.ToolOut {
		if out.Error != nil {
			return out
		}
		summary := runVerifier(context.Background(), wd.Get())
		if summary == "" {
			return out
		}
		out.LLMContent = append(out.LLMContent, llm.StringContent(summary))
		return out
	}
}
