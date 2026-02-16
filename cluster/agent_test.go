package cluster

import (
	"context"
	"testing"
	"time"
)

// setupTestRegistry starts an embedded NATS server, sets up JetStream, and
// returns an AgentRegistry ready for testing. It registers cleanup via t.
func setupTestRegistry(t *testing.T) (*AgentRegistry, context.Context) {
	t.Helper()

	dir := t.TempDir()
	srv, err := StartEmbeddedNATS(dir, 0)
	if err != nil {
		t.Fatalf("StartEmbeddedNATS: %v", err)
	}
	t.Cleanup(srv.Shutdown)

	ctx := context.Background()
	nc, err := Connect(ctx, srv.ClientURL())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(nc.Close)

	js, err := SetupJetStream(ctx, nc)
	if err != nil {
		t.Fatalf("SetupJetStream: %v", err)
	}

	reg, err := NewAgentRegistry(js)
	if err != nil {
		t.Fatalf("NewAgentRegistry: %v", err)
	}
	return reg, ctx
}

func TestRegisterAndGet(t *testing.T) {
	reg, ctx := setupTestRegistry(t)

	card := AgentCard{
		ID:           "agent-1",
		Name:         "Percy Worker 1",
		Capabilities: []string{"code", "review"},
		Repo:         "github.com/tgruben-circuit/percy",
		Branch:       "main",
		Machine:      "laptop",
	}

	if err := reg.Register(ctx, card); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := reg.Get(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.ID != card.ID {
		t.Errorf("ID: got %q, want %q", got.ID, card.ID)
	}
	if got.Name != card.Name {
		t.Errorf("Name: got %q, want %q", got.Name, card.Name)
	}
	if len(got.Capabilities) != 2 || got.Capabilities[0] != "code" || got.Capabilities[1] != "review" {
		t.Errorf("Capabilities: got %v, want %v", got.Capabilities, card.Capabilities)
	}
	if got.Repo != card.Repo {
		t.Errorf("Repo: got %q, want %q", got.Repo, card.Repo)
	}
	if got.Branch != card.Branch {
		t.Errorf("Branch: got %q, want %q", got.Branch, card.Branch)
	}
	if got.Machine != card.Machine {
		t.Errorf("Machine: got %q, want %q", got.Machine, card.Machine)
	}
	if got.Status != AgentStatusIdle {
		t.Errorf("Status: got %q, want %q", got.Status, AgentStatusIdle)
	}
	if got.StartedAt.IsZero() {
		t.Error("StartedAt should not be zero")
	}
	if got.LastHeartbeat.IsZero() {
		t.Error("LastHeartbeat should not be zero")
	}
}

func TestListAgents(t *testing.T) {
	reg, ctx := setupTestRegistry(t)

	agents := []AgentCard{
		{ID: "agent-a", Name: "Worker A", Capabilities: []string{"code"}},
		{ID: "agent-b", Name: "Worker B", Capabilities: []string{"review"}},
		{ID: "agent-c", Name: "Worker C", Capabilities: []string{"code", "review"}},
	}

	for _, a := range agents {
		if err := reg.Register(ctx, a); err != nil {
			t.Fatalf("Register(%s): %v", a.ID, err)
		}
	}

	got, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("List: got %d agents, want 3", len(got))
	}

	// Build a set of returned IDs.
	ids := make(map[string]bool)
	for _, a := range got {
		ids[a.ID] = true
	}
	for _, a := range agents {
		if !ids[a.ID] {
			t.Errorf("List missing agent %q", a.ID)
		}
	}
}

func TestListEmptyReturnsNil(t *testing.T) {
	reg, ctx := setupTestRegistry(t)

	got, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got != nil {
		t.Fatalf("List: expected nil for empty registry, got %v", got)
	}
}

func TestDeregister(t *testing.T) {
	reg, ctx := setupTestRegistry(t)

	card := AgentCard{ID: "agent-1", Name: "Worker 1", Capabilities: []string{"code"}}
	if err := reg.Register(ctx, card); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := reg.Deregister(ctx, "agent-1"); err != nil {
		t.Fatalf("Deregister: %v", err)
	}

	// Get should return an error.
	_, err := reg.Get(ctx, "agent-1")
	if err == nil {
		t.Fatal("Get after Deregister: expected error, got nil")
	}

	// List should return nil (empty).
	got, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List after Deregister: %v", err)
	}
	if got != nil {
		t.Fatalf("List after Deregister: expected nil, got %v", got)
	}
}

func TestUpdateStatus(t *testing.T) {
	reg, ctx := setupTestRegistry(t)

	card := AgentCard{ID: "agent-1", Name: "Worker 1", Capabilities: []string{"code"}}
	if err := reg.Register(ctx, card); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := reg.UpdateStatus(ctx, "agent-1", AgentStatusWorking, "task-42"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	got, err := reg.Get(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != AgentStatusWorking {
		t.Errorf("Status: got %q, want %q", got.Status, AgentStatusWorking)
	}
	if got.CurrentTaskID != "task-42" {
		t.Errorf("CurrentTaskID: got %q, want %q", got.CurrentTaskID, "task-42")
	}

	// Set back to idle with empty task.
	if err := reg.UpdateStatus(ctx, "agent-1", AgentStatusIdle, ""); err != nil {
		t.Fatalf("UpdateStatus to idle: %v", err)
	}

	got, err = reg.Get(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Get after idle: %v", err)
	}
	if got.Status != AgentStatusIdle {
		t.Errorf("Status: got %q, want %q", got.Status, AgentStatusIdle)
	}
	if got.CurrentTaskID != "" {
		t.Errorf("CurrentTaskID: got %q, want empty", got.CurrentTaskID)
	}
}

func TestHeartbeat(t *testing.T) {
	reg, ctx := setupTestRegistry(t)

	card := AgentCard{ID: "agent-1", Name: "Worker 1", Capabilities: []string{"code"}}
	if err := reg.Register(ctx, card); err != nil {
		t.Fatalf("Register: %v", err)
	}

	before, err := reg.Get(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Get before heartbeat: %v", err)
	}

	// Heartbeat should update LastHeartbeat to a time >= before.
	if err := reg.Heartbeat(ctx, "agent-1"); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	after, err := reg.Get(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Get after heartbeat: %v", err)
	}

	if after.LastHeartbeat.Before(before.LastHeartbeat) {
		t.Errorf("LastHeartbeat went backwards: before=%v, after=%v",
			before.LastHeartbeat.Format(time.RFC3339Nano),
			after.LastHeartbeat.Format(time.RFC3339Nano))
	}
}
