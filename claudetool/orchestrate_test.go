package claudetool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// mockOrchestrateRunner implements OrchestrateRunner for testing.
type mockOrchestrateRunner struct {
	response     string
	err          error
	receivedGoal string
	receivedOpts OrchestrateOptions
	receivedCwd  string
	receivedPID  string
}

func (m *mockOrchestrateRunner) RunOrchestration(ctx context.Context, parentConversationID, cwd, goal string, opts OrchestrateOptions) (string, error) {
	m.receivedGoal = goal
	m.receivedOpts = opts
	m.receivedCwd = cwd
	m.receivedPID = parentConversationID
	if m.err != nil {
		return "", m.err
	}
	return m.response, nil
}

func newOrchestrateTool(runner OrchestrateRunner) *OrchestrateTool {
	return &OrchestrateTool{
		ParentConversationID: "parent-1",
		WorkingDir:           NewMutableWorkingDir("/work"),
		Runner:               runner,
	}
}

func TestOrchestrateTool_PassesGoalAndOptions(t *testing.T) {
	runner := &mockOrchestrateRunner{response: "done"}
	tool := newOrchestrateTool(runner)

	input, _ := json.Marshal(orchestrateInput{
		Goal:           "build a feature",
		PlannerModel:   "claude-opus-4.8",
		BuilderModel:   "glm-5.2-fireworks",
		VerifierModel:  "gpt-5.5",
		MaxConcurrency: 2,
		SkipVerify:     true,
	})

	out := tool.Run(context.Background(), input)
	if out.Error != nil {
		t.Fatalf("unexpected tool error: %v", out.LLMContent)
	}
	if runner.receivedGoal != "build a feature" {
		t.Errorf("goal = %q, want %q", runner.receivedGoal, "build a feature")
	}
	if runner.receivedPID != "parent-1" {
		t.Errorf("parentID = %q, want parent-1", runner.receivedPID)
	}
	if runner.receivedCwd != "/work" {
		t.Errorf("cwd = %q, want /work", runner.receivedCwd)
	}
	if runner.receivedOpts.PlannerModel != "claude-opus-4.8" ||
		runner.receivedOpts.BuilderModel != "glm-5.2-fireworks" ||
		runner.receivedOpts.VerifierModel != "gpt-5.5" ||
		runner.receivedOpts.MaxConcurrency != 2 ||
		!runner.receivedOpts.SkipVerify {
		t.Errorf("opts mismatch: %+v", runner.receivedOpts)
	}
}

func TestOrchestrateTool_RequiresGoal(t *testing.T) {
	tool := newOrchestrateTool(&mockOrchestrateRunner{})
	out := tool.Run(context.Background(), json.RawMessage(`{"goal":"  "}`))
	if out.Error == nil {
		t.Fatal("expected error for empty goal")
	}
}

func TestOrchestrateTool_PropagatesRunnerError(t *testing.T) {
	tool := newOrchestrateTool(&mockOrchestrateRunner{err: context.Canceled})
	out := tool.Run(context.Background(), json.RawMessage(`{"goal":"x"}`))
	if out.Error == nil {
		t.Fatal("expected tool error from runner failure")
	}
}

func TestOrchestrateTool_ReturnsSummary(t *testing.T) {
	tool := newOrchestrateTool(&mockOrchestrateRunner{response: "SUMMARY-TEXT"})
	out := tool.Run(context.Background(), json.RawMessage(`{"goal":"x"}`))
	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.LLMContent)
	}
	var found bool
	for _, c := range out.LLMContent {
		if strings.Contains(c.Text, "SUMMARY-TEXT") {
			found = true
		}
	}
	if !found {
		t.Errorf("summary not returned: %+v", out.LLMContent)
	}
}
