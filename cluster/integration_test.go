package cluster

import (
	"context"
	"testing"
	"time"
)

func TestIntegrationTwoNodeTaskFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start orchestrator node (embedded NATS).
	orchNode, err := StartNode(ctx, NodeConfig{
		AgentID:    "orchestrator",
		AgentName:  "orchestrator",
		ListenAddr: ":0",
		StoreDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer orchNode.Stop()

	// Start worker node connecting to orchestrator.
	workerNode, err := StartNode(ctx, NodeConfig{
		AgentID:      "worker-1",
		AgentName:    "backend-specialist",
		Capabilities: []string{"go", "sql"},
		NATSUrl:      orchNode.ClientURL(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer workerNode.Stop()

	// Verify both agents registered.
	agents, err := orchNode.Registry.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}

	// Orchestrator submits a plan with dependencies.
	orch := NewOrchestrator(orchNode)
	plan := TaskPlan{
		Tasks: []PlannedTask{
			{Task: Task{ID: "t1", Title: "Step 1", Type: TaskTypeImplement, Specialization: []string{"go"}}},
			{Task: Task{ID: "t2", Title: "Step 2", Type: TaskTypeTest}, DependsOn: []string{"t1"}},
		},
	}
	if err := orch.SubmitPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}

	// Worker claims t1 from its own node connection (shared NATS).
	if err := workerNode.Tasks.Claim(ctx, "t1", "worker-1"); err != nil {
		t.Fatal(err)
	}

	// Verify the claim is visible from orchestrator's view.
	task, err := orchNode.Tasks.Get(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if task.AssignedTo != "worker-1" {
		t.Fatalf("got assigned_to %q, want worker-1", task.AssignedTo)
	}

	// Worker acquires a file lock.
	if err := workerNode.Locks.Acquire(ctx, "github.com/test/repo", "server/users.go", "worker-1", "t1"); err != nil {
		t.Fatal(err)
	}

	// Verify the lock is visible from orchestrator's view.
	lock, err := orchNode.Locks.Get(ctx, "github.com/test/repo", "server/users.go")
	if err != nil {
		t.Fatal(err)
	}
	if lock.AgentID != "worker-1" {
		t.Fatalf("lock agent: got %q, want worker-1", lock.AgentID)
	}
	if lock.TaskID != "t1" {
		t.Fatalf("lock task: got %q, want t1", lock.TaskID)
	}

	// Worker completes t1.
	if err := workerNode.Tasks.Complete(ctx, "t1", TaskResult{Branch: "agent/worker-1/t1", Summary: "done"}); err != nil {
		t.Fatal(err)
	}

	// Worker releases lock.
	if err := workerNode.Locks.Release(ctx, "github.com/test/repo", "server/users.go"); err != nil {
		t.Fatal(err)
	}

	// Orchestrator resolves deps -- t2 should be unblocked.
	unblocked, err := orch.ResolveDependencies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unblocked) != 1 || unblocked[0].ID != "t2" {
		t.Fatalf("expected t2 unblocked, got %v", unblocked)
	}

	// Worker claims and completes t2.
	if err := workerNode.Tasks.Claim(ctx, "t2", "worker-1"); err != nil {
		t.Fatal(err)
	}
	if err := workerNode.Tasks.Complete(ctx, "t2", TaskResult{Branch: "agent/worker-1/t2", Summary: "tests pass"}); err != nil {
		t.Fatal(err)
	}

	// All tasks completed.
	completed, err := orchNode.Tasks.ListByStatus(ctx, TaskStatusCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if len(completed) != 2 {
		t.Fatalf("got %d completed tasks, want 2", len(completed))
	}

	// Verify results are stored correctly.
	t1, err := orchNode.Tasks.Get(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if t1.Result.Branch != "agent/worker-1/t1" {
		t.Errorf("t1 branch: got %q, want %q", t1.Result.Branch, "agent/worker-1/t1")
	}
	if t1.Result.Summary != "done" {
		t.Errorf("t1 summary: got %q, want %q", t1.Result.Summary, "done")
	}

	t2, err := orchNode.Tasks.Get(ctx, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if t2.Result.Branch != "agent/worker-1/t2" {
		t.Errorf("t2 branch: got %q, want %q", t2.Result.Branch, "agent/worker-1/t2")
	}
	if t2.Result.Summary != "tests pass" {
		t.Errorf("t2 summary: got %q, want %q", t2.Result.Summary, "tests pass")
	}

	// Worker goes offline -- verify deregistration.
	workerNode.Stop()

	// Verify only orchestrator remains.
	agents, err = orchNode.Registry.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Fatalf("got %d agents after worker stop, want 1", len(agents))
	}
	if agents[0].ID != "orchestrator" {
		t.Fatalf("remaining agent is %q, want orchestrator", agents[0].ID)
	}
}

func TestIntegrationThreeNodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	orchNode, err := StartNode(ctx, NodeConfig{
		AgentID:    "orch",
		AgentName:  "orchestrator",
		ListenAddr: ":0",
		StoreDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer orchNode.Stop()

	w1, err := StartNode(ctx, NodeConfig{
		AgentID:      "w1",
		AgentName:    "frontend",
		Capabilities: []string{"ts", "react"},
		NATSUrl:      orchNode.ClientURL(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w1.Stop()

	w2, err := StartNode(ctx, NodeConfig{
		AgentID:      "w2",
		AgentName:    "backend",
		Capabilities: []string{"go", "sql"},
		NATSUrl:      orchNode.ClientURL(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Stop()

	// All three registered.
	agents, err := orchNode.Registry.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 3 {
		t.Fatalf("got %d agents, want 3", len(agents))
	}

	// Submit parallel tasks with a dependent integration task.
	orchestrator := NewOrchestrator(orchNode)
	plan := TaskPlan{
		Tasks: []PlannedTask{
			{Task: Task{ID: "fe-1", Title: "Build UI", Type: TaskTypeImplement, Specialization: []string{"ts"}}},
			{Task: Task{ID: "be-1", Title: "Build API", Type: TaskTypeImplement, Specialization: []string{"go"}}},
			{Task: Task{ID: "int-1", Title: "Integration", Type: TaskTypeTest}, DependsOn: []string{"fe-1", "be-1"}},
		},
	}
	if err := orchestrator.SubmitPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}

	// Both workers claim their tasks.
	if err := w1.Tasks.Claim(ctx, "fe-1", "w1"); err != nil {
		t.Fatal(err)
	}
	if err := w2.Tasks.Claim(ctx, "be-1", "w2"); err != nil {
		t.Fatal(err)
	}

	// Verify each worker's claim is visible from the other node.
	fe1, err := w2.Tasks.Get(ctx, "fe-1")
	if err != nil {
		t.Fatal(err)
	}
	if fe1.AssignedTo != "w1" {
		t.Fatalf("fe-1 assigned_to: got %q, want w1", fe1.AssignedTo)
	}

	be1, err := w1.Tasks.Get(ctx, "be-1")
	if err != nil {
		t.Fatal(err)
	}
	if be1.AssignedTo != "w2" {
		t.Fatalf("be-1 assigned_to: got %q, want w2", be1.AssignedTo)
	}

	// Both complete in parallel.
	if err := w1.Tasks.Complete(ctx, "fe-1", TaskResult{Branch: "agent/w1/fe-1", Summary: "UI done"}); err != nil {
		t.Fatal(err)
	}
	if err := w2.Tasks.Complete(ctx, "be-1", TaskResult{Branch: "agent/w2/be-1", Summary: "API done"}); err != nil {
		t.Fatal(err)
	}

	// Resolve deps -- integration test unblocked.
	unblocked, err := orchestrator.ResolveDependencies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unblocked) != 1 || unblocked[0].ID != "int-1" {
		t.Fatalf("expected int-1 unblocked, got %v", unblocked)
	}

	// Either worker can claim the integration task.
	if err := w1.Tasks.Claim(ctx, "int-1", "w1"); err != nil {
		t.Fatal(err)
	}
	if err := w1.Tasks.Complete(ctx, "int-1", TaskResult{Branch: "agent/w1/int-1", Summary: "integration pass"}); err != nil {
		t.Fatal(err)
	}

	// All three tasks completed.
	completed, err := orchNode.Tasks.ListByStatus(ctx, TaskStatusCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if len(completed) != 3 {
		t.Fatalf("got %d completed tasks, want 3", len(completed))
	}
}

func TestIntegrationFileLockConflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	orchNode, err := StartNode(ctx, NodeConfig{
		AgentID:    "orch",
		AgentName:  "orchestrator",
		ListenAddr: ":0",
		StoreDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer orchNode.Stop()

	w1, err := StartNode(ctx, NodeConfig{
		AgentID:      "w1",
		AgentName:    "worker-1",
		Capabilities: []string{"go"},
		NATSUrl:      orchNode.ClientURL(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w1.Stop()

	w2, err := StartNode(ctx, NodeConfig{
		AgentID:      "w2",
		AgentName:    "worker-2",
		Capabilities: []string{"go"},
		NATSUrl:      orchNode.ClientURL(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Stop()

	// w1 acquires a lock on a file.
	if err := w1.Locks.Acquire(ctx, "github.com/test/repo", "main.go", "w1", "task-a"); err != nil {
		t.Fatal(err)
	}

	// w2 tries to lock the same file -- should fail.
	err = w2.Locks.Acquire(ctx, "github.com/test/repo", "main.go", "w2", "task-b")
	if err == nil {
		t.Fatal("expected error acquiring already-locked file, got nil")
	}

	// w1 releases the lock.
	if err := w1.Locks.Release(ctx, "github.com/test/repo", "main.go"); err != nil {
		t.Fatal(err)
	}

	// Now w2 can acquire it.
	if err := w2.Locks.Acquire(ctx, "github.com/test/repo", "main.go", "w2", "task-b"); err != nil {
		t.Fatalf("w2 acquire after release: %v", err)
	}

	// Verify the lock is owned by w2 from the orchestrator's view.
	lock, err := orchNode.Locks.Get(ctx, "github.com/test/repo", "main.go")
	if err != nil {
		t.Fatal(err)
	}
	if lock.AgentID != "w2" {
		t.Fatalf("lock agent: got %q, want w2", lock.AgentID)
	}
}

func TestIntegrationCASDoubleClaim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	orchNode, err := StartNode(ctx, NodeConfig{
		AgentID:    "orch",
		AgentName:  "orchestrator",
		ListenAddr: ":0",
		StoreDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer orchNode.Stop()

	w1, err := StartNode(ctx, NodeConfig{
		AgentID:      "w1",
		AgentName:    "worker-1",
		Capabilities: []string{"go"},
		NATSUrl:      orchNode.ClientURL(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w1.Stop()

	w2, err := StartNode(ctx, NodeConfig{
		AgentID:      "w2",
		AgentName:    "worker-2",
		Capabilities: []string{"go"},
		NATSUrl:      orchNode.ClientURL(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Stop()

	// Submit a single task.
	orch := NewOrchestrator(orchNode)
	plan := TaskPlan{
		Tasks: []PlannedTask{
			{Task: Task{ID: "race-task", Title: "Contested task", Type: TaskTypeImplement}},
		},
	}
	if err := orch.SubmitPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}

	// w1 claims first.
	if err := w1.Tasks.Claim(ctx, "race-task", "w1"); err != nil {
		t.Fatal(err)
	}

	// w2 tries to claim the same task -- should fail (status is no longer submitted).
	err = w2.Tasks.Claim(ctx, "race-task", "w2")
	if err == nil {
		t.Fatal("expected error on double-claim, got nil")
	}

	// Verify w1 still owns it.
	task, err := orchNode.Tasks.Get(ctx, "race-task")
	if err != nil {
		t.Fatal(err)
	}
	if task.AssignedTo != "w1" {
		t.Fatalf("assigned_to: got %q, want w1", task.AssignedTo)
	}
}
