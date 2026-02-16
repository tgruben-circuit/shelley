package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
)

// setupTestTaskQueue starts an embedded NATS server, sets up JetStream, and
// returns a TaskQueue ready for testing. It registers cleanup via t.
func setupTestTaskQueue(t *testing.T) (*TaskQueue, context.Context) {
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

	tq, err := NewTaskQueue(js, nc)
	if err != nil {
		t.Fatalf("NewTaskQueue: %v", err)
	}
	return tq, ctx
}

func TestSubmitAndGet(t *testing.T) {
	tq, ctx := setupTestTaskQueue(t)

	task := Task{
		ID:        "task-1",
		Type:      TaskTypeImplement,
		Priority:  10,
		CreatedBy: "agent-a",
		Title:     "Implement feature X",
		Context: TaskContext{
			Repo:       "github.com/tgruben-circuit/percy",
			BaseBranch: "main",
			FilesHint:  []string{"server/api.go"},
		},
	}

	if err := tq.Submit(ctx, task); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	got, err := tq.Get(ctx, "task-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.ID != "task-1" {
		t.Errorf("ID: got %q, want %q", got.ID, "task-1")
	}
	if got.Type != TaskTypeImplement {
		t.Errorf("Type: got %q, want %q", got.Type, TaskTypeImplement)
	}
	if got.Priority != 10 {
		t.Errorf("Priority: got %d, want %d", got.Priority, 10)
	}
	if got.Status != TaskStatusSubmitted {
		t.Errorf("Status: got %q, want %q", got.Status, TaskStatusSubmitted)
	}
	if got.CreatedBy != "agent-a" {
		t.Errorf("CreatedBy: got %q, want %q", got.CreatedBy, "agent-a")
	}
	if got.Title != "Implement feature X" {
		t.Errorf("Title: got %q, want %q", got.Title, "Implement feature X")
	}
	if got.Context.Repo != "github.com/tgruben-circuit/percy" {
		t.Errorf("Context.Repo: got %q, want %q", got.Context.Repo, "github.com/tgruben-circuit/percy")
	}
	if got.Context.BaseBranch != "main" {
		t.Errorf("Context.BaseBranch: got %q, want %q", got.Context.BaseBranch, "main")
	}
	if len(got.Context.FilesHint) != 1 || got.Context.FilesHint[0] != "server/api.go" {
		t.Errorf("Context.FilesHint: got %v, want %v", got.Context.FilesHint, []string{"server/api.go"})
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should not be zero")
	}
}

func TestClaimSucceeds(t *testing.T) {
	tq, ctx := setupTestTaskQueue(t)

	task := Task{
		ID:        "task-1",
		Type:      TaskTypeReview,
		Priority:  5,
		CreatedBy: "agent-a",
		Title:     "Review PR #42",
		Context:   TaskContext{Repo: "percy", BaseBranch: "main"},
	}
	if err := tq.Submit(ctx, task); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if err := tq.Claim(ctx, "task-1", "agent-b"); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	got, err := tq.Get(ctx, "task-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Status != TaskStatusAssigned {
		t.Errorf("Status: got %q, want %q", got.Status, TaskStatusAssigned)
	}
	if got.AssignedTo != "agent-b" {
		t.Errorf("AssignedTo: got %q, want %q", got.AssignedTo, "agent-b")
	}
}

func TestDoubleClaimFails(t *testing.T) {
	tq, ctx := setupTestTaskQueue(t)

	task := Task{
		ID:        "task-1",
		Type:      TaskTypeTest,
		Priority:  1,
		CreatedBy: "agent-a",
		Title:     "Run tests",
		Context:   TaskContext{Repo: "percy", BaseBranch: "main"},
	}
	if err := tq.Submit(ctx, task); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// First claim succeeds.
	if err := tq.Claim(ctx, "task-1", "agent-b"); err != nil {
		t.Fatalf("first Claim: %v", err)
	}

	// Second claim must fail because the task is no longer submitted.
	err := tq.Claim(ctx, "task-1", "agent-c")
	if err == nil {
		t.Fatal("second Claim: expected error, got nil")
	}
}

