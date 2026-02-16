package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const BucketTasks = "tasks"

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskStatusSubmitted     TaskStatus = "submitted"
	TaskStatusAssigned      TaskStatus = "assigned"
	TaskStatusWorking       TaskStatus = "working"
	TaskStatusCompleted     TaskStatus = "completed"
	TaskStatusFailed        TaskStatus = "failed"
	TaskStatusInputRequired TaskStatus = "input_required"
)

// TaskType represents the kind of work a task describes.
type TaskType string

const (
	TaskTypeImplement TaskType = "implement"
	TaskTypeReview    TaskType = "review"
	TaskTypeTest      TaskType = "test"
	TaskTypeRefactor  TaskType = "refactor"
)

// TaskContext provides repository and file context for a task.
type TaskContext struct {
	Repo       string   `json:"repo"`
	BaseBranch string   `json:"base_branch"`
	FilesHint  []string `json:"files_hint,omitempty"`
}

// TaskResult holds the outcome of a completed or failed task.
type TaskResult struct {
	Branch  string `json:"branch"`
	Summary string `json:"summary"`
}

// Task represents a unit of work in the Percy cluster.
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
	Result         TaskResult `json:"result,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

// TaskQueue manages tasks via NATS JetStream KV with CAS-based claiming.
type TaskQueue struct {
	js jetstream.JetStream
	nc *nats.Conn
}

// NewTaskQueue creates a new TaskQueue backed by the given JetStream instance.
// It ensures the tasks KV bucket exists (idempotent).
func NewTaskQueue(js jetstream.JetStream, nc *nats.Conn) (*TaskQueue, error) {
	return &TaskQueue{js: js, nc: nc}, nil
}

// taskKV returns a handle to the tasks KV bucket, creating it if needed.
func (q *TaskQueue) taskKV(ctx context.Context) (jetstream.KeyValue, error) {
	kv, err := q.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: BucketTasks,
	})
	if err != nil {
		return nil, fmt.Errorf("task queue kv: %w", err)
	}
	return kv, nil
}

// Submit stores a task in the KV bucket with status=submitted and publishes an
// event to task.{id}.status.
func (q *TaskQueue) Submit(ctx context.Context, task Task) error {
	now := time.Now()
	task.Status = TaskStatusSubmitted
	task.CreatedAt = now
	task.UpdatedAt = now

	kv, err := q.taskKV(ctx)
	if err != nil {
		return err
	}

	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task %q: %w", task.ID, err)
	}

	if _, err := kv.Put(ctx, task.ID, data); err != nil {
		return fmt.Errorf("put task %q: %w", task.ID, err)
	}

	if err := q.nc.Publish(fmt.Sprintf("task.%s.status", task.ID), data); err != nil {
		return fmt.Errorf("publish task %q status: %w", task.ID, err)
	}

	return nil
}

// Get retrieves a task from the KV bucket by ID.
func (q *TaskQueue) Get(ctx context.Context, taskID string) (*Task, error) {
	kv, err := q.taskKV(ctx)
	if err != nil {
		return nil, err
	}

	entry, err := kv.Get(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("get task %q: %w", taskID, err)
	}

	var task Task
	if err := json.Unmarshal(entry.Value(), &task); err != nil {
		return nil, fmt.Errorf("unmarshal task %q: %w", taskID, err)
	}
	return &task, nil
}

// Claim assigns a task to an agent. It verifies the task has status=submitted
// and uses CAS (compare-and-swap) via KV Update with the entry's revision to
// prevent double-claiming.
func (q *TaskQueue) Claim(ctx context.Context, taskID, agentID string) error {
	kv, err := q.taskKV(ctx)
	if err != nil {
		return err
	}

	entry, err := kv.Get(ctx, taskID)
	if err != nil {
		return fmt.Errorf("claim get task %q: %w", taskID, err)
	}

	var task Task
	if err := json.Unmarshal(entry.Value(), &task); err != nil {
		return fmt.Errorf("claim unmarshal task %q: %w", taskID, err)
	}

	if task.Status != TaskStatusSubmitted {
		return fmt.Errorf("claim task %q: status is %q, want %q", taskID, task.Status, TaskStatusSubmitted)
	}

	task.Status = TaskStatusAssigned
	task.AssignedTo = agentID
	task.UpdatedAt = time.Now()

	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("claim marshal task %q: %w", taskID, err)
	}

	if _, err := kv.Update(ctx, taskID, data, entry.Revision()); err != nil {
		return fmt.Errorf("claim update task %q: %w", taskID, err)
	}

	return nil
}

// Complete marks a task as completed and stores the result.
func (q *TaskQueue) Complete(ctx context.Context, taskID string, result TaskResult) error {
	return q.setResult(ctx, taskID, TaskStatusCompleted, result)
}

// Fail marks a task as failed and stores the result.
func (q *TaskQueue) Fail(ctx context.Context, taskID string, result TaskResult) error {
	return q.setResult(ctx, taskID, TaskStatusFailed, result)
}

// setResult updates a task's status and result.
func (q *TaskQueue) setResult(ctx context.Context, taskID string, status TaskStatus, result TaskResult) error {
	kv, err := q.taskKV(ctx)
	if err != nil {
		return err
	}

	entry, err := kv.Get(ctx, taskID)
	if err != nil {
		return fmt.Errorf("%s get task %q: %w", status, taskID, err)
	}

	var task Task
	if err := json.Unmarshal(entry.Value(), &task); err != nil {
		return fmt.Errorf("%s unmarshal task %q: %w", status, taskID, err)
	}

	task.Status = status
	task.Result = result
	task.UpdatedAt = time.Now()

	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("%s marshal task %q: %w", status, taskID, err)
	}

	if _, err := kv.Update(ctx, taskID, data, entry.Revision()); err != nil {
		return fmt.Errorf("%s update task %q: %w", status, taskID, err)
	}

	return nil
}

// ListByStatus returns all tasks with the given status. Returns an empty slice
// (not nil) if no tasks match.
func (q *TaskQueue) ListByStatus(ctx context.Context, status TaskStatus) ([]Task, error) {
	kv, err := q.taskKV(ctx)
	if err != nil {
		return nil, err
	}

	keys, err := kv.Keys(ctx)
	if errors.Is(err, jetstream.ErrNoKeysFound) {
		return []Task{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list task keys: %w", err)
	}

	var tasks []Task
	for _, key := range keys {
		entry, err := kv.Get(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("get task %q during list: %w", key, err)
		}
		var task Task
		if err := json.Unmarshal(entry.Value(), &task); err != nil {
			return nil, fmt.Errorf("unmarshal task %q during list: %w", key, err)
		}
		if task.Status == status {
			tasks = append(tasks, task)
		}
	}
	if tasks == nil {
		return []Task{}, nil
	}
	return tasks, nil
}
