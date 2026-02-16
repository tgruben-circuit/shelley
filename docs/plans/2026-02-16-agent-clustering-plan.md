# Agent Clustering Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add NATS-based multi-agent coordination to Percy so multiple instances can collaborate on tasks across machines.

**Architecture:** New `cluster/` package with embedded NATS server, JetStream for durable task queues and KV stores, hybrid orchestration via an `orchestrate` CLI subcommand. Additive to Percy -- without `--cluster`, behavior is unchanged.

**Tech Stack:** Go, NATS (github.com/nats-io/nats-server/v2, github.com/nats-io/nats.go), JetStream KV, existing Percy loop/server/llm packages.

**Review process:** At the end of each phase, request a code review from Codex before proceeding.

---

## Phase 1: NATS Foundation & Embedded Server

### Task 1.1: Add NATS dependencies

**Files:**
- Modify: `go.mod`

**Step 1: Add NATS server and client libraries**

Run:
```bash
cd /Users/toddgruben/Projects/shelley
go get github.com/nats-io/nats-server/v2@latest
go get github.com/nats-io/nats.go@latest
go mod tidy
```

**Step 2: Verify dependencies resolve**

Run: `go build ./...`
Expected: Clean build, no errors.

**Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add nats-server and nats.go for agent clustering"
```

---

### Task 1.2: Create cluster package with NATS connection management

**Files:**
- Create: `cluster/nats.go`
- Create: `cluster/nats_test.go`

**Step 1: Write the test for embedded NATS startup and client connection**

```go
// cluster/nats_test.go
package cluster

import (
	"context"
	"testing"
	"time"
)

func TestEmbeddedNATS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ns, err := StartEmbeddedNATS(t.TempDir(), 0) // 0 = random port
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Shutdown()

	nc, err := Connect(ctx, ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	if !nc.IsConnected() {
		t.Fatal("expected connected")
	}
}

