package cluster

import (
	"context"
	"testing"
)

// setupTestLockManager starts an embedded NATS server, sets up JetStream, and
// returns a LockManager ready for testing. It registers cleanup via t.
func setupTestLockManager(t *testing.T) (*LockManager, context.Context) {
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

	return NewLockManager(js), ctx
}

func TestLockKey(t *testing.T) {
	tests := []struct {
		repo string
		path string
		want string
	}{
		{
			repo: "github.com/org/project",
			path: "server/users.go",
			want: "github.com.org.project=server.users.go",
		},
		{
			repo: "github.com/tgruben-circuit/percy",
			path: "cluster/locks.go",
			want: "github.com.tgruben-circuit.percy=cluster.locks.go",
		},
		{
			repo: "simple",
			path: "main.go",
			want: "simple=main.go",
		},
	}

	for _, tt := range tests {
		got := lockKey(tt.repo, tt.path)
		if got != tt.want {
			t.Errorf("lockKey(%q, %q) = %q, want %q", tt.repo, tt.path, got, tt.want)
		}
	}
}

func TestAcquireAndGet(t *testing.T) {
	lm, ctx := setupTestLockManager(t)

	repo := "github.com/org/project"
	path := "server/users.go"
	agentID := "agent-1"
	taskID := "task-42"

	if err := lm.Acquire(ctx, repo, path, agentID, taskID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	lock, err := lm.Get(ctx, repo, path)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if lock.AgentID != agentID {
		t.Errorf("AgentID: got %q, want %q", lock.AgentID, agentID)
	}
	if lock.TaskID != taskID {
		t.Errorf("TaskID: got %q, want %q", lock.TaskID, taskID)
	}
	if lock.LockedAt.IsZero() {
		t.Error("LockedAt should not be zero")
	}
}

func TestRelease(t *testing.T) {
	lm, ctx := setupTestLockManager(t)

	repo := "github.com/org/project"
	path := "server/users.go"

	if err := lm.Acquire(ctx, repo, path, "agent-1", "task-1"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	if err := lm.Release(ctx, repo, path); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Get should return an error after release.
	_, err := lm.Get(ctx, repo, path)
	if err == nil {
		t.Fatal("Get after Release: expected error, got nil")
	}
}

func TestContention(t *testing.T) {
	lm, ctx := setupTestLockManager(t)

	repo := "github.com/org/project"
	path := "server/users.go"

	if err := lm.Acquire(ctx, repo, path, "agent-1", "task-1"); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	// Second acquire on the same file should fail.
	err := lm.Acquire(ctx, repo, path, "agent-2", "task-2")
	if err == nil {
		t.Fatal("second Acquire: expected error for contention, got nil")
	}
}

func TestReleaseByAgent(t *testing.T) {
	lm, ctx := setupTestLockManager(t)

	// Agent-1 locks two files.
	if err := lm.Acquire(ctx, "repo-a", "file1.go", "agent-1", "task-1"); err != nil {
		t.Fatalf("Acquire file1: %v", err)
	}
	if err := lm.Acquire(ctx, "repo-a", "file2.go", "agent-1", "task-1"); err != nil {
		t.Fatalf("Acquire file2: %v", err)
	}

	// Agent-2 locks one file.
	if err := lm.Acquire(ctx, "repo-a", "file3.go", "agent-2", "task-2"); err != nil {
		t.Fatalf("Acquire file3: %v", err)
	}

	// Release all locks held by agent-1.
	count, err := lm.ReleaseByAgent(ctx, "agent-1")
	if err != nil {
		t.Fatalf("ReleaseByAgent: %v", err)
	}
	if count != 2 {
		t.Fatalf("ReleaseByAgent count: got %d, want 2", count)
	}

	// Agent-1's locks should be gone.
	if _, err := lm.Get(ctx, "repo-a", "file1.go"); err == nil {
		t.Error("file1.go lock should be released")
	}
	if _, err := lm.Get(ctx, "repo-a", "file2.go"); err == nil {
		t.Error("file2.go lock should be released")
	}

	// Agent-2's lock should still exist.
	lock, err := lm.Get(ctx, "repo-a", "file3.go")
	if err != nil {
		t.Fatalf("Get file3.go: %v", err)
	}
	if lock.AgentID != "agent-2" {
		t.Errorf("file3.go AgentID: got %q, want %q", lock.AgentID, "agent-2")
	}
}

func TestReleaseByAgentEmpty(t *testing.T) {
	lm, ctx := setupTestLockManager(t)

	// Release by an agent with no locks should return 0.
	count, err := lm.ReleaseByAgent(ctx, "agent-nobody")
	if err != nil {
		t.Fatalf("ReleaseByAgent: %v", err)
	}
	if count != 0 {
		t.Fatalf("ReleaseByAgent count: got %d, want 0", count)
	}
}
