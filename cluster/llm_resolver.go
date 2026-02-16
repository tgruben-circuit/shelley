package cluster

import (
	"context"
	"fmt"
	"time"

	"github.com/tgruben-circuit/percy/llm"
)

// NewLLMConflictResolver returns a ConflictResolver that uses an LLM to resolve
// merge conflicts by providing the base, ours, and theirs versions.
func NewLLMConflictResolver(service llm.Service) ConflictResolver {
	return func(ctx context.Context, path, ours, theirs, base, taskTitle, taskDesc string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()

		prompt := fmt.Sprintf(`Resolve this merge conflict for %s.

The worker was doing: %s: %s

BASE version (before either change):
%s

OURS version (working branch):
%s

THEIRS version (worker's change):
%s

Output ONLY the resolved file content. No explanation, no markdown fences, no line numbers. Just the raw file content.`,
			path, taskTitle, taskDesc, base, ours, theirs)

		resp, err := service.Do(ctx, &llm.Request{
			Messages: []llm.Message{
				{
					Role:    llm.MessageRoleUser,
					Content: []llm.Content{{Type: llm.ContentTypeText, Text: prompt}},
				},
			},
		})
		if err != nil {
			return "", fmt.Errorf("llm resolve %q: %w", path, err)
		}

		for _, c := range resp.Content {
			if c.Type == llm.ContentTypeText {
				return c.Text, nil
			}
		}

		return "", fmt.Errorf("llm resolve %q: no text in response", path)
	}
}
