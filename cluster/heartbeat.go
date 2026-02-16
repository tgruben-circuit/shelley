package cluster

import (
	"context"
	"log/slog"
	"time"
)

// FindStaleAgents returns agents whose last heartbeat is older than maxAge.
// Agents already marked offline are skipped.
func FindStaleAgents(ctx context.Context, reg *AgentRegistry, maxAge time.Duration) []AgentCard {
	agents, err := reg.List(ctx)
	if err != nil {
		slog.Error("find stale agents: list", "error", err)
		return nil
	}

	cutoff := time.Now().Add(-maxAge)
	var stale []AgentCard
	for _, a := range agents {
		if a.Status == AgentStatusOffline {
			continue
		}
		if a.LastHeartbeat.Before(cutoff) {
			stale = append(stale, a)
		}
	}
	return stale
}

// MarkStaleAgentsOffline finds stale agents and marks them offline.
// It returns the list of agents that were marked offline.
func MarkStaleAgentsOffline(ctx context.Context, reg *AgentRegistry, maxAge time.Duration) []AgentCard {
	stale := FindStaleAgents(ctx, reg, maxAge)
	var marked []AgentCard
	for _, a := range stale {
		if err := reg.UpdateStatus(ctx, a.ID, AgentStatusOffline, ""); err != nil {
			slog.Error("mark stale agent offline", "agent", a.ID, "error", err)
			continue
		}
		marked = append(marked, a)
	}
	return marked
}
