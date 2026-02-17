package claudetool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tgruben-circuit/percy/llm"
)

// ReadFileTool reads a file and returns its contents with line numbers.
type ReadFileTool struct {
	WorkingDir *MutableWorkingDir
}

const (
	readFileName        = "read_file"
	readFileDescription = `Read the contents of a file with line numbers.

Returns file contents formatted with line numbers (like cat -n). Supports pagination
via offset and limit parameters for large files.

Use this instead of bash cat/head/tail for reading files — it provides line numbers,
binary detection, and pagination without shell overhead.`

	readFileInputSchema = `{
  "type": "object",
  "required": ["path"],
  "properties": {
    "path": {
      "type": "string",
      "description": "File path to read (absolute or relative to working directory)"
    },
    "offset": {
      "type": "integer",
      "description": "1-based starting line number (default: 1)"
    },
    "limit": {
      "type": "integer",
      "description": "Maximum number of lines to return (default: 1000, max: 10000)"
    }
  }
}`

	readFileDefaultLimit = 1000
	readFileMaxLimit     = 10000
)

type readFileInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// Tool returns an llm.Tool for reading files.
func (r *ReadFileTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        readFileName,
		Description: readFileDescription,
		InputSchema: llm.MustSchema(readFileInputSchema),
		Run:         r.Run,
	}
}

// isBinary reports whether data contains null bytes in the first 512 bytes,
// indicating a binary file.
func isBinary(data []byte) bool {
	check := data
	if len(check) > 512 {
		check = check[:512]
	}
	return bytes.ContainsRune(check, 0)
}

// Run executes the read_file tool.
func (r *ReadFileTool) Run(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var req readFileInput
	if err := json.Unmarshal(m, &req); err != nil {
		return llm.ErrorfToolOut("failed to parse read_file input: %w", err)
	}

	if req.Path == "" {
		return llm.ErrorfToolOut("path is required")
	}

	// Resolve relative paths against working directory.
	path := req.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.WorkingDir.Get(), path)
	}
	path = filepath.Clean(path)

	// Stat the file.
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return llm.ErrorfToolOut("file not found: %s", path)
		}
		return llm.ErrorfToolOut("failed to stat file: %w", err)
	}
	if info.IsDir() {
		return llm.ErrorfToolOut("path is a directory, not a file: %s", path)
	}

	// Read the file.
	data, err := os.ReadFile(path)
	if err != nil {
		return llm.ErrorfToolOut("failed to read file: %w", err)
	}

	// Binary detection.
	if isBinary(data) {
		return llm.ErrorfToolOut("file appears to be binary: %s", path)
	}

	// Split into lines.
	content := string(data)
	// Remove trailing newline to avoid an extra empty line at the end.
	content = strings.TrimRight(content, "\n")
	var lines []string
	if len(content) == 0 {
		lines = nil
	} else {
		lines = strings.Split(content, "\n")
	}
	totalLines := len(lines)

	// Apply defaults.
	offset := req.Offset
	if offset <= 0 {
		offset = 1
	}
	limit := req.Limit
	if limit <= 0 {
		limit = readFileDefaultLimit
	}
	if limit > readFileMaxLimit {
		limit = readFileMaxLimit
	}

	// Validate offset.
	if totalLines > 0 && offset > totalLines {
		return llm.ErrorfToolOut("offset %d is beyond end of file (%d lines)", offset, totalLines)
	}

	// Slice lines (offset is 1-based).
	startIdx := offset - 1
	endIdx := startIdx + limit
	truncated := false
	if endIdx > totalLines {
		endIdx = totalLines
	} else if endIdx < totalLines {
		truncated = true
	}
	visibleLines := lines[startIdx:endIdx]

	// Format output with line numbers.
	var buf strings.Builder
	showStart := offset
	showEnd := startIdx + len(visibleLines)
	if totalLines == 0 {
		fmt.Fprintf(&buf, "File: %s (0 lines)\n", path)
	} else {
		fmt.Fprintf(&buf, "File: %s (%d total lines, showing %d-%d)\n", path, totalLines, showStart, showEnd)
	}

	for i, line := range visibleLines {
		lineNum := offset + i
		fmt.Fprintf(&buf, "%6d\t%s\n", lineNum, line)
	}

	if truncated {
		fmt.Fprintf(&buf, "\n... truncated (%d lines remaining)", totalLines-endIdx)
	}

	return llm.ToolOut{
		LLMContent: llm.TextContent(buf.String()),
	}
}
