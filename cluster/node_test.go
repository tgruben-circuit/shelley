package cluster

import (
	"context"
	"log/slog"
	"testing"
)

func TestStartNodeEmbedded(t *testing.T) {
	ctx := context.Background()
	node, err := StartNode(ctx, NodeConfig{
		AgentID:      "node-1",
		AgentName:    "Test Node 1",
		Capabilities: []string{"code", "review"},
		ListenAddr:   ":0",
		StoreDir:     t.TempDir(),
		Logger:       slog.Default(),
	})
	if err != nil {
		t.Fatalf("StartNode: %v", err)
	}
	defer node.Stop()

	// Verify the node has a valid client URL.
	if node.ClientURL() == "" {
		t.Fatal("ClientURL returned empty string")
	}

	// Verify self-registration: agent should be visible in the registry.
	card, err := node.Registry.Get(ctx, "node-1")
	if err != nil {
		t.Fatalf("Registry.Get: %v", err)
	}
	if card.ID != "node-1" {
		t.Errorf("agent ID: got %q, want %q", card.ID, "node-1")
	}
	if card.Name != "Test Node 1" {
		t.Errorf("agent Name: got %q, want %q", card.Name, "Test Node 1")
	}
	if card.Status != AgentStatusIdle {
		t.Errorf("agent Status: got %q, want %q", card.Status, AgentStatusIdle)
	}
	if len(card.Capabilities) != 2 || card.Capabilities[0] != "code" || card.Capabilities[1] != "review" {
		t.Errorf("agent Capabilities: got %v, want [code review]", card.Capabilities)
	}
}

func TestStartNodeConnectToExisting(t *testing.T) {
	ctx := context.Background()

	// Start first node with embedded NATS.
	node1, err := StartNode(ctx, NodeConfig{
		AgentID:      "node-1",
		AgentName:    "Node 1",
		Capabilities: []string{"code"},
		ListenAddr:   ":0",
		StoreDir:     t.TempDir(),
		Logger:       slog.Default(),
	})
	if err != nil {
		t.Fatalf("StartNode(node-1): %v", err)
	}
	defer node1.Stop()

	// Start second node connecting to first node's embedded NATS.
	node2, err := StartNode(ctx, NodeConfig{
		AgentID:      "node-2",
		AgentName:    "Node 2",
		Capabilities: []string{"review"},
		NATSUrl:      node1.ClientURL(),
		Logger:       slog.Default(),
	})
	if err != nil {
		t.Fatalf("StartNode(node-2): %v", err)
	}
	defer node2.Stop()

	// Both agents should be visible from node1's registry.
	agents1, err := node1.Registry.List(ctx)
	if err != nil {
		t.Fatalf("node1 Registry.List: %v", err)
	}
	if len(agents1) != 2 {
		t.Fatalf("node1 Registry.List: got %d agents, want 2", len(agents1))
	}

	// Both agents should be visible from node2's registry.
	agents2, err := node2.Registry.List(ctx)
	if err != nil {
		t.Fatalf("node2 Registry.List: %v", err)
	}
	if len(agents2) != 2 {
		t.Fatalf("node2 Registry.List: got %d agents, want 2", len(agents2))
	}

	// Verify specific agents are present from node2's perspective.
	ids := make(map[string]bool)
	for _, a := range agents2 {
		ids[a.ID] = true
	}
	if !ids["node-1"] {
		t.Error("node2 registry missing node-1")
	}
	if !ids["node-2"] {
		t.Error("node2 registry missing node-2")
	}
}

func TestStopDeregisters(t *testing.T) {
	ctx := context.Background()

	// Start first node with embedded NATS.
	node1, err := StartNode(ctx, NodeConfig{
		AgentID:      "node-1",
		AgentName:    "Node 1",
		Capabilities: []string{"code"},
		ListenAddr:   ":0",
		StoreDir:     t.TempDir(),
		Logger:       slog.Default(),
	})
	if err != nil {
		t.Fatalf("StartNode(node-1): %v", err)
	}
	defer node1.Stop()

	// Start second node connecting to first.
	node2, err := StartNode(ctx, NodeConfig{
		AgentID:      "node-2",
		AgentName:    "Node 2",
		Capabilities: []string{"review"},
		NATSUrl:      node1.ClientURL(),
		Logger:       slog.Default(),
	})
	if err != nil {
		t.Fatalf("StartNode(node-2): %v", err)
	}

	// Verify both are registered.
	agents, err := node1.Registry.List(ctx)
	if err != nil {
		t.Fatalf("List before stop: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("List before stop: got %d agents, want 2", len(agents))
	}

	// Stop node2 -- it should deregister.
	node2.Stop()

	// Only node-1 should remain.
	agents, err = node1.Registry.List(ctx)
	if err != nil {
		t.Fatalf("List after stop: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("List after stop: got %d agents, want 1", len(agents))
	}
	if agents[0].ID != "node-1" {
		t.Errorf("remaining agent: got %q, want %q", agents[0].ID, "node-1")
	}
}

func TestStartNodeNoConfig(t *testing.T) {
	ctx := context.Background()
	_, err := StartNode(ctx, NodeConfig{
		AgentID:   "node-1",
		AgentName: "Node 1",
		Logger:    slog.Default(),
	})
	if err == nil {
		t.Fatal("expected error when neither ListenAddr nor NATSUrl is set")
	}
}
