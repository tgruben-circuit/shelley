package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileTool(t *testing.T) {
	tmpDir := t.TempDir()
	wd := NewMutableWorkingDir(tmpDir)
	tool := &ReadFileTool{WorkingDir: wd}
	ctx := context.Background()

	run := func(input readFileInput) (string, error) {
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		result := tool.Run(ctx, data)
		if result.Error != nil {
			return "", result.Error
		}
		if len(result.LLMContent) == 0 {
			return "", nil
		}
		return result.LLMContent[0].Text, nil
	}

	writeFile := func(name, content string) string {
		p := filepath.Join(tmpDir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("basic read", func(t *testing.T) {
		path := writeFile("hello.txt", "line one\nline two\nline three\n")
		out, err := run(readFileInput{Path: path})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "3 total lines") {
			t.Errorf("expected header with 3 total lines, got:\n%s", out)
		}
		if !strings.Contains(out, "1\tline one") {
			t.Errorf("expected line 1, got:\n%s", out)
		}
		if !strings.Contains(out, "3\tline three") {
			t.Errorf("expected line 3, got:\n%s", out)
		}
	})

	t.Run("pagination offset and limit", func(t *testing.T) {
		path := writeFile("five.txt", "a\nb\nc\nd\ne\n")
		out, err := run(readFileInput{Path: path, Offset: 3, Limit: 2})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "showing 3-4") {
			t.Errorf("expected showing 3-4, got:\n%s", out)
		}
		if !strings.Contains(out, "3\tc") {
			t.Errorf("expected line 3 = c, got:\n%s", out)
		}
		if !strings.Contains(out, "4\td") {
			t.Errorf("expected line 4 = d, got:\n%s", out)
		}
		// Should not contain lines outside the range.
		if strings.Contains(out, "5\te") {
			t.Errorf("should not contain line 5, got:\n%s", out)
		}
	})

	t.Run("binary rejection", func(t *testing.T) {
		p := filepath.Join(tmpDir, "binary.bin")
		if err := os.WriteFile(p, []byte("hello\x00world"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := run(readFileInput{Path: p})
		if err == nil {
			t.Fatal("expected error for binary file")
		}
		if !strings.Contains(err.Error(), "binary") {
			t.Errorf("expected binary error, got: %v", err)
		}
	})

	t.Run("directory rejection", func(t *testing.T) {
		_, err := run(readFileInput{Path: tmpDir})
		if err == nil {
			t.Fatal("expected error for directory")
		}
		if !strings.Contains(err.Error(), "directory") {
			t.Errorf("expected directory error, got: %v", err)
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, err := run(readFileInput{Path: filepath.Join(tmpDir, "nope.txt")})
		if err == nil {
			t.Fatal("expected error for non-existent file")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected not found error, got: %v", err)
		}
	})

	t.Run("relative path", func(t *testing.T) {
		writeFile("rel.txt", "relative content\n")
		out, err := run(readFileInput{Path: "rel.txt"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "relative content") {
			t.Errorf("expected file content, got:\n%s", out)
		}
	})

	t.Run("offset beyond end", func(t *testing.T) {
		writeFile("short.txt", "one\ntwo\n")
		_, err := run(readFileInput{Path: filepath.Join(tmpDir, "short.txt"), Offset: 100})
		if err == nil {
			t.Fatal("expected error for offset beyond end")
		}
		if !strings.Contains(err.Error(), "beyond end") {
			t.Errorf("expected beyond end error, got: %v", err)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		writeFile("empty.txt", "")
		out, err := run(readFileInput{Path: filepath.Join(tmpDir, "empty.txt")})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "0 lines") {
			t.Errorf("expected 0 lines header, got:\n%s", out)
		}
	})

	t.Run("truncation with default limit", func(t *testing.T) {
		var lines []string
		for i := 1; i <= 1500; i++ {
			lines = append(lines, fmt.Sprintf("line %d", i))
		}
		writeFile("big.txt", strings.Join(lines, "\n")+"\n")
		out, err := run(readFileInput{Path: filepath.Join(tmpDir, "big.txt")})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "showing 1-1000") {
			t.Errorf("expected showing 1-1000, got:\n%s", out[:200])
		}
		if !strings.Contains(out, "truncated") {
			t.Errorf("expected truncation note, got tail:\n%s", out[len(out)-100:])
		}
		if !strings.Contains(out, "500 lines remaining") {
			t.Errorf("expected 500 lines remaining, got tail:\n%s", out[len(out)-100:])
		}
	})

	t.Run("large offset near end", func(t *testing.T) {
		var lines []string
		for i := 1; i <= 50; i++ {
			lines = append(lines, fmt.Sprintf("line %d", i))
		}
		writeFile("fifty.txt", strings.Join(lines, "\n")+"\n")
		out, err := run(readFileInput{Path: filepath.Join(tmpDir, "fifty.txt"), Offset: 48})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "showing 48-50") {
			t.Errorf("expected showing 48-50, got:\n%s", out)
		}
		if !strings.Contains(out, "48\tline 48") {
			t.Errorf("expected line 48, got:\n%s", out)
		}
		if !strings.Contains(out, "50\tline 50") {
			t.Errorf("expected line 50, got:\n%s", out)
		}
	})
}

func TestReadFileTool_Method(t *testing.T) {
	wd := NewMutableWorkingDir("/test")
	tool := &ReadFileTool{WorkingDir: wd}
	llmTool := tool.Tool()

	if llmTool == nil {
		t.Fatal("Tool() returned nil")
	}
	if llmTool.Name != readFileName {
		t.Errorf("expected name %q, got %q", readFileName, llmTool.Name)
	}
	if llmTool.Run == nil {
		t.Error("Run function not set")
	}
}
