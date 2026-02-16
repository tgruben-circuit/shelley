package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// AgentStatus represents the current operational state of an agent.
type AgentStatus string

const (
	AgentStatusIdle    AgentStatus = "idle"
	AgentStatusWorking AgentStatus = "working"
	AgentStatusOffline AgentStatus = "offline"
)

// AgentCard describes a registered Percy agent and its current state.
type AgentCard struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Capabilities  []string    `json:"capabilities"`
	Status        AgentStatus `json:"status"`
	CurrentTaskID string      `json:"current_task,omitempty"`
	Repo          string      `json:"repo,omitempty"`
	Branch        string      `json:"branch,omitempty"`
	Machine       string      `json:"machine,omitempty"`
	StartedAt     time.Time   `json:"started_at"`
	LastHeartbeat time.Time   `json:"last_heartbeat"`
}

// AgentRegistry manages agent registration and discovery via NATS JetStream KV.
type AgentRegistry struct {
	js jetstream.JetStream
}

// NewAgentRegistry creates a new AgentRegistry backed by the given JetStream
// instance. The "agents" KV bucket must already exist (see SetupJetStream).
func NewAgentRegistry(js jetstream.JetStream) (*AgentRegistry, error) {
	return &AgentRegistry{js: js}, nil
}

// kv returns a handle to the agents KV bucket.
func (r *AgentRegistry) kv(ctx context.Context) (jetstream.KeyValue, error) {
	kv, err := r.js.KeyValue(ctx, BucketAgents)
	if err != nil {
		return nil, fmt.Errorf("agent registry kv: %w", err)
	}
	return kv, nil
}

// Register adds an agent to the registry. It sets the status to idle and
// initializes StartedAt and LastHeartbeat to the current time.
func (r *AgentRegistry) Register(ctx context.Context, card AgentCard) error {
	now := time.Now()
	card.Status = AgentStatusIdle
	card.StartedAt = now
	card.LastHeartbeat = now

	return r.putCard(ctx, card)
}

// Get retrieves an agent card by ID.
func (r *AgentRegistry) Get(ctx context.Context, agentID string) (*AgentCard, error) {
	kv, err := r.kv(ctx)
	if err != nil {
		return nil, err
	}

	entry, err := kv.Get(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("get agent %q: %w", agentID, err)
	}

	var card AgentCard
	if err := json.Unmarshal(entry.Value(), &card); err != nil {
		return nil, fmt.Errorf("unmarshal agent %q: %w", agentID, err)
	}
	return &card, nil
}

// List returns all registered agents. If no agents are registered, it returns
// nil, nil.
func (r *AgentRegistry) List(ctx context.Context) ([]AgentCard, error) {
	kv, err := r.kv(ctx)
	if err != nil {
		return nil, err
	}

	keys, err := kv.Keys(ctx)
	if errors.Is(err, jetstream.ErrNoKeysFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list agent keys: %w", err)
	}

	var cards []AgentCard
	for _, key := range keys {
		entry, err := kv.Get(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("get agent %q during list: %w", key, err)
		}
		var card AgentCard
		if err := json.Unmarshal(entry.Value(), &card); err != nil {
			return nil, fmt.Errorf("unmarshal agent %q during list: %w", key, err)
		}
		cards = append(cards, card)
	}
	return cards, nil
}

// Deregister removes an agent from the registry.
func (r *AgentRegistry) Deregister(ctx context.Context, agentID string) error {
	kv, err := r.kv(ctx)
	if err != nil {
		return err
	}

	if err := kv.Delete(ctx, agentID); err != nil {
		return fmt.Errorf("deregister agent %q: %w", agentID, err)
	}
	return nil
}

// Heartbeat updates the LastHeartbeat timestamp for the given agent.
func (r *AgentRegistry) Heartbeat(ctx context.Context, agentID string) error {
	card, err := r.Get(ctx, agentID)
	if err != nil {
		return fmt.Errorf("heartbeat get: %w", err)
	}

	card.LastHeartbeat = time.Now()
	return r.putCard(ctx, *card)
}

// UpdateStatus changes the status and current task ID for the given agent.
func (r *AgentRegistry) UpdateStatus(ctx context.Context, agentID string, status AgentStatus, taskID string) error {
	card, err := r.Get(ctx, agentID)
	if err != nil {
		return fmt.Errorf("update status get: %w", err)
	}

	card.Status = status
	card.CurrentTaskID = taskID
	return r.putCard(ctx, *card)
}

// putCard marshals the card and writes it to the KV store.
func (r *AgentRegistry) putCard(ctx context.Context, card AgentCard) error {
	kv, err := r.kv(ctx)
	if err != nil {
		return err
	}

	data, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("marshal agent %q: %w", card.ID, err)
	}

	if _, err := kv.Put(ctx, card.ID, data); err != nil {
		return fmt.Errorf("put agent %q: %w", card.ID, err)
	}
	return nil
}
