package server

import (
	"context"
	"strings"
	"testing"

	"github.com/tgruben-circuit/percy/db"
)

func TestExtractSnippet(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		kw          string
		wantSnippet string // exact, when deterministic; empty means skip exact check
		wantRanges  int    // number of ranges expected
	}{
		{
			name:       "match in middle adds ellipses both sides",
			text:       strings.Repeat("a", 200) + "needle" + strings.Repeat("b", 200),
			kw:         "needle",
			wantRanges: 1,
		},
		{
			name:        "match at start",
			text:        "hello world",
			kw:          "hello",
			wantSnippet: "hello world",
			wantRanges:  1,
		},
		{
			name:        "case insensitive",
			text:        "Hello World",
			kw:          "world",
			wantSnippet: "Hello World",
			wantRanges:  1,
		},
		{
			name:        "not found returns head text with nil ranges",
			text:        "the quick brown fox",
			kw:          "missing",
			wantSnippet: "the quick brown fox",
			wantRanges:  0,
		},
		{
			name:        "empty keyword returns head with nil ranges",
			text:        "the quick brown fox",
			kw:          "",
			wantSnippet: "the quick brown fox",
			wantRanges:  0,
		},
		{
			name:       "multibyte runes slice back to match",
			text:       "😀😀 needle here",
			kw:         "needle",
			wantRanges: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snippet, ranges := extractSnippet(tc.text, tc.kw)

			if len(ranges) != tc.wantRanges {
				t.Fatalf("got %d ranges, want %d (snippet=%q)", len(ranges), tc.wantRanges, snippet)
			}
			if tc.wantSnippet != "" && snippet != tc.wantSnippet {
				t.Fatalf("snippet = %q, want %q", snippet, tc.wantSnippet)
			}

			// Each range must slice back to a string fold-equal to the keyword.
			sr := []rune(snippet)
			for _, r := range ranges {
				if r[0] < 0 || r[1] > len(sr) || r[0] > r[1] {
					t.Fatalf("range %v out of bounds for snippet of %d runes", r, len(sr))
				}
				got := string(sr[r[0]:r[1]])
				if !strings.EqualFold(got, tc.kw) {
					t.Fatalf("range %v sliced to %q, want fold-equal to %q", r, got, tc.kw)
				}
			}
		})
	}

	// Specific offset checks per the plan.
	t.Run("match at start has range 0..5", func(t *testing.T) {
		_, ranges := extractSnippet("hello world", "hello")
		if len(ranges) != 1 || ranges[0] != [2]int{0, 5} {
			t.Fatalf("ranges = %v, want [[0 5]]", ranges)
		}
	})
	t.Run("case-insensitive has range 6..11", func(t *testing.T) {
		_, ranges := extractSnippet("Hello World", "world")
		if len(ranges) != 1 || ranges[0] != [2]int{6, 11} {
			t.Fatalf("ranges = %v, want [[6 11]]", ranges)
		}
	})
	t.Run("middle match snippet has leading and trailing ellipsis", func(t *testing.T) {
		snippet, _ := extractSnippet(strings.Repeat("a", 200)+"needle"+strings.Repeat("b", 200), "needle")
		if !strings.HasPrefix(snippet, "…") || !strings.HasSuffix(snippet, "…") {
			t.Fatalf("snippet = %q, want leading and trailing ellipsis", snippet)
		}
		if !strings.Contains(snippet, "needle") {
			t.Fatalf("snippet = %q, want to contain needle", snippet)
		}
	})
}

func TestMatchMessageText(t *testing.T) {
	ud := `{"text":"please refactor the WIDGET module"}`
	if got := matchMessageText("user", &ud, nil); got != "please refactor the WIDGET module" {
		t.Fatalf("user text = %q", got)
	}

	// ContentTypeText marshals as 2 (see llm.ContentType iota: tool_use=0, text=2).
	llmData := `{"Role":0,"Content":[{"Type":2,"Text":"first"},{"Type":2,"Text":"second"}]}`
	if got := matchMessageText("agent", nil, &llmData); got != "first\nsecond" {
		t.Fatalf("agent text = %q", got)
	}

	if got := matchMessageText("user", nil, nil); got != "" {
		t.Fatalf("nil data = %q, want empty", got)
	}
}

func TestSearchMessageHits(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	slug := "find-me"
	conv, err := database.CreateConversation(ctx, &slug, true, nil, nil)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeUser,
		UserData:       map[string]any{"text": "please refactor the WIDGET module"},
	}); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	hits, err := database.SearchMessageHits(ctx, "widget", 50, 0)
	if err != nil {
		t.Fatalf("SearchMessageHits: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if hits[0].ConversationID != conv.ConversationID {
		t.Fatalf("ConversationID = %q, want %q", hits[0].ConversationID, conv.ConversationID)
	}
	if hits[0].MatchMessageID == "" {
		t.Fatal("MatchMessageID is empty")
	}
}
