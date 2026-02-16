package cluster

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

func TestMonitorResolvesDependencies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start a node with embedded NATS.
	node, err := StartNode(ctx, NodeConfig{
		AgentID:      "mon-agent",
		AgentName:    "Monitor Agent",
		Capabilities: []string{"orchestrate"},
		ListenAddr:   ":0",
		StoreDir:     t.TempDir(),
		Logger:       slog.Default(),
	})
	if err != nil {
		t.Fatalf("StartNode: %v", err)
	}
	defer node.Stop()

	// Create orchestrator and submit a plan with dependencies.
	orch := NewOrchestrator(node)
	plan := TaskPlan{
		Tasks: []PlannedTask{
			{
				Task: Task{
					ID:      "t1",
					Type:    TaskTypeImplement,
					Title:   "Task 1 (no deps)",
					Context: TaskContext{Repo: "percy", BaseBranch: "main"},
				},
			},
			{
				Task: Task{
					ID:      "t2",
					Type:    TaskTypeTest,
					Title:   "Task 2 (depends on t1)",
					Context: TaskContext{Repo: "percy", BaseBranch: "main"},
				},
				DependsOn: []string{"t1"},
			},
		},
	}

	if err := orch.SubmitPlan(ctx, plan); err != nil {
		t.Fatalf("SubmitPlan: %v", err)
	}

	// Start monitor in background goroutine.
	mon := NewMonitor(node, orch)
	go mon.Run(ctx)

	// Claim and complete t1. Complete publishes a status event which
	// triggers the monitor to call ResolveDependencies.
	if err := node.Tasks.Claim(ctx, "t1", "worker-1"); err != nil {
		t.Fatalf("Claim(t1): %v", err)
	}
	if err := node.Tasks.Complete(ctx, "t1", TaskResult{Summary: "done"}); err != nil {
		t.Fatalf("Complete(t1): %v", err)
	}

	// Poll until t2 appears as submitted (max 5s, poll every 100ms).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err := node.Tasks.Get(ctx, "t2")
		if err == nil && task.Status == TaskStatusSubmitted {
			return // success
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("t2 was not submitted within 5s; monitor did not resolve dependencies")
}
