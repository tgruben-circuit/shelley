package cluster

import (
	"context"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

// Monitor watches for task status changes and resolves dependencies.
type Monitor struct {
	node         *Node
	orchestrator *Orchestrator
}

// NewMonitor creates a Monitor tied to the given cluster node and orchestrator.
func NewMonitor(node *Node, orch *Orchestrator) *Monitor {
	return &Monitor{
		node:         node,
		orchestrator: orch,
	}
}

// Run starts the monitor. It subscribes to task status events via NATS and
// periodically checks for stale agents (every 60s). Blocks until ctx is
// cancelled.
func (m *Monitor) Run(ctx context.Context) {
	sub, err := m.node.NC().Subscribe("task.*.status", func(msg *nats.Msg) {
		if _, err := m.orchestrator.ResolveDependencies(ctx); err != nil {
			slog.Error("monitor: resolve dependencies", "error", err)
		}
	})
	if err != nil {
		slog.Error("monitor: subscribe to task status", "error", err)
		return
	}
	defer func() { _ = sub.Unsubscribe() }()

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkStaleAgents(ctx)
		}
	}
}

// checkStaleAgents marks stale agents offline and requeues their tasks.
func (m *Monitor) checkStaleAgents(ctx context.Context) {
	stale := MarkStaleAgentsOffline(ctx, m.node.Registry, 90*time.Second)
	for _, agent := range stale {
		slog.Info("monitor: stale agent detected", "agent", agent.ID)
		m.requeueAgentTasks(ctx, agent.ID)
	}
}

// requeueAgentTasks requeues all assigned/working tasks for a dead agent
// and releases its locks.
func (m *Monitor) requeueAgentTasks(ctx context.Context, agentID string) {
	for _, status := range []TaskStatus{TaskStatusAssigned, TaskStatusWorking} {
		tasks, err := m.node.Tasks.ListByStatus(ctx, status)
		if err != nil {
			slog.Error("monitor: list tasks for requeue", "status", status, "error", err)
			continue
		}
		for _, task := range tasks {
			if task.AssignedTo != agentID {
				continue
			}
			if err := m.node.Tasks.Requeue(ctx, task.ID); err != nil {
				slog.Error("monitor: requeue task", "task", task.ID, "error", err)
			} else {
				slog.Info("monitor: requeued task", "task", task.ID, "agent", agentID)
			}
		}
	}

	if released, err := m.node.Locks.ReleaseByAgent(ctx, agentID); err != nil {
		slog.Error("monitor: release locks for agent", "agent", agentID, "error", err)
	} else if released > 0 {
		slog.Info("monitor: released locks", "agent", agentID, "count", released)
	}
}
