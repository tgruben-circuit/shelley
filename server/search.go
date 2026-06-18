package server

import (
	"encoding/json"
	"strings"

	"github.com/tgruben-circuit/percy/llm"
)

// snippetRadius is the number of runes of context shown on each side of a match.
const snippetRadius = 80

// extractSnippet returns a short excerpt of text centered on the first
// case-insensitive occurrence of kw, plus the [start,end) rune ranges of the
// match within the returned snippet. If kw is empty or absent, it returns a
// head-of-text excerpt with no ranges. Leading/trailing "…" each count as one
// rune in the returned offsets. This is the seam where SQLite FTS5 snippet()
// could later be substituted.
func extractSnippet(text, kw string) (string, [][2]int) {
	runes := []rune(text)

	byteIdx := -1
	if kw != "" {
		byteIdx = strings.Index(strings.ToLower(text), strings.ToLower(kw))
	}

	if byteIdx < 0 {
		end := len(runes)
		trailing := false
		if end > 2*snippetRadius {
			end = 2 * snippetRadius
			trailing = true
		}
		s := string(runes[:end])
		if trailing {
			s += "…"
		}
		return s, nil
	}

	matchStart := len([]rune(text[:byteIdx]))
	matchEnd := matchStart + len([]rune(kw))

	start := matchStart - snippetRadius
	if start < 0 {
		start = 0
	}
	end := matchEnd + snippetRadius
	if end > len(runes) {
		end = len(runes)
	}

	var b strings.Builder
	offset := 0
	if start > 0 {
		b.WriteRune('…')
		offset = 1
	}
	b.WriteString(string(runes[start:end]))
	if end < len(runes) {
		b.WriteRune('…')
	}

	ranges := [][2]int{{matchStart - start + offset, matchEnd - start + offset}}
	return b.String(), ranges
}

// matchMessageText returns the human-readable text of a matched message: the
// user_data ".text" field for user messages, or the joined text content of the
// llm_data llm.Message for agent messages. Returns "" if neither is parseable.
func matchMessageText(msgType string, userData, llmData *string) string {
	if msgType == "user" && userData != nil {
		var ud struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(*userData), &ud) == nil {
			return ud.Text
		}
		return ""
	}
	if llmData != nil {
		var lm llm.Message
		if json.Unmarshal([]byte(*llmData), &lm) == nil {
			var parts []string
			for _, c := range lm.Content {
				if c.Type == llm.ContentTypeText && c.Text != "" {
					parts = append(parts, c.Text)
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	return ""
}
