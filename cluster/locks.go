package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// FileLock represents an active file lock held by an agent.
type FileLock struct {
	AgentID  string    `json:"agent_id"`
	TaskID   string    `json:"task_id"`
	LockedAt time.Time `json:"locked_at"`
}

// LockManager provides distributed file locking via NATS JetStream KV.
type LockManager struct {
	js jetstream.JetStream
}

// NewLockManager creates a LockManager backed by the given JetStream instance.
// The "locks" KV bucket must already exist (see SetupJetStream).
func NewLockManager(js jetstream.JetStream) *LockManager {
	return &LockManager{js: js}
}

// lockKey encodes a repo and file path into a valid NATS KV key.
// Slashes are replaced with dots and the repo and path are separated by "=".
func lockKey(repo, path string) string {
	r := strings.ReplaceAll(repo, "/", ".")
	p := strings.ReplaceAll(path, "/", ".")
	return r + "=" + p
}

// kv returns a handle to the locks KV bucket.
func (m *LockManager) kv(ctx context.Context) (jetstream.KeyValue, error) {
	kv, err := m.js.KeyValue(ctx, BucketLocks)
	if err != nil {
		return nil, fmt.Errorf("lock manager kv: %w", err)
	}
	return kv, nil
}

// Acquire atomically locks a file. It fails if the file is already locked.
func (m *LockManager) Acquire(ctx context.Context, repo, path, agentID, taskID string) error {
	kv, err := m.kv(ctx)
	if err != nil {
		return err
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

	key := lockKey(repo, path)
	if _, err := kv.Create(ctx, key, data); err != nil {
		return fmt.Errorf("acquire lock %q: %w", key, err)
	}
	return nil
}

// Release removes the lock on a file.
func (m *LockManager) Release(ctx context.Context, repo, path string) error {
	kv, err := m.kv(ctx)
	if err != nil {
		return err
	}

	key := lockKey(repo, path)
	if err := kv.Delete(ctx, key); err != nil {
		return fmt.Errorf("release lock %q: %w", key, err)
	}
	return nil
}

// Get retrieves the lock information for a file.
func (m *LockManager) Get(ctx context.Context, repo, path string) (*FileLock, error) {
	kv, err := m.kv(ctx)
	if err != nil {
		return nil, err
	}

	key := lockKey(repo, path)
	entry, err := kv.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("get lock %q: %w", key, err)
	}

	var lock FileLock
	if err := json.Unmarshal(entry.Value(), &lock); err != nil {
		return nil, fmt.Errorf("unmarshal lock %q: %w", key, err)
	}
	return &lock, nil
}

// ReleaseByAgent releases all locks held by the given agent and returns the
// count of released locks.
func (m *LockManager) ReleaseByAgent(ctx context.Context, agentID string) (int, error) {
	kv, err := m.kv(ctx)
	if err != nil {
		return 0, err
	}

	keys, err := kv.Keys(ctx)
	if errors.Is(err, jetstream.ErrNoKeysFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("list lock keys: %w", err)
	}

	var count int
	for _, key := range keys {
		entry, err := kv.Get(ctx, key)
		if err != nil {
			return count, fmt.Errorf("get lock %q during release-by-agent: %w", key, err)
		}

		var lock FileLock
		if err := json.Unmarshal(entry.Value(), &lock); err != nil {
			return count, fmt.Errorf("unmarshal lock %q during release-by-agent: %w", key, err)
		}

		if lock.AgentID == agentID {
			if err := kv.Delete(ctx, key); err != nil {
				return count, fmt.Errorf("delete lock %q during release-by-agent: %w", key, err)
			}
			count++
		}
	}
	return count, nil
}
