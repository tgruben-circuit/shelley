package memory

import (
	"strings"
	"testing"
)

func containsString(s, substr string) bool {
	return strings.Contains(s, substr)
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"empty", "", 0},
		{"short", "hello", 2},                                   // 5 chars / 4 = 1.25 -> 2
		{"exact multiple", "abcdefgh", 2},                       // 8 chars / 4 = 2
		{"longer", "this is a test string with some words", 10}, // 37 chars / 4 = 9.25 -> 10
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateTokens(tt.input)
			if got != tt.want {
				t.Errorf("EstimateTokens(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestChunkMessagesShortFitsInOneChunk(t *testing.T) {
	msgs := []MessageText{
		{Role: "user", Text: "Hello there"},
		{Role: "agent", Text: "Hi! How can I help?"},
	}
	chunks := ChunkMessages(msgs, 1000)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Index != 0 {
		t.Errorf("expected Index 0, got %d", chunks[0].Index)
	}
	if !containsString(chunks[0].Text, "User: Hello there") {
		t.Errorf("expected role prefix, got %q", chunks[0].Text)
	}
	if !containsString(chunks[0].Text, "Agent: Hi! How can I help?") {
		t.Errorf("expected role prefix, got %q", chunks[0].Text)
	}
	if chunks[0].TokenCount <= 0 {
		t.Error("expected positive token count")
	}
}

func TestChunkMessagesLongSplitsIntoMultiple(t *testing.T) {
	// Each message is ~25 chars -> ~7 tokens with prefix.
	// With maxTokens=10, each message should end up in its own chunk.
	msgs := []MessageText{
		{Role: "user", Text: "Tell me a long story about dragons and knights"},
		{Role: "agent", Text: "Once upon a time in a land far far away there lived a dragon"},
	}
	chunks := ChunkMessages(msgs, 10)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if c.Index != i {
			t.Errorf("chunk %d: expected Index %d, got %d", i, i, c.Index)
		}
	}
}

func TestChunkMarkdown(t *testing.T) {
	md := `# Introduction

This is an introduction paragraph.

## Details

Here are the details of the project.

## Conclusion

The final thoughts on the matter.
`
	chunks := ChunkMarkdown(md, 1000)
	if len(chunks) < 1 {
		t.Fatal("expected at least 1 chunk")
	}
	// With a large token limit, all content should fit
	combined := ""
	for _, c := range chunks {
		combined += c.Text
	}
	if !containsString(combined, "Introduction") {
		t.Error("missing Introduction in chunks")
	}
	if !containsString(combined, "Details") {
		t.Error("missing Details in chunks")
	}
	if !containsString(combined, "Conclusion") {
		t.Error("missing Conclusion in chunks")
	}
}

func TestChunkMarkdownSplits(t *testing.T) {
	md := `# Section One

This is some text in section one.

# Section Two

This is some text in section two.

# Section Three

This is some text in section three.
`
	// Use a small token limit to force splitting by heading
	chunks := ChunkMarkdown(md, 15)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks with small token limit, got %d", len(chunks))
	}
	for i, c := range chunks {
		if c.Index != i {
			t.Errorf("chunk %d: expected Index %d, got %d", i, i, c.Index)
		}
		if c.TokenCount <= 0 {
			t.Errorf("chunk %d: expected positive token count", i)
		}
	}
}

func TestRoleLabel(t *testing.T) {
	tests := []struct {
		role string
		want string
	}{
		{"user", "User"},
		{"agent", "Agent"},
		{"assistant", "Agent"},
		{"system", "System"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			got := roleLabel(tt.role)
			if got != tt.want {
				t.Errorf("roleLabel(%q) = %q, want %q", tt.role, got, tt.want)
			}
		})
	}
}