func TestCASPreventsRace(t *testing.T) {
	tq, ctx := setupTestTaskQueue(t)

	task := Task{
		ID:        "task-1",
		Type:      TaskTypeImplement,
		Priority:  1,
		CreatedBy: "agent-a",
		Title:     "Race test",
		Context:   TaskContext{Repo: "percy", BaseBranch: "main"},
	}
	if err := tq.Submit(ctx, task); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Simulate two agents reading the task at the same time by using the
	// low-level KV operations. One should succeed, the other should fail.
	kv, err := tq.taskKV(ctx)
	if err != nil {
		t.Fatalf("taskKV: %v", err)
	}

	// Both agents read the entry at the same revision.
	entry1, err := kv.Get(ctx, "task-1")
	if err != nil {
		t.Fatalf("kv.Get (agent-b): %v", err)
	}
	entry2, err := kv.Get(ctx, "task-1")
	if err != nil {
		t.Fatalf("kv.Get (agent-c): %v", err)
	}

	rev1 := entry1.Revision()
	rev2 := entry2.Revision()
	if rev1 != rev2 {
		t.Fatalf("revisions should match: %d != %d", rev1, rev2)
	}

	// Agent-b claims first using CAS update.
	_, err = kv.Update(ctx, "task-1", entry1.Value(), rev1)
	if err != nil {
		t.Fatalf("agent-b Update: %v", err)
	}

	// Agent-c tries with the same old revision -- should fail.
	_, err = kv.Update(ctx, "task-1", entry2.Value(), rev2)
	if err == nil {
		t.Fatal("agent-c Update: expected CAS failure, got nil")
	}
}

func TestCompleteSetsStatusAndResult(t *testing.T) {
	tq, ctx := setupTestTaskQueue(t)

	task := Task{
		ID:        "task-1",
		Type:      TaskTypeImplement,
		Priority:  1,
		CreatedBy: "agent-a",
		Title:     "Build it",
		Context:   TaskContext{Repo: "percy", BaseBranch: "main"},
	}
	if err := tq.Submit(ctx, task); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := tq.Claim(ctx, "task-1", "agent-b"); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	result := TaskResult{
		Branch:  "feature/task-1",
		Summary: "Implemented feature X successfully",
	}
	if err := tq.Complete(ctx, "task-1", result); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	got, err := tq.Get(ctx, "task-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Status != TaskStatusCompleted {
		t.Errorf("Status: got %q, want %q", got.Status, TaskStatusCompleted)
	}
	if got.Result.Branch != "feature/task-1" {
		t.Errorf("Result.Branch: got %q, want %q", got.Result.Branch, "feature/task-1")
	}
	if got.Result.Summary != "Implemented feature X successfully" {
		t.Errorf("Result.Summary: got %q, want %q", got.Result.Summary, "Implemented feature X successfully")
	}
}

func TestFailSetsStatusAndResult(t *testing.T) {
	tq, ctx := setupTestTaskQueue(t)

	task := Task{
		ID:        "task-1",
		Type:      TaskTypeRefactor,
		Priority:  3,
		CreatedBy: "agent-a",
		Title:     "Refactor module",
		Context:   TaskContext{Repo: "percy", BaseBranch: "main"},
	}
	if err := tq.Submit(ctx, task); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := tq.Claim(ctx, "task-1", "agent-b"); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	result := TaskResult{
		Summary: "compilation error in foo.go",
	}
	if err := tq.Fail(ctx, "task-1", result); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	got, err := tq.Get(ctx, "task-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Status != TaskStatusFailed {
		t.Errorf("Status: got %q, want %q", got.Status, TaskStatusFailed)
	}
	if got.Result.Summary != "compilation error in foo.go" {
		t.Errorf("Result.Summary: got %q, want %q", got.Result.Summary, "compilation error in foo.go")
	}
}

