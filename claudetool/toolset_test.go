package claudetool

import (
	"context"
	"testing"
)

func TestIsStrongModel(t *testing.T) {
	tests := []struct {
		modelID  string
		expected bool
	}{
		{"claude-3-sonnet-20240229", true},
		{"claude-3-opus-20240229", true},
		{"claude-3-haiku-20240307", false},
		{"Sonnet Model", true},
		{"OPUS Model", true},
		{"haiku model", false},
		{"other-model", false},
		{"", false},
	}

	for _, test := range tests {
		result := isStrongModel(test.modelID)
		if result != test.expected {
			t.Errorf("isStrongModel(%q) = %v, expected %v", test.modelID, result, test.expected)
		}
	}
}

func TestNewToolSet(t *testing.T) {
	provider := &mockLLMProvider{}

	cfg := ToolSetConfig{
		LLMProvider: provider,
		ModelID:     "test-model",
		WorkingDir:  "/test",
	}

	ctx := context.Background()
	ts := NewToolSet(ctx, cfg)

	if ts == nil {
		t.Fatal("NewToolSet returned nil")
	}

	if ts.wd == nil {
		t.Error("Working directory not initialized")
	}

	if ts.tools == nil {
		t.Error("Tools not initialized")
	}
}

func TestToolSet_Tools(t *testing.T) {
	provider := &mockLLMProvider{}

	cfg := ToolSetConfig{
		LLMProvider: provider,
		ModelID:     "test-model",
		WorkingDir:  "/test",
	}

	ctx := context.Background()
	ts := NewToolSet(ctx, cfg)

	tools := ts.AllTools()
	if tools == nil {
		t.Fatal("Tools() returned nil")
	}

	if len(tools) == 0 {
		t.Error("expected at least one tool")
	}
}

func TestToolSet_WorkingDir(t *testing.T) {
	provider := &mockLLMProvider{}

	cfg := ToolSetConfig{
		LLMProvider: provider,
		ModelID:     "test-model",
		WorkingDir:  "/test",
	}

	ctx := context.Background()
	ts := NewToolSet(ctx, cfg)

	wd := ts.WorkingDir()
	if wd == nil {
		t.Fatal("WorkingDir() returned nil")
	}

	if wd.Get() != "/test" {
		t.Errorf("expected working dir '/test', got %q", wd.Get())
	}
}

func TestToolSet_Cleanup(t *testing.T) {
	provider := &mockLLMProvider{}

	cfg := ToolSetConfig{
		LLMProvider: provider,
		ModelID:     "test-model",
		WorkingDir:  "/test",
	}

	ctx := context.Background()
	ts := NewToolSet(ctx, cfg)

	// Cleanup should not panic
	ts.Cleanup()
}

func TestNewToolSet_DefaultWorkingDir(t *testing.T) {
	provider := &mockLLMProvider{}

	// Test with empty working dir (should default to "/")
	cfg := ToolSetConfig{
		LLMProvider: provider,
		ModelID:     "test-model",
		WorkingDir:  "",
	}

	ctx := context.Background()
	ts := NewToolSet(ctx, cfg)

	wd := ts.WorkingDir()
	if wd.Get() != "/" {
		t.Errorf("expected default working dir '/', got %q", wd.Get())
	}
}

func TestNewToolSet_WithBrowser(t *testing.T) {
	provider := &mockLLMProvider{}

	cfg := ToolSetConfig{
		LLMProvider:   provider,
		ModelID:       "test-model",
		WorkingDir:    "/test",
		EnableBrowser: true,
	}

	ctx := context.Background()
	ts := NewToolSet(ctx, cfg)

	if ts == nil {
		t.Fatal("NewToolSet returned nil")
	}

	if ts.wd == nil {
		t.Error("Working directory not initialized")
	}

	if ts.tools == nil {
		t.Error("Tools not initialized")
	}
}