func TestConnectExternal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ns, err := StartEmbeddedNATS(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Shutdown()

	// Connect as if external
	nc, err := Connect(ctx, ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	// Basic pub/sub works
	sub, err := nc.SubscribeSync("test.ping")
	if err != nil {
		t.Fatal(err)
	}

	if err := nc.Publish("test.ping", []byte("hello")); err != nil {
		t.Fatal(err)
	}

	msg, err := sub.NextMsgWithContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(msg.Data) != "hello" {
		t.Fatalf("got %q, want %q", msg.Data, "hello")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cluster/ -run TestEmbeddedNATS -v`
Expected: FAIL -- package doesn't exist yet.

**Step 3: Write the implementation**

```go
// cluster/nats.go
package cluster

import (
	"context"
	"fmt"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// EmbeddedNATS wraps an in-process NATS server.
type EmbeddedNATS struct {
	server *natsserver.Server
}

// StartEmbeddedNATS starts a NATS server in-process with JetStream enabled.
// If port is 0, a random available port is chosen.
// storeDir is the directory for JetStream file-based storage.
func StartEmbeddedNATS(storeDir string, port int) (*EmbeddedNATS, error) {
	opts := &natsserver.Options{
		Port:      port,
		JetStream: true,
		StoreDir:  storeDir,
		NoLog:     true,
		NoSigs:    true,
	}

	ns, err := natsserver.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("create nats server: %w", err)
	}

	ns.Start()

	if !ns.ReadyForConnections(5 * time.Second) {
		ns.Shutdown()
		return nil, fmt.Errorf("nats server failed to start within 5s")
	}

	return &EmbeddedNATS{server: ns}, nil
}

// ClientURL returns the URL clients should use to connect.
func (e *EmbeddedNATS) ClientURL() string {
	return e.server.ClientURL()
}

// Shutdown gracefully stops the embedded NATS server.
func (e *EmbeddedNATS) Shutdown() {
	e.server.Shutdown()
	e.server.WaitForShutdown()
}

// Connect establishes a NATS client connection with auto-reconnect.
func Connect(ctx context.Context, url string) (*nats.Conn, error) {
	nc, err := nats.Connect(url,
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
		nats.Context(ctx),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to nats %s: %w", url, err)
	}
	return nc, nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./cluster/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add cluster/
git commit -m "feat(cluster): embedded NATS server and client connection"
```

---

### Task 1.3: JetStream setup helper

**Files:**
- Modify: `cluster/nats.go`
- Create: `cluster/jetstream.go`
- Create: `cluster/jetstream_test.go`

**Step 1: Write the test for JetStream initialization**

```go
// cluster/jetstream_test.go
package cluster

import (
	"context"
	"testing"
	"time"
)

func TestJetStreamSetup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ns, err := StartEmbeddedNATS(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Shutdown()

	nc, err := Connect(ctx, ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	js, err := SetupJetStream(ctx, nc)
	if err != nil {
		t.Fatal(err)
	}

	// Verify KV buckets exist
	agents, err := js.KeyValue(ctx, BucketAgents)
	if err != nil {
		t.Fatalf("agents bucket: %v", err)
	}
	if agents == nil {
		t.Fatal("agents bucket is nil")
	}

	locks, err := js.KeyValue(ctx, BucketLocks)
	if err != nil {
		t.Fatalf("locks bucket: %v", err)
	}
	if locks == nil {
		t.Fatal("locks bucket is nil")
	}

	clusterKV, err := js.KeyValue(ctx, BucketCluster)
	if err != nil {
		t.Fatalf("cluster bucket: %v", err)
	}
	if clusterKV == nil {
		t.Fatal("cluster bucket is nil")
	}

	// Verify TASKS stream exists
	stream, err := js.Stream(ctx, StreamTasks)
	if err != nil {
		t.Fatalf("tasks stream: %v", err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Config.Name != StreamTasks {
		t.Fatalf("got stream name %q, want %q", info.Config.Name, StreamTasks)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cluster/ -run TestJetStreamSetup -v`
Expected: FAIL

**Step 3: Write the implementation**

```go
// cluster/jetstream.go
package cluster

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	BucketAgents  = "agents"
	BucketLocks   = "locks"
	BucketCluster = "cluster"
	StreamTasks   = "TASKS"
)

// SetupJetStream creates the required KV buckets and streams.
// Safe to call multiple times -- existing buckets/streams are returned as-is.
func SetupJetStream(ctx context.Context, nc *nats.Conn) (jetstream.JetStream, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("create jetstream: %w", err)
	}

	buckets := []string{BucketAgents, BucketLocks, BucketCluster}
	for _, name := range buckets {
		_, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
			Bucket: name,
		})
		if err != nil {
			return nil, fmt.Errorf("create kv bucket %s: %w", name, err)
		}
	}

	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     StreamTasks,
		Subjects: []string{"task.>"},
	})
	if err != nil {
		return nil, fmt.Errorf("create tasks stream: %w", err)
	}

	return js, nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./cluster/ -v`
Expected: All PASS

**Step 5: Commit**

```bash
git add cluster/
git commit -m "feat(cluster): JetStream setup with KV buckets and TASKS stream"
```

---

### Task 1.4: Phase 1 review

Request a code review from Codex on the `cluster/` package so far. Verify:
- Embedded NATS starts and stops cleanly
- JetStream KV buckets and streams are created idempotently
- Tests pass, no resource leaks
- Code follows Percy conventions (error wrapping, naming)

---

## Phase 2: Agent Registry & Discovery

### Task 2.1: Agent card model

**Files:**
- Create: `cluster/agent.go`
- Create: `cluster/agent_test.go`

**Step 1: Write the test for agent registration and retrieval**

```go
// cluster/agent_test.go
package cluster

import (
	"context"
	"testing"
	"time"
)

func TestAgentRegisterAndGet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ns, err := StartEmbeddedNATS(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Shutdown()

	nc, err := Connect(ctx, ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	js, err := SetupJetStream(ctx, nc)
	if err != nil {
		t.Fatal(err)
	}

	reg := NewAgentRegistry(js)

	card := AgentCard{
		ID:           "agent-test-1",
		Name:         "test-agent",
		Capabilities: []string{"go", "sql"},
		Machine:      "localhost",
		Repo:         "github.com/test/repo",
	}

	if err := reg.Register(ctx, card); err != nil {
		t.Fatal(err)
	}

	got, err := reg.Get(ctx, "agent-test-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "test-agent" {
		t.Fatalf("got name %q, want %q", got.Name, "test-agent")
	}
	if got.Status != AgentStatusIdle {
		t.Fatalf("got status %q, want %q", got.Status, AgentStatusIdle)
	}
}

func TestAgentList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ns, err := StartEmbeddedNATS(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Shutdown()

	nc, err := Connect(ctx, ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	js, err := SetupJetStream(ctx, nc)
	if err != nil {
		t.Fatal(err)
	}

	reg := NewAgentRegistry(js)

	for _, name := range []string{"agent-1", "agent-2"} {
		err := reg.Register(ctx, AgentCard{
			ID:           name,
			Name:         name,
			Capabilities: []string{"go"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	agents, err := reg.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}
}

func TestAgentDeregister(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ns, err := StartEmbeddedNATS(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Shutdown()

	nc, err := Connect(ctx, ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	js, err := SetupJetStream(ctx, nc)
	if err != nil {
		t.Fatal(err)
	}

	reg := NewAgentRegistry(js)

	err = reg.Register(ctx, AgentCard{ID: "agent-1", Name: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}

	if err := reg.Deregister(ctx, "agent-1"); err != nil {
		t.Fatal(err)
	}

	agents, err := reg.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 0 {
		t.Fatalf("got %d agents, want 0", len(agents))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cluster/ -run TestAgent -v`
Expected: FAIL

**Step 3: Write the implementation**

```go
// cluster/agent.go
package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

type AgentStatus string

const (
	AgentStatusIdle    AgentStatus = "idle"
	AgentStatusWorking AgentStatus = "working"
	AgentStatusOffline AgentStatus = "offline"
)

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

type AgentRegistry struct {
	js jetstream.JetStream
}

func NewAgentRegistry(js jetstream.JetStream) *AgentRegistry {
	return &AgentRegistry{js: js}
}

func (r *AgentRegistry) Register(ctx context.Context, card AgentCard) error {
	kv, err := r.js.KeyValue(ctx, BucketAgents)
	if err != nil {
		return fmt.Errorf("get agents bucket: %w", err)
	}

	now := time.Now()
	card.Status = AgentStatusIdle
	card.StartedAt = now
	card.LastHeartbeat = now

	data, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("marshal agent card: %w", err)
	}

	if _, err := kv.Put(ctx, card.ID, data); err != nil {
		return fmt.Errorf("put agent card: %w", err)
	}
	return nil
}

func (r *AgentRegistry) Get(ctx context.Context, agentID string) (*AgentCard, error) {
	kv, err := r.js.KeyValue(ctx, BucketAgents)
	if err != nil {
		return nil, fmt.Errorf("get agents bucket: %w", err)
	}

	entry, err := kv.Get(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("get agent %s: %w", agentID, err)
	}

	var card AgentCard
	if err := json.Unmarshal(entry.Value(), &card); err != nil {
		return nil, fmt.Errorf("unmarshal agent card: %w", err)
	}
	return &card, nil
}

func (r *AgentRegistry) List(ctx context.Context) ([]AgentCard, error) {
	kv, err := r.js.KeyValue(ctx, BucketAgents)
	if err != nil {
		return nil, fmt.Errorf("get agents bucket: %w", err)
	}

	keys, err := kv.Keys(ctx)
	if err != nil {
		if err.Error() == "nats: no keys found" {
			return nil, nil
		}
		return nil, fmt.Errorf("list agent keys: %w", err)
	}

	var agents []AgentCard
	for _, key := range keys {
		entry, err := kv.Get(ctx, key)
		if err != nil {
			continue
		}
		var card AgentCard
		if err := json.Unmarshal(entry.Value(), &card); err != nil {
			continue
		}
		agents = append(agents, card)
	}
	return agents, nil
}

func (r *AgentRegistry) Deregister(ctx context.Context, agentID string) error {
	kv, err := r.js.KeyValue(ctx, BucketAgents)
	if err != nil {
		return fmt.Errorf("get agents bucket: %w", err)
	}

	if err := kv.Delete(ctx, agentID); err != nil {
		return fmt.Errorf("delete agent %s: %w", agentID, err)
	}
	return nil
}

func (r *AgentRegistry) Heartbeat(ctx context.Context, agentID string) error {
	card, err := r.Get(ctx, agentID)
	if err != nil {
		return err
	}
	card.LastHeartbeat = time.Now()

	kv, err := r.js.KeyValue(ctx, BucketAgents)
	if err != nil {
		return fmt.Errorf("get agents bucket: %w", err)
	}

	data, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("marshal agent card: %w", err)
	}

	if _, err := kv.Put(ctx, agentID, data); err != nil {
		return fmt.Errorf("put agent card: %w", err)
	}
	return nil
}

func (r *AgentRegistry) UpdateStatus(ctx context.Context, agentID string, status AgentStatus, taskID string) error {
	card, err := r.Get(ctx, agentID)
	if err != nil {
		return err
	}
	card.Status = status
	card.CurrentTaskID = taskID
	card.LastHeartbeat = time.Now()

	kv, err := r.js.KeyValue(ctx, BucketAgents)
	if err != nil {
		return fmt.Errorf("get agents bucket: %w", err)
	}

	data, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("marshal agent card: %w", err)
	}

	if _, err := kv.Put(ctx, agentID, data); err != nil {
		return fmt.Errorf("put agent card: %w", err)
	}
	return nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./cluster/ -run TestAgent -v`
Expected: All PASS

**Step 5: Commit**

```bash
git add cluster/
git commit -m "feat(cluster): agent registry with KV-backed registration and heartbeat"
```

---

### Task 2.2: Agent heartbeat monitor

**Files:**
- Create: `cluster/heartbeat.go`
- Create: `cluster/heartbeat_test.go`

**Step 1: Write the test for stale agent detection**

```go
// cluster/heartbeat_test.go
package cluster

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestHeartbeatMonitorDetectsStaleAgent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ns, err := StartEmbeddedNATS(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Shutdown()

	nc, err := Connect(ctx, ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	js, err := SetupJetStream(ctx, nc)
	if err != nil {
		t.Fatal(err)
	}

	reg := NewAgentRegistry(js)

	// Register an agent with an old heartbeat
	card := AgentCard{
		ID:            "stale-agent",
		Name:          "stale",
		Status:        AgentStatusWorking,
		LastHeartbeat: time.Now().Add(-5 * time.Minute),
		StartedAt:     time.Now().Add(-10 * time.Minute),
	}
	kv, _ := js.KeyValue(ctx, BucketAgents)
	data, _ := json.Marshal(card)
	kv.Put(ctx, card.ID, data)

	stale := FindStaleAgents(ctx, reg, 90*time.Second)
	if len(stale) != 1 {
		t.Fatalf("got %d stale agents, want 1", len(stale))
	}
	if stale[0].ID != "stale-agent" {
		t.Fatalf("got stale agent %q, want %q", stale[0].ID, "stale-agent")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cluster/ -run TestHeartbeat -v`
Expected: FAIL

**Step 3: Write the implementation**

```go
// cluster/heartbeat.go
package cluster

import (
	"context"
	"time"
)

// FindStaleAgents returns agents whose last heartbeat is older than maxAge.
func FindStaleAgents(ctx context.Context, reg *AgentRegistry, maxAge time.Duration) []AgentCard {
	agents, err := reg.List(ctx)
	if err != nil {
		return nil
	}

	cutoff := time.Now().Add(-maxAge)
	var stale []AgentCard
	for _, a := range agents {
		if a.Status != AgentStatusOffline && a.LastHeartbeat.Before(cutoff) {
			stale = append(stale, a)
		}
	}
	return stale
}

// MarkStaleAgentsOffline finds stale agents and marks them offline.
func MarkStaleAgentsOffline(ctx context.Context, reg *AgentRegistry, maxAge time.Duration) []AgentCard {
	stale := FindStaleAgents(ctx, reg, maxAge)
	for _, a := range stale {
		reg.UpdateStatus(ctx, a.ID, AgentStatusOffline, "")
	}
	return stale
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./cluster/ -v`
Expected: All PASS

**Step 5: Commit**

```bash
git add cluster/
git commit -m "feat(cluster): heartbeat monitoring and stale agent detection"
```

---

### Task 2.3: Phase 2 review

Request a code review from Codex on the agent registry and heartbeat system. Verify:
- Agent cards serialize/deserialize correctly
- KV operations are idempotent and handle missing keys
- Stale detection threshold works correctly
- No race conditions in registry operations

---

## Phase 3: Task Model & Queue

### Task 3.1: Task model and JetStream task queue

**Files:**
- Create: `cluster/task.go`
- Create: `cluster/task_test.go`

**Step 1: Write the test for task submission, claiming, and completion**

```go
// cluster/task_test.go
package cluster

import (
	"context"
	"testing"
	"time"
)

func TestTaskSubmitAndClaim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ns, err := StartEmbeddedNATS(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Shutdown()

	nc, err := Connect(ctx, ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	js, err := SetupJetStream(ctx, nc)
	if err != nil {
		t.Fatal(err)
	}

	tq := NewTaskQueue(js, nc)

	task := Task{
		ID:             "task-1",
		Type:           TaskTypeImplement,
		Specialization: []string{"backend", "go"},
		Priority:       1,
		Title:          "Add pagination",
		Description:    "Add pagination to /api/users",
		CreatedBy:      "orchestrator",
		Context: TaskContext{
			Repo:       "github.com/test/repo",
			BaseBranch: "main",
			FilesHint:  []string{"server/users.go"},
		},
	}

	if err := tq.Submit(ctx, task); err != nil {
		t.Fatal(err)
	}

	got, err := tq.Get(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != TaskStatusSubmitted {
		t.Fatalf("got status %q, want %q", got.Status, TaskStatusSubmitted)
	}

	if err := tq.Claim(ctx, "task-1", "agent-1"); err != nil {
		t.Fatal(err)
	}

	got, err = tq.Get(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != TaskStatusAssigned {
		t.Fatalf("got status %q, want %q", got.Status, TaskStatusAssigned)
	}
	if got.AssignedTo != "agent-1" {
		t.Fatalf("got assigned_to %q, want %q", got.AssignedTo, "agent-1")
	}
}

func TestTaskComplete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ns, err := StartEmbeddedNATS(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Shutdown()

	nc, err := Connect(ctx, ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	js, err := SetupJetStream(ctx, nc)
	if err != nil {
		t.Fatal(err)
	}

	tq := NewTaskQueue(js, nc)

	task := Task{
		ID:        "task-2",
		Type:      TaskTypeImplement,
		Title:     "Test task",
		CreatedBy: "orchestrator",
	}

	if err := tq.Submit(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := tq.Claim(ctx, "task-2", "agent-1"); err != nil {
		t.Fatal(err)
	}

	result := TaskResult{
		Branch:  "agent/agent-1/task-2",
		Summary: "Added pagination successfully",
	}
	if err := tq.Complete(ctx, "task-2", result); err != nil {
		t.Fatal(err)
	}

	got, err := tq.Get(ctx, "task-2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != TaskStatusCompleted {
		t.Fatalf("got status %q, want %q", got.Status, TaskStatusCompleted)
	}
	if got.Result.Branch != "agent/agent-1/task-2" {
		t.Fatalf("got branch %q, want %q", got.Result.Branch, "agent/agent-1/task-2")
	}
}

func TestTaskDoubleClaimFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ns, err := StartEmbeddedNATS(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Shutdown()

	nc, err := Connect(ctx, ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	js, err := SetupJetStream(ctx, nc)
	if err != nil {
		t.Fatal(err)
	}

	tq := NewTaskQueue(js, nc)

	task := Task{
		ID:        "task-3",
		Type:      TaskTypeImplement,
		Title:     "Contested task",
		CreatedBy: "orchestrator",
	}

	if err := tq.Submit(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := tq.Claim(ctx, "task-3", "agent-1"); err != nil {
		t.Fatal(err)
	}

	// Second claim should fail
	err = tq.Claim(ctx, "task-3", "agent-2")
	if err == nil {
		t.Fatal("expected error on double claim")
	}
}

func TestTaskListByStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ns, err := StartEmbeddedNATS(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Shutdown()

	nc, err := Connect(ctx, ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	js, err := SetupJetStream(ctx, nc)
	if err != nil {
		t.Fatal(err)
	}

	tq := NewTaskQueue(js, nc)

	for i, title := range []string{"task-a", "task-b", "task-c"} {
		tq.Submit(ctx, Task{
			ID:        title,
			Type:      TaskTypeImplement,
			Title:     title,
			Priority:  i,
			CreatedBy: "orchestrator",
		})
	}
	tq.Claim(ctx, "task-a", "agent-1")

	submitted, err := tq.ListByStatus(ctx, TaskStatusSubmitted)
	if err != nil {
		t.Fatal(err)
	}
	if len(submitted) != 2 {
		t.Fatalf("got %d submitted tasks, want 2", len(submitted))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cluster/ -run TestTask -v`
Expected: FAIL

**Step 3: Write the implementation**

```go
// cluster/task.go
package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type TaskStatus string

const (
	TaskStatusSubmitted     TaskStatus = "submitted"
	TaskStatusAssigned      TaskStatus = "assigned"
	TaskStatusWorking       TaskStatus = "working"
	TaskStatusCompleted     TaskStatus = "completed"
	TaskStatusFailed        TaskStatus = "failed"
	TaskStatusInputRequired TaskStatus = "input_required"
)

type TaskType string

const (
	TaskTypeImplement TaskType = "implement"
	TaskTypeReview    TaskType = "review"
	TaskTypeTest      TaskType = "test"
	TaskTypeRefactor  TaskType = "refactor"
)

type TaskContext struct {
	Repo       string   `json:"repo"`
	BaseBranch string   `json:"base_branch"`
	FilesHint  []string `json:"files_hint,omitempty"`
}

type TaskResult struct {
	Branch  string `json:"branch"`
	Summary string `json:"summary"`
}

type Task struct {
	ID             string      `json:"id"`
	ParentID       string      `json:"parent_id,omitempty"`
	Type           TaskType    `json:"type"`
	Specialization []string    `json:"specialization,omitempty"`
	Priority       int         `json:"priority"`
	Status         TaskStatus  `json:"status"`
	AssignedTo     string      `json:"assigned_to,omitempty"`
	CreatedBy      string      `json:"created_by"`
	Title          string      `json:"title"`
	Description    string      `json:"description,omitempty"`
	Context        TaskContext `json:"context"`
	Result         TaskResult  `json:"result,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

const BucketTasks = "tasks"

type TaskQueue struct {
	js jetstream.JetStream
	nc *nats.Conn
}

func NewTaskQueue(js jetstream.JetStream, nc *nats.Conn) *TaskQueue {
	return &TaskQueue{js: js, nc: nc}
}

func (tq *TaskQueue) taskKV(ctx context.Context) (jetstream.KeyValue, error) {
	return tq.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: BucketTasks,
	})
}

func (tq *TaskQueue) Submit(ctx context.Context, task Task) error {
	kv, err := tq.taskKV(ctx)
	if err != nil {
		return err
	}

	now := time.Now()
	task.Status = TaskStatusSubmitted
	task.CreatedAt = now
	task.UpdatedAt = now

	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}

	if _, err := kv.Put(ctx, task.ID, data); err != nil {
		return fmt.Errorf("put task: %w", err)
	}

	// Publish event for subscribers
	tq.nc.Publish("task."+task.ID+".status", data)
	return nil
}

func (tq *TaskQueue) Get(ctx context.Context, taskID string) (*Task, error) {
	kv, err := tq.taskKV(ctx)
	if err != nil {
		return nil, err
	}

	entry, err := kv.Get(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("get task %s: %w", taskID, err)
	}

	var task Task
	if err := json.Unmarshal(entry.Value(), &task); err != nil {
		return nil, fmt.Errorf("unmarshal task: %w", err)
	}
	return &task, nil
}

func (tq *TaskQueue) Claim(ctx context.Context, taskID string, agentID string) error {
	kv, err := tq.taskKV(ctx)
	if err != nil {
		return err
	}

	entry, err := kv.Get(ctx, taskID)
	if err != nil {
		return fmt.Errorf("get task %s: %w", taskID, err)
	}

	var task Task
	if err := json.Unmarshal(entry.Value(), &task); err != nil {
		return fmt.Errorf("unmarshal task: %w", err)
	}

	if task.Status != TaskStatusSubmitted {
		return fmt.Errorf("task %s is %s, not claimable", taskID, task.Status)
	}

	task.Status = TaskStatusAssigned
	task.AssignedTo = agentID
	task.UpdatedAt = time.Now()

	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}

	// CAS: only update if revision matches (prevents double-claim)
	if _, err := kv.Update(ctx, taskID, data, entry.Revision()); err != nil {
		return fmt.Errorf("claim task %s (CAS failed): %w", taskID, err)
	}

	tq.nc.Publish("task."+task.ID+".status", data)
	return nil
}

func (tq *TaskQueue) Complete(ctx context.Context, taskID string, result TaskResult) error {
	return tq.updateStatus(ctx, taskID, TaskStatusCompleted, result)
}

func (tq *TaskQueue) Fail(ctx context.Context, taskID string, result TaskResult) error {
	return tq.updateStatus(ctx, taskID, TaskStatusFailed, result)
}

func (tq *TaskQueue) updateStatus(ctx context.Context, taskID string, status TaskStatus, result TaskResult) error {
	kv, err := tq.taskKV(ctx)
	if err != nil {
		return err
	}

	entry, err := kv.Get(ctx, taskID)
	if err != nil {
		return fmt.Errorf("get task %s: %w", taskID, err)
	}

	var task Task
	if err := json.Unmarshal(entry.Value(), &task); err != nil {
		return fmt.Errorf("unmarshal task: %w", err)
	}

	task.Status = status
	task.Result = result
	task.UpdatedAt = time.Now()

	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}

	if _, err := kv.Update(ctx, taskID, data, entry.Revision()); err != nil {
		return fmt.Errorf("update task %s: %w", taskID, err)
	}

	tq.nc.Publish("task."+task.ID+".status", data)
	return nil
}

func (tq *TaskQueue) ListByStatus(ctx context.Context, status TaskStatus) ([]Task, error) {
	kv, err := tq.taskKV(ctx)
	if err != nil {
		return nil, err
	}

	keys, err := kv.Keys(ctx)
	if err != nil {
		if err.Error() == "nats: no keys found" {
			return nil, nil
		}
		return nil, fmt.Errorf("list task keys: %w", err)
	}

	var tasks []Task
	for _, key := range keys {
		entry, err := kv.Get(ctx, key)
		if err != nil {
			continue
		}
		var task Task
		if err := json.Unmarshal(entry.Value(), &task); err != nil {
			continue
		}
		if task.Status == status {
			tasks = append(tasks, task)
		}
	}
	return tasks, nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./cluster/ -run TestTask -v`
Expected: All PASS

**Step 5: Commit**

```bash
git add cluster/
git commit -m "feat(cluster): task model with KV-backed queue and CAS-based claiming"
```

---

### Task 3.2: Phase 3 review

Request a code review from Codex. Verify:
- Task state transitions are valid (can't claim a completed task, etc.)
- CAS-based claiming prevents double-claim race condition
- Status updates publish events for subscribers
- ListByStatus filters correctly

---

## Phase 4: Git Coordination & File Locking

### Task 4.1: File lock manager

**Files:**
- Create: `cluster/locks.go`
- Create: `cluster/locks_test.go`

**Step 1: Write the test for file lock acquire, release, and contention**

```go
// cluster/locks_test.go
package cluster

import (
	"context"
	"testing"
	"time"
)

func TestLockAcquireAndRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ns, err := StartEmbeddedNATS(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Shutdown()

	nc, err := Connect(ctx, ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	js, err := SetupJetStream(ctx, nc)
	if err != nil {
		t.Fatal(err)
	}

	lm := NewLockManager(js)

	err = lm.Acquire(ctx, "github.com/test/repo", "server/users.go", "agent-1", "task-1")
	if err != nil {
		t.Fatal(err)
	}

	lock, err := lm.Get(ctx, "github.com/test/repo", "server/users.go")
	if err != nil {
		t.Fatal(err)
	}
	if lock.AgentID != "agent-1" {
		t.Fatalf("got agent %q, want %q", lock.AgentID, "agent-1")
	}

	if err := lm.Release(ctx, "github.com/test/repo", "server/users.go"); err != nil {
		t.Fatal(err)
	}

	_, err = lm.Get(ctx, "github.com/test/repo", "server/users.go")
	if err == nil {
		t.Fatal("expected error after release")
	}
}

func TestLockContention(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ns, err := StartEmbeddedNATS(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Shutdown()

	nc, err := Connect(ctx, ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	js, err := SetupJetStream(ctx, nc)
	if err != nil {
		t.Fatal(err)
	}

	lm := NewLockManager(js)

	// First agent acquires
	err = lm.Acquire(ctx, "repo", "file.go", "agent-1", "task-1")
	if err != nil {
		t.Fatal(err)
	}

	// Second agent cannot acquire same file
	err = lm.Acquire(ctx, "repo", "file.go", "agent-2", "task-2")
	if err == nil {
		t.Fatal("expected error on contention")
	}
}

func TestReleaseByAgent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ns, err := StartEmbeddedNATS(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Shutdown()

	nc, err := Connect(ctx, ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	js, err := SetupJetStream(ctx, nc)
	if err != nil {
		t.Fatal(err)
	}

	lm := NewLockManager(js)

	lm.Acquire(ctx, "repo", "a.go", "agent-1", "task-1")
	lm.Acquire(ctx, "repo", "b.go", "agent-1", "task-1")
	lm.Acquire(ctx, "repo", "c.go", "agent-2", "task-2")

	released, err := lm.ReleaseByAgent(ctx, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if released != 2 {
		t.Fatalf("released %d locks, want 2", released)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cluster/ -run TestLock -v`
Expected: FAIL

**Step 3: Write the implementation**

```go
// cluster/locks.go
package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

type FileLock struct {
	AgentID  string    `json:"agent_id"`
	TaskID   string    `json:"task_id"`
	LockedAt time.Time `json:"locked_at"`
}

type LockManager struct {
	js jetstream.JetStream
}

func NewLockManager(js jetstream.JetStream) *LockManager {
	return &LockManager{js: js}
}

func lockKey(repo, path string) string {
	return strings.ReplaceAll(repo, "/", ".") + ":" + strings.ReplaceAll(path, "/", ".")
}

func (lm *LockManager) Acquire(ctx context.Context, repo, path, agentID, taskID string) error {
	kv, err := lm.js.KeyValue(ctx, BucketLocks)
	if err != nil {
		return fmt.Errorf("get locks bucket: %w", err)
	}

	lock := FileLock{
		AgentID:  agentID,
		TaskID:   taskID,
		LockedAt: time.Now(),
	}

	data, err := json.Marshal(lock)
	if err != nil {
		return fmt.Errorf("marshal lock: %w", err)
	}

	// Create fails if key already exists -- atomic lock acquisition
	if _, err := kv.Create(ctx, lockKey(repo, path), data); err != nil {
		return fmt.Errorf("file %s already locked: %w", path, err)
	}
	return nil
}

func (lm *LockManager) Release(ctx context.Context, repo, path string) error {
	kv, err := lm.js.KeyValue(ctx, BucketLocks)
	if err != nil {
		return fmt.Errorf("get locks bucket: %w", err)
	}

	if err := kv.Delete(ctx, lockKey(repo, path)); err != nil {
		return fmt.Errorf("release lock %s: %w", path, err)
	}
	return nil
}

func (lm *LockManager) Get(ctx context.Context, repo, path string) (*FileLock, error) {
	kv, err := lm.js.KeyValue(ctx, BucketLocks)
	if err != nil {
		return nil, fmt.Errorf("get locks bucket: %w", err)
	}

	entry, err := kv.Get(ctx, lockKey(repo, path))
	if err != nil {
		return nil, fmt.Errorf("get lock %s: %w", path, err)
	}

	var lock FileLock
	if err := json.Unmarshal(entry.Value(), &lock); err != nil {
		return nil, fmt.Errorf("unmarshal lock: %w", err)
	}
	return &lock, nil
}

// ReleaseByAgent releases all locks held by a specific agent.
// Used when an agent goes offline to free its locks.
func (lm *LockManager) ReleaseByAgent(ctx context.Context, agentID string) (int, error) {
	kv, err := lm.js.KeyValue(ctx, BucketLocks)
	if err != nil {
		return 0, fmt.Errorf("get locks bucket: %w", err)
	}

	keys, err := kv.Keys(ctx)
	if err != nil {
		if err.Error() == "nats: no keys found" {
			return 0, nil
		}
		return 0, fmt.Errorf("list lock keys: %w", err)
	}

	released := 0
	for _, key := range keys {
		entry, err := kv.Get(ctx, key)
		if err != nil {
			continue
		}
		var lock FileLock
		if err := json.Unmarshal(entry.Value(), &lock); err != nil {
			continue
		}
		if lock.AgentID == agentID {
			if err := kv.Delete(ctx, key); err == nil {
				released++
			}
		}
	}
	return released, nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./cluster/ -v`
Expected: All PASS

**Step 5: Commit**

```bash
git add cluster/
git commit -m "feat(cluster): file lock manager with atomic KV-based locking"
```

---

### Task 4.2: Phase 4 review

Request a code review from Codex. Verify:
- Lock acquire is truly atomic (KV Create, not Put)
- ReleaseByAgent cleans up all locks for a dead agent
- Lock key encoding handles repo/path separators correctly
- No TOCTOU races in lock operations

---

## Phase 5: Orchestrator Core

### Task 5.1: Cluster node -- the main integration struct

**Files:**
- Create: `cluster/node.go`
- Create: `cluster/node_test.go`

**Step 1: Write the test for node lifecycle**

```go
// cluster/node_test.go
package cluster

import (
	"context"
	"testing"
	"time"
)

func TestNodeStartStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	node, err := StartNode(ctx, NodeConfig{
		AgentID:      "test-node",
		AgentName:    "test",
		Capabilities: []string{"go"},
		ListenAddr:   ":0", // embedded, random port
		StoreDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Stop()

	// Agent should be registered
	card, err := node.Registry.Get(ctx, "test-node")
	if err != nil {
		t.Fatal(err)
	}
	if card.Status != AgentStatusIdle {
		t.Fatalf("got status %q, want %q", card.Status, AgentStatusIdle)
	}

	// Should be able to submit and claim tasks
	err = node.Tasks.Submit(ctx, Task{
		ID:        "task-1",
		Type:      TaskTypeImplement,
		Title:     "Test",
		CreatedBy: "test-node",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNodeConnectToExisting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start first node with embedded server
	node1, err := StartNode(ctx, NodeConfig{
		AgentID:    "node-1",
		AgentName:  "node-1",
		ListenAddr: ":0",
		StoreDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node1.Stop()

	// Start second node connecting to first
	node2, err := StartNode(ctx, NodeConfig{
		AgentID:   "node-2",
		AgentName: "node-2",
		NATSUrl:   node1.ClientURL(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node2.Stop()

	// Both agents visible
	agents, err := node1.Registry.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cluster/ -run TestNode -v`
Expected: FAIL

**Step 3: Write the implementation**

```go
// cluster/node.go
package cluster

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type NodeConfig struct {
	AgentID      string
	AgentName    string
	Capabilities []string
	ListenAddr   string // non-empty = start embedded NATS
	NATSUrl      string // non-empty = connect to external NATS
	StoreDir     string // JetStream storage directory (embedded only)
	Logger       *slog.Logger
}

// Node represents this Percy instance's connection to the cluster.
type Node struct {
	config   NodeConfig
	embedded *EmbeddedNATS
	nc       *nats.Conn
	js       jetstream.JetStream

	Registry *AgentRegistry
	Tasks    *TaskQueue
	Locks    *LockManager
}

// StartNode joins the cluster, either by starting an embedded NATS server
// or connecting to an existing one.
func StartNode(ctx context.Context, cfg NodeConfig) (*Node, error) {
	node := &Node{config: cfg}

	// Determine NATS URL
	natsURL := cfg.NATSUrl
	if cfg.ListenAddr != "" {
		port := 0
		if cfg.ListenAddr != ":0" {
			// Parse port from ":4222" format
			fmt.Sscanf(cfg.ListenAddr, ":%d", &port)
		}

		ns, err := StartEmbeddedNATS(cfg.StoreDir, port)
		if err != nil {
			return nil, fmt.Errorf("start embedded nats: %w", err)
		}
		node.embedded = ns
		natsURL = ns.ClientURL()
	}

	if natsURL == "" {
		return nil, fmt.Errorf("either ListenAddr or NATSUrl must be set")
	}

	// Connect
	nc, err := Connect(ctx, natsURL)
	if err != nil {
		if node.embedded != nil {
			node.embedded.Shutdown()
		}
		return nil, err
	}
	node.nc = nc

	// Setup JetStream
	js, err := SetupJetStream(ctx, nc)
	if err != nil {
		nc.Close()
		if node.embedded != nil {
			node.embedded.Shutdown()
		}
		return nil, err
	}
	node.js = js

	// Initialize components
	node.Registry = NewAgentRegistry(js)
	node.Tasks = NewTaskQueue(js, nc)
	node.Locks = NewLockManager(js)

	// Register self
	card := AgentCard{
		ID:           cfg.AgentID,
		Name:         cfg.AgentName,
		Capabilities: cfg.Capabilities,
	}
	if err := node.Registry.Register(ctx, card); err != nil {
		node.Stop()
		return nil, fmt.Errorf("register agent: %w", err)
	}

	return node, nil
}

// ClientURL returns the NATS URL for other nodes to connect to.
// Only valid when running in embedded mode.
func (n *Node) ClientURL() string {
	if n.embedded != nil {
		return n.embedded.ClientURL()
	}
	return n.config.NATSUrl
}

// Stop gracefully shuts down the node.
func (n *Node) Stop() {
	ctx := context.Background()
	if n.nc != nil && n.nc.IsConnected() {
		n.Registry.Deregister(ctx, n.config.AgentID)
		n.nc.Close()
	}
	if n.embedded != nil {
		n.embedded.Shutdown()
	}
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./cluster/ -v`
Expected: All PASS

**Step 5: Commit**

```bash
git add cluster/
git commit -m "feat(cluster): Node struct integrating embedded NATS, registry, tasks, and locks"
```

---

### Task 5.2: Orchestrator -- task planning and dependency management

**Files:**
- Create: `cluster/orchestrator.go`
- Create: `cluster/orchestrator_test.go`

**Step 1: Write the test for dependency-aware task scheduling**

```go
// cluster/orchestrator_test.go
package cluster

import (
	"context"
	"testing"
	"time"
)

func TestOrchestratorDependencyScheduling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	node, err := StartNode(ctx, NodeConfig{
		AgentID:    "orchestrator",
		AgentName:  "orchestrator",
		ListenAddr: ":0",
		StoreDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Stop()

	orch := NewOrchestrator(node)

	// Submit a plan with dependencies
	plan := TaskPlan{
		Tasks: []PlannedTask{
			{Task: Task{ID: "t1", Title: "Add JWT library", Type: TaskTypeImplement, Specialization: []string{"backend"}}},
			{Task: Task{ID: "t2", Title: "Update login", Type: TaskTypeImplement}, DependsOn: []string{"t1"}},
			{Task: Task{ID: "t3", Title: "Update frontend", Type: TaskTypeImplement}, DependsOn: []string{"t1"}},
			{Task: Task{ID: "t4", Title: "Integration tests", Type: TaskTypeTest}, DependsOn: []string{"t2", "t3"}},
		},
	}

	if err := orch.SubmitPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}

	// Only t1 should be submitted (no deps)
	submitted, _ := node.Tasks.ListByStatus(ctx, TaskStatusSubmitted)
	if len(submitted) != 1 {
		t.Fatalf("got %d submitted tasks, want 1", len(submitted))
	}
	if submitted[0].ID != "t1" {
		t.Fatalf("got submitted task %q, want t1", submitted[0].ID)
	}

	// Complete t1
	node.Tasks.Claim(ctx, "t1", "agent-1")
	node.Tasks.Complete(ctx, "t1", TaskResult{Branch: "agent/agent-1/t1"})

	// Resolve deps -- should unblock t2 and t3
	unblocked, err := orch.ResolveDependencies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unblocked) != 2 {
		t.Fatalf("got %d unblocked tasks, want 2", len(unblocked))
	}

	submitted, _ = node.Tasks.ListByStatus(ctx, TaskStatusSubmitted)
	if len(submitted) != 2 {
		t.Fatalf("got %d submitted tasks, want 2", len(submitted))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cluster/ -run TestOrchestrator -v`
Expected: FAIL

**Step 3: Write the implementation**

```go
// cluster/orchestrator.go
package cluster

import (
	"context"
	"fmt"
)

type PlannedTask struct {
	Task      Task     `json:"task"`
	DependsOn []string `json:"depends_on,omitempty"`
}

type TaskPlan struct {
	Tasks []PlannedTask `json:"tasks"`
}

type Orchestrator struct {
	node *Node
	plan *TaskPlan
}

func NewOrchestrator(node *Node) *Orchestrator {
	return &Orchestrator{node: node}
}

// SubmitPlan stores the plan and submits all tasks with no dependencies.
func (o *Orchestrator) SubmitPlan(ctx context.Context, plan TaskPlan) error {
	o.plan = &plan

	for _, pt := range plan.Tasks {
		if len(pt.DependsOn) == 0 {
			task := pt.Task
			task.CreatedBy = o.node.config.AgentID
			if err := o.node.Tasks.Submit(ctx, task); err != nil {
				return fmt.Errorf("submit task %s: %w", task.ID, err)
			}
		}
	}
	return nil
}

// ResolveDependencies checks completed tasks and submits newly unblocked tasks.
// Returns the list of tasks that were unblocked and submitted.
func (o *Orchestrator) ResolveDependencies(ctx context.Context) ([]Task, error) {
	if o.plan == nil {
		return nil, nil
	}

	// Build set of completed task IDs
	completed, err := o.node.Tasks.ListByStatus(ctx, TaskStatusCompleted)
	if err != nil {
		return nil, err
	}
	completedSet := make(map[string]bool, len(completed))
	for _, t := range completed {
		completedSet[t.ID] = true
	}

	// Build set of already-submitted/assigned/working task IDs
	existing := make(map[string]bool)
	for _, status := range []TaskStatus{TaskStatusSubmitted, TaskStatusAssigned, TaskStatusWorking, TaskStatusCompleted} {
		tasks, _ := o.node.Tasks.ListByStatus(ctx, status)
		for _, t := range tasks {
			existing[t.ID] = true
		}
	}

	var unblocked []Task
	for _, pt := range o.plan.Tasks {
		if existing[pt.Task.ID] {
			continue // already in the system
		}
		if len(pt.DependsOn) == 0 {
			continue // already submitted in SubmitPlan
		}

		// Check if all deps are completed
		allDone := true
		for _, dep := range pt.DependsOn {
			if !completedSet[dep] {
				allDone = false
				break
			}
		}

		if allDone {
			task := pt.Task
			task.CreatedBy = o.node.config.AgentID
			if err := o.node.Tasks.Submit(ctx, task); err != nil {
				return nil, fmt.Errorf("submit unblocked task %s: %w", task.ID, err)
			}
			unblocked = append(unblocked, task)
		}
	}

	return unblocked, nil
}

// PendingTasks returns plan tasks that haven't been submitted yet.
func (o *Orchestrator) PendingTasks() []PlannedTask {
	if o.plan == nil {
		return nil
	}
	// This is a simple accessor; actual filtering happens in ResolveDependencies
	var pending []PlannedTask
	for _, pt := range o.plan.Tasks {
		if len(pt.DependsOn) > 0 {
			pending = append(pending, pt)
		}
	}
	return pending
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./cluster/ -v`
Expected: All PASS

**Step 5: Commit**

```bash
git add cluster/
git commit -m "feat(cluster): orchestrator with dependency-aware task scheduling"
```

---

### Task 5.3: Phase 5 review

Request a code review from Codex. Verify:
- Node lifecycle (start/stop) cleans up all resources
- Orchestrator correctly identifies unblocked tasks
- No tasks submitted prematurely (before deps complete)
- ResolveDependencies is idempotent (safe to call repeatedly)

---

## Phase 6: CLI Integration

### Task 6.1: Add --cluster flag to serve command

**Files:**
- Modify: `cmd/percy/main.go`

**Step 1: Add cluster flags to serve FlagSet**

In `runServe()`, add to the serve FlagSet (after existing flags around line 84):

```go
clusterAddr := serveFlags.String("cluster", "", "NATS cluster address. ':PORT' to embed, 'nats://host:port' to connect")
agentName   := serveFlags.String("agent-name", "", "Agent name in cluster")
capabilities := serveFlags.String("capabilities", "", "Comma-separated agent capabilities")
```

**Step 2: After server creation, conditionally start cluster node**

After `server.NewServer(...)` but before `svr.Start()`:

```go
if *clusterAddr != "" {
    cfg := cluster.NodeConfig{
        AgentID:      generateAgentID(),
        AgentName:    *agentName,
        Capabilities: strings.Split(*capabilities, ","),
        Logger:       logger,
    }
    if strings.HasPrefix(*clusterAddr, ":") {
        cfg.ListenAddr = *clusterAddr
        cfg.StoreDir = filepath.Join(filepath.Dir(global.DBPath), "nats-data")
    } else {
        cfg.NATSUrl = *clusterAddr
    }

    node, err := cluster.StartNode(context.Background(), cfg)
    if err != nil {
        logger.Error("failed to start cluster node", "error", err)
        os.Exit(1)
    }
    defer node.Stop()
    logger.Info("cluster node started", "agent_id", cfg.AgentID, "nats", *clusterAddr)
}
```

Add helper:

```go
func generateAgentID() string {
    b := make([]byte, 6)
    rand.Read(b)
    return fmt.Sprintf("agent-%x", b)
}
```

**Step 3: Verify build**

Run: `go build ./cmd/percy/`
Expected: Clean build

**Step 4: Commit**

```bash
git add cmd/percy/
git commit -m "feat(cmd): add --cluster, --agent-name, --capabilities flags to serve"
```

---

### Task 6.2: Add orchestrate subcommand

**Files:**
- Modify: `cmd/percy/main.go`

**Step 1: Add orchestrate case to subcommand switch**

In the subcommand switch (around line 68), add:

```go
case "orchestrate":
    runOrchestrate(global)
```

**Step 2: Implement runOrchestrate**

```go
func runOrchestrate(global GlobalConfig) {
    orchFlags := flag.NewFlagSet("orchestrate", flag.ExitOnError)
    clusterAddr := orchFlags.String("cluster", ":4222", "NATS listen address")
    repo := orchFlags.String("repo", "", "Repository to orchestrate")
    orchFlags.Parse(flag.Args()[1:])

    if *repo == "" {
        fmt.Fprintf(os.Stderr, "error: --repo is required\n")
        os.Exit(1)
    }

    logger := setupLogging(global.Debug)

    storeDir := filepath.Join(filepath.Dir(global.DBPath), "nats-data")
    os.MkdirAll(storeDir, 0755)

    node, err := cluster.StartNode(context.Background(), cluster.NodeConfig{
        AgentID:    "orchestrator",
        AgentName:  "orchestrator",
        ListenAddr: *clusterAddr,
        StoreDir:   storeDir,
        Logger:     logger,
    })
    if err != nil {
        logger.Error("failed to start orchestrator", "error", err)
        os.Exit(1)
    }

    logger.Info("orchestrator started", "nats", node.ClientURL(), "repo", *repo)

    // Wait for shutdown signal
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    <-sigCh

    logger.Info("shutting down orchestrator")
    node.Stop()
}
```

**Step 3: Verify build**

Run: `go build ./cmd/percy/`
Expected: Clean build

**Step 4: Commit**

```bash
git add cmd/percy/
git commit -m "feat(cmd): add orchestrate subcommand for cluster coordination"
```

---

### Task 6.3: Phase 6 review

Request a code review from Codex. Verify:
- CLI flags parse correctly for both serve and orchestrate
- Embedded vs. external NATS mode works based on address format
- Graceful shutdown in orchestrate mode
- No changes to default (non-cluster) behavior

---

## Phase 7: Server Integration

### Task 7.1: Wire cluster node into Server struct

**Files:**
- Modify: `server/server.go`

**Step 1: Add cluster node field to Server struct**

Add to the Server struct (around line 237):

```go
clusterNode *cluster.Node
```

**Step 2: Add SetClusterNode method**

```go
func (s *Server) SetClusterNode(node *cluster.Node) {
    s.clusterNode = node
}
```

**Step 3: Add cluster status API endpoint**

In `RegisterRoutes`, add:

```go
mux.HandleFunc("GET /api/cluster/status", s.handleClusterStatus)
```

Implement:

```go
func (s *Server) handleClusterStatus(w http.ResponseWriter, r *http.Request) {
    if s.clusterNode == nil {
        http.Error(w, "not in cluster mode", http.StatusNotFound)
        return
    }

    ctx := r.Context()
    agents, err := s.clusterNode.Registry.List(ctx)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    submitted, _ := s.clusterNode.Tasks.ListByStatus(ctx, cluster.TaskStatusSubmitted)
    working, _ := s.clusterNode.Tasks.ListByStatus(ctx, cluster.TaskStatusWorking)
    completed, _ := s.clusterNode.Tasks.ListByStatus(ctx, cluster.TaskStatusCompleted)

    status := map[string]any{
        "agents": agents,
        "tasks": map[string]any{
            "submitted": len(submitted),
            "working":   len(working),
            "completed": len(completed),
        },
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(status)
}
```

**Step 4: Verify build**

Run: `go build ./...`
Expected: Clean build

**Step 5: Commit**

```bash
git add server/
git commit -m "feat(server): wire cluster node with status API endpoint"
```

---

### Task 7.2: Report loop completion to cluster

**Files:**
- Modify: `server/convo.go`

**Step 1: Extend onConversationDone to report task completion**

In `ensureLoop` (around line 516 where `onConversationDone` is called), add cluster task completion before the existing memory indexing call:

```go
// In the loop completion goroutine, before calling onConversationDone:
if cm.clusterTaskID != "" && s.clusterNode != nil {
    result := cluster.TaskResult{
        Branch:  currentBranch(), // helper to get current git branch
        Summary: lastAssistantMessage(history),
    }
    s.clusterNode.Tasks.Complete(ctx, cm.clusterTaskID, result)
    s.clusterNode.Locks.ReleaseByAgent(ctx, s.clusterNode.config.AgentID)
}
```

Add `clusterTaskID` field to ConversationManager struct.

**Step 2: Verify build**

Run: `go build ./...`
Expected: Clean build

**Step 3: Commit**

```bash
git add server/
git commit -m "feat(server): report cluster task completion on loop finish"
```

---

### Task 7.3: Phase 7 review

Request a code review from Codex. Verify:
- Cluster mode is fully opt-in (nil checks everywhere)
- Status endpoint returns useful data
- Task completion reported correctly
- No changes to non-cluster code paths

---

## Phase 8: End-to-End Integration Test

### Task 8.1: Multi-node integration test

**Files:**
- Create: `cluster/integration_test.go`

**Step 1: Write end-to-end test with two nodes**

```go
// cluster/integration_test.go
package cluster

import (
	"context"
	"testing"
	"time"
)

func TestIntegrationTwoNodeTaskFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start orchestrator node (embedded NATS)
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

	// Start worker node
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

	// Verify both agents registered
	agents, err := orchNode.Registry.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}

	// Orchestrator submits a plan
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

	// Worker claims t1 from its own node connection
	err = workerNode.Tasks.Claim(ctx, "t1", "worker-1")
	if err != nil {
		t.Fatal(err)
	}

	// Worker completes t1
	err = workerNode.Tasks.Complete(ctx, "t1", TaskResult{Branch: "agent/worker-1/t1", Summary: "done"})
	if err != nil {
		t.Fatal(err)
	}

	// Orchestrator resolves deps -- t2 should be unblocked
	unblocked, err := orch.ResolveDependencies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unblocked) != 1 {
		t.Fatalf("got %d unblocked, want 1", len(unblocked))
	}
	if unblocked[0].ID != "t2" {
		t.Fatalf("got unblocked %q, want t2", unblocked[0].ID)
	}

	// Worker claims and completes t2
	workerNode.Tasks.Claim(ctx, "t2", "worker-1")
	workerNode.Tasks.Complete(ctx, "t2", TaskResult{Branch: "agent/worker-1/t2", Summary: "tests pass"})

	// All tasks completed
	completed, _ := orchNode.Tasks.ListByStatus(ctx, TaskStatusCompleted)
	if len(completed) != 2 {
		t.Fatalf("got %d completed, want 2", len(completed))
	}
}
```

**Step 2: Run test**

Run: `go test ./cluster/ -run TestIntegration -v -timeout 30s`
Expected: PASS

**Step 3: Commit**

```bash
git add cluster/
git commit -m "test(cluster): end-to-end integration test with two-node task flow"
```

---

### Task 8.2: Phase 8 review (final)

Request a final code review from Codex on the entire `cluster/` package and all integration points. Verify:
- Full task lifecycle works across nodes
- Dependency resolution is correct
- No resource leaks (NATS connections, goroutines)
- All tests pass: `go test ./cluster/ -v`
- Build clean: `go build ./...`
- Existing tests unaffected: `go test ./...` (requires `make ui` first)