func TestListByStatus(t *testing.T) {
	tq, ctx := setupTestTaskQueue(t)

	tasks := []Task{
		{ID: "task-1", Type: TaskTypeImplement, Priority: 1, CreatedBy: "agent-a", Title: "T1", Context: TaskContext{Repo: "percy", BaseBranch: "main"}},
		{ID: "task-2", Type: TaskTypeReview, Priority: 2, CreatedBy: "agent-a", Title: "T2", Context: TaskContext{Repo: "percy", BaseBranch: "main"}},
		{ID: "task-3", Type: TaskTypeTest, Priority: 3, CreatedBy: "agent-a", Title: "T3", Context: TaskContext{Repo: "percy", BaseBranch: "main"}},
	}

	for _, task := range tasks {
		if err := tq.Submit(ctx, task); err != nil {
			t.Fatalf("Submit(%s): %v", task.ID, err)
		}
	}

	// All three should be submitted.
	submitted, err := tq.ListByStatus(ctx, TaskStatusSubmitted)
	if err != nil {
		t.Fatalf("ListByStatus(submitted): %v", err)
	}
	if len(submitted) != 3 {
		t.Fatalf("ListByStatus(submitted): got %d tasks, want 3", len(submitted))
	}

	// Claim one task.
	if err := tq.Claim(ctx, "task-2", "agent-b"); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	// Now only 2 submitted.
	submitted, err = tq.ListByStatus(ctx, TaskStatusSubmitted)
	if err != nil {
		t.Fatalf("ListByStatus(submitted) after claim: %v", err)
	}
	if len(submitted) != 2 {
		t.Fatalf("ListByStatus(submitted) after claim: got %d tasks, want 2", len(submitted))
	}

	// 1 assigned.
	assigned, err := tq.ListByStatus(ctx, TaskStatusAssigned)
	if err != nil {
		t.Fatalf("ListByStatus(assigned): %v", err)
	}
	if len(assigned) != 1 {
		t.Fatalf("ListByStatus(assigned): got %d tasks, want 1", len(assigned))
	}
	if assigned[0].ID != "task-2" {
		t.Errorf("assigned task ID: got %q, want %q", assigned[0].ID, "task-2")
	}

	// 0 completed.
	completed, err := tq.ListByStatus(ctx, TaskStatusCompleted)
	if err != nil {
		t.Fatalf("ListByStatus(completed): %v", err)
	}
	if len(completed) != 0 {
		t.Fatalf("ListByStatus(completed): got %d tasks, want 0", len(completed))
	}
}

func TestListByStatusEmpty(t *testing.T) {
	tq, ctx := setupTestTaskQueue(t)

	got, err := tq.ListByStatus(ctx, TaskStatusSubmitted)
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListByStatus: got %d tasks, want 0", len(got))
	}
}

func TestSubmitPublishesEvent(t *testing.T) {
	tq, ctx := setupTestTaskQueue(t)

	// Subscribe to task status events before submitting.
	sub, err := tq.nc.SubscribeSync("task.task-1.status")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	task := Task{
		ID:        "task-1",
		Type:      TaskTypeImplement,
		Priority:  1,
		CreatedBy: "agent-a",
		Title:     "Event test",
		Context:   TaskContext{Repo: "percy", BaseBranch: "main"},
	}
	if err := tq.Submit(ctx, task); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// We should receive a message on the subject.
	msg, err := sub.NextMsgWithContext(ctx)
	if err != nil {
		t.Fatalf("NextMsg: %v", err)
	}
	if msg == nil {
		t.Fatal("expected a message, got nil")
	}
}

func TestClaimNonSubmittedFails(t *testing.T) {
	tq, ctx := setupTestTaskQueue(t)

	task := Task{
		ID:        "task-1",
		Type:      TaskTypeImplement,
		Priority:  1,
		CreatedBy: "agent-a",
		Title:     "Already completed",
		Context:   TaskContext{Repo: "percy", BaseBranch: "main"},
	}
	if err := tq.Submit(ctx, task); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := tq.Claim(ctx, "task-1", "agent-b"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := tq.Complete(ctx, "task-1", TaskResult{Summary: "done"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Trying to claim a completed task should fail.
	err := tq.Claim(ctx, "task-1", "agent-c")
	if err == nil {
		t.Fatal("Claim on completed task: expected error, got nil")
	}
}

func TestGetNonexistent(t *testing.T) {
	tq, ctx := setupTestTaskQueue(t)

	_, err := tq.Get(ctx, "nonexistent")
	if err == nil {
		t.Fatal("Get nonexistent: expected error, got nil")
	}
	// Should mention the task ID or "get" in the error.
	if !containsAny(err.Error(), "nonexistent", jetstream.ErrKeyNotFound.Error()) {
		t.Errorf("Get nonexistent error: %v", err)
	}
}

// containsAny returns true if s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
