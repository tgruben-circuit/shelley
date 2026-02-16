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

// SetupJetStream initializes the JetStream infrastructure required by Percy
// clustering: KV buckets for agent registry, distributed locks, and cluster
// metadata, plus a stream for task distribution. Safe to call multiple times.
func SetupJetStream(ctx context.Context, nc *nats.Conn) (jetstream.JetStream, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("jetstream new: %w", err)
	}

	for _, bucket := range []string{BucketAgents, BucketLocks, BucketCluster} {
		if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
			Bucket: bucket,
		}); err != nil {
			return nil, fmt.Errorf("create kv bucket %q: %w", bucket, err)
		}
	}

	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     StreamTasks,
		Subjects: []string{"task.>"},
	}); err != nil {
		return nil, fmt.Errorf("create stream %q: %w", StreamTasks, err)
	}

	return js, nil
}
