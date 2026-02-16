package cluster

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// NodeConfig holds the configuration for starting a cluster Node.
type NodeConfig struct {
	AgentID      string
	AgentName    string
	Capabilities []string
	ListenAddr   string // non-empty = start embedded NATS (e.g. ":4222" or ":0")
	NATSUrl      string // non-empty = connect to external NATS
	StoreDir     string // JetStream storage directory (embedded only)
	Logger       *slog.Logger
}

// Node is the main integration point that ties together all cluster components
// for a single Percy instance.
type Node struct {
	Config   NodeConfig
	embedded *EmbeddedNATS
	nc       *nats.Conn
	js       jetstream.JetStream

	Registry *AgentRegistry
	Tasks    *TaskQueue
	Locks    *LockManager
}

// StartNode creates and starts a cluster Node. It starts an embedded NATS
// server if ListenAddr is set, or connects to an external one if NATSUrl is
// set. It initializes JetStream infrastructure and registers itself in the
// agent registry.
func StartNode(ctx context.Context, cfg NodeConfig) (*Node, error) {
	if cfg.ListenAddr == "" && cfg.NATSUrl == "" {
		return nil, fmt.Errorf("start node: either ListenAddr or NATSUrl must be set")
	}

	n := &Node{Config: cfg}

	// Determine the NATS URL to connect to.
	var natsURL string
	if cfg.ListenAddr != "" {
		var port int
		if _, err := fmt.Sscanf(cfg.ListenAddr, ":%d", &port); err != nil {
			return nil, fmt.Errorf("start node: parse listen addr %q: %w", cfg.ListenAddr, err)
		}

		embedded, err := StartEmbeddedNATS(cfg.StoreDir, port)
		if err != nil {
			return nil, fmt.Errorf("start node: %w", err)
		}
		n.embedded = embedded
		natsURL = embedded.ClientURL()
	} else {
		natsURL = cfg.NATSUrl
	}

	// Connect to NATS.
	nc, err := Connect(ctx, natsURL)
	if err != nil {
		n.shutdownEmbedded()
		return nil, fmt.Errorf("start node: %w", err)
	}
	n.nc = nc

	// Set up JetStream infrastructure.
	js, err := SetupJetStream(ctx, nc)
	if err != nil {
		nc.Close()
		n.shutdownEmbedded()
		return nil, fmt.Errorf("start node: %w", err)
	}
	n.js = js

	// Create subsystems.
	registry, err := NewAgentRegistry(js)
	if err != nil {
		nc.Close()
		n.shutdownEmbedded()
		return nil, fmt.Errorf("start node: %w", err)
	}
	n.Registry = registry

	tasks, err := NewTaskQueue(js, nc)
	if err != nil {
		nc.Close()
		n.shutdownEmbedded()
		return nil, fmt.Errorf("start node: %w", err)
	}
	n.Tasks = tasks

	n.Locks = NewLockManager(js)

	// Register self in the agent registry.
	card := AgentCard{
		ID:           cfg.AgentID,
		Name:         cfg.AgentName,
		Capabilities: cfg.Capabilities,
	}
	if err := registry.Register(ctx, card); err != nil {
		nc.Close()
		n.shutdownEmbedded()
		return nil, fmt.Errorf("start node: register self: %w", err)
	}

	return n, nil
}

// NC returns the underlying NATS connection.
func (n *Node) NC() *nats.Conn { return n.nc }

// IsOrchestrator returns true if this node is running an embedded NATS server.
func (n *Node) IsOrchestrator() bool { return n.embedded != nil }

// ClientURL returns the NATS URL that clients should use to connect.
func (n *Node) ClientURL() string {
	if n.embedded != nil {
		return n.embedded.ClientURL()
	}
	return n.Config.NATSUrl
}

// Stop deregisters the node from the agent registry, closes the NATS
// connection, and shuts down the embedded NATS server if one was started.
func (n *Node) Stop() {
	ctx := context.Background()
	if n.Registry != nil {
		_ = n.Registry.Deregister(ctx, n.Config.AgentID)
	}
	if n.nc != nil {
		n.nc.Close()
	}
	n.shutdownEmbedded()
}

// shutdownEmbedded shuts down the embedded NATS server if present.
func (n *Node) shutdownEmbedded() {
	if n.embedded != nil {
		n.embedded.Shutdown()
	}
}
