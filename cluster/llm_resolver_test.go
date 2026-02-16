package cluster

import (
	"context"
	"testing"

	"github.com/tgruben-circuit/percy/llm"
)

type mockLLMService struct{ response string }

func (m *mockLLMService) Do(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return &llm.Response{
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: m.response}},
	}, nil
}

func (m *mockLLMService) TokenContextWindow() int { return 100000 }
func (m *mockLLMService) MaxImageDimension() int  { return 0 }

func TestLLMConflictResolver(t *testing.T) {
	svc := &mockLLMService{response: "resolved content here"}
	resolver := NewLLMConflictResolver(svc)

	ctx := context.Background()
	got, err := resolver(ctx, "file.go", "ours code", "theirs code", "base code", "fix bug", "Fix the login bug")
	if err != nil {
		t.Fatal(err)
	}
	if got != "resolved content here" {
		t.Fatalf("expected 'resolved content here', got %q", got)
	}
}