func TestNewToolSet_SubagentDepthLimit(t *testing.T) {
	provider := &mockLLMProvider{}
	db := newMockSubagentDB()
	runner := &mockSubagentRunner{response: "ok"}

	hasSubagentTool := func(ts *ToolSet) bool {
		for _, tool := range ts.AllTools() {
			if tool.Name == "subagent" {
				return true
			}
		}
		return false
	}

	// Depth 0, MaxDepth 1 -> should have subagent tool
	t.Run("depth 0 max 1 has subagent", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider:          provider,
			ModelID:              "test-model",
			WorkingDir:           "/test",
			SubagentRunner:       runner,
			SubagentDB:           db,
			ParentConversationID: "parent-123",
			SubagentDepth:        0,
			MaxSubagentDepth:     1,
		}
		ts := NewToolSet(context.Background(), cfg)
		if !hasSubagentTool(ts) {
			t.Error("expected subagent tool at depth 0 with max 1")
		}
	})

	// Depth 1, MaxDepth 1 -> should NOT have subagent tool
	t.Run("depth 1 max 1 no subagent", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider:          provider,
			ModelID:              "test-model",
			WorkingDir:           "/test",
			SubagentRunner:       runner,
			SubagentDB:           db,
			ParentConversationID: "parent-123",
			SubagentDepth:        1,
			MaxSubagentDepth:     1,
		}
		ts := NewToolSet(context.Background(), cfg)
		if hasSubagentTool(ts) {
			t.Error("expected no subagent tool at depth 1 with max 1")
		}
	})

	// Depth 0, MaxDepth 0 (unlimited) -> should have subagent tool
	t.Run("depth 0 max 0 unlimited has subagent", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider:          provider,
			ModelID:              "test-model",
			WorkingDir:           "/test",
			SubagentRunner:       runner,
			SubagentDB:           db,
			ParentConversationID: "parent-123",
			SubagentDepth:        0,
			MaxSubagentDepth:     0,
		}
		ts := NewToolSet(context.Background(), cfg)
		if !hasSubagentTool(ts) {
			t.Error("expected subagent tool at depth 0 with unlimited max")
		}
	})

	// Depth 5, MaxDepth 0 (unlimited) -> should have subagent tool
	t.Run("depth 5 max 0 unlimited has subagent", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider:          provider,
			ModelID:              "test-model",
			WorkingDir:           "/test",
			SubagentRunner:       runner,
			SubagentDB:           db,
			ParentConversationID: "parent-123",
			SubagentDepth:        5,
			MaxSubagentDepth:     0,
		}
		ts := NewToolSet(context.Background(), cfg)
		if !hasSubagentTool(ts) {
			t.Error("expected subagent tool at depth 5 with unlimited max")
		}
	})

	// No SubagentRunner -> should NOT have subagent tool regardless of depth
	t.Run("no runner no subagent", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider:          provider,
			ModelID:              "test-model",
			WorkingDir:           "/test",
			ParentConversationID: "parent-123",
			SubagentDepth:        0,
			MaxSubagentDepth:     1,
		}
		ts := NewToolSet(context.Background(), cfg)
		if hasSubagentTool(ts) {
			t.Error("expected no subagent tool without runner")
		}
	})

	// Depth 2, MaxDepth 3 -> should have subagent tool
	t.Run("depth 2 max 3 has subagent", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider:          provider,
			ModelID:              "test-model",
			WorkingDir:           "/test",
			SubagentRunner:       runner,
			SubagentDB:           db,
			ParentConversationID: "parent-123",
			SubagentDepth:        2,
			MaxSubagentDepth:     3,
		}
		ts := NewToolSet(context.Background(), cfg)
		if !hasSubagentTool(ts) {
			t.Error("expected subagent tool at depth 2 with max 3")
		}
	})

	// Depth 3, MaxDepth 3 -> should NOT have subagent tool
	t.Run("depth 3 max 3 no subagent", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider:          provider,
			ModelID:              "test-model",
			WorkingDir:           "/test",
			SubagentRunner:       runner,
			SubagentDB:           db,
			ParentConversationID: "parent-123",
			SubagentDepth:        3,
			MaxSubagentDepth:     3,
		}
		ts := NewToolSet(context.Background(), cfg)
		if hasSubagentTool(ts) {
			t.Error("expected no subagent tool at depth 3 with max 3")
		}
	})
}

func TestToolSet_DeferredTools(t *testing.T) {
	provider := &mockLLMProvider{}

	cfg := ToolSetConfig{
		LLMProvider:            provider,
		ModelID:                "test-model",
		WorkingDir:             "/test",
		EnableBrowser:          true,
		EnableCodeIntelligence: true,
	}

	ctx := context.Background()
	ts := NewToolSet(ctx, cfg)

	allTools := ts.AllTools()
	activeTools := ts.ActiveTools()

	if len(activeTools) >= len(allTools) {
		t.Errorf("expected ActiveTools (%d) < AllTools (%d)", len(activeTools), len(allTools))
	}

	// request_tools should be in active tools
	foundRequestTools := false
	for _, tool := range activeTools {
		if tool.Name == "request_tools" {
			foundRequestTools = true
			break
		}
	}
	if !foundRequestTools {
		t.Error("expected request_tools in ActiveTools")
	}

	// request_tools should also be in all tools
	foundInAll := false
	for _, tool := range allTools {
		if tool.Name == "request_tools" {
			foundInAll = true
			break
		}
	}
	if !foundInAll {
		t.Error("expected request_tools in AllTools")
	}

	// Verify deferred tools are NOT in active tools
	for _, tool := range activeTools {
		if tool.Deferred {
			t.Errorf("deferred tool %q should not be in ActiveTools before activation", tool.Name)
		}
	}

	// Verify Tools() is alias for AllTools()
	if len(ts.AllTools()) != len(allTools) {
		t.Errorf("Tools() (%d) should equal AllTools() (%d)", len(ts.AllTools()), len(allTools))
	}
}

func TestToolSet_NoDeferredWithoutOptionalTools(t *testing.T) {
	provider := &mockLLMProvider{}

	cfg := ToolSetConfig{
		LLMProvider: provider,
		ModelID:     "test-model",
		WorkingDir:  "/test",
		// No browser, no LSP
	}

	ctx := context.Background()
	ts := NewToolSet(ctx, cfg)

	allTools := ts.AllTools()
	activeTools := ts.ActiveTools()

	// output_iframe is always present and always deferred, so request_tools is always added.
	// ActiveTools should exclude deferred output_iframe but include request_tools.
	foundRequestTools := false
	for _, tool := range allTools {
		if tool.Name == "request_tools" {
			foundRequestTools = true
			break
		}
	}
	if !foundRequestTools {
		t.Error("expected request_tools in AllTools (output_iframe is always deferred)")
	}

	// ActiveTools should be less than AllTools (deferred output_iframe excluded)
	if len(activeTools) >= len(allTools) {
		t.Errorf("expected ActiveTools (%d) < AllTools (%d)", len(activeTools), len(allTools))
	}

	// No browser or LSP tools should be present at all
	for _, tool := range allTools {
		if tool.Category == "browser" {
			t.Errorf("unexpected browser tool %q without EnableBrowser", tool.Name)
		}
		if tool.Category == "lsp" {
			t.Errorf("unexpected LSP tool %q without EnableCodeIntelligence", tool.Name)
		}
	}

	// The deferred output_iframe should NOT be in active tools
	for _, tool := range activeTools {
		if tool.Name == "output_iframe" {
			t.Error("deferred output_iframe should not be in ActiveTools before activation")
		}
	}
}

func TestToolSetConcurrentClassification(t *testing.T) {
	ts := NewToolSet(context.Background(), ToolSetConfig{
		WorkingDir: t.TempDir(),
	})
	defer ts.Cleanup()

	expectedConcurrent := map[string]bool{
		"read_file":      true,
		"keyword_search": true,
	}
	expectedSequential := map[string]bool{
		"bash":       true,
		"patch":      true,
		"change_dir": true,
		"todo_write": true,
	}

	for _, tool := range ts.AllTools() {
		if expectedConcurrent[tool.Name] && !tool.Concurrent {
			t.Errorf("tool %q should be Concurrent", tool.Name)
		}
		if expectedSequential[tool.Name] && tool.Concurrent {
			t.Errorf("tool %q should NOT be Concurrent", tool.Name)
		}
	}
}

func TestToolSet_HasScriptedTools(t *testing.T) {
	cfg := ToolSetConfig{
		WorkingDir: t.TempDir(),
		ModelID:    "test-model",
	}

	ts := NewToolSet(context.Background(), cfg)

	found := false
	for _, tool := range ts.AllTools() {
		if tool.Name == "scripted_tools" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected scripted_tools in tool set")
	}
}
