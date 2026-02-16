package cluster

import (
	"context"
	"testing"
)

func TestSetupJetStream(t *testing.T) {
	dir := t.TempDir()
	srv, err := StartEmbeddedNATS(dir, 0)
	if err != nil {
		t.Fatalf("StartEmbeddedNATS: %v", err)
	}
	defer srv.Shutdown()

	ctx := context.Background()
	nc, err := Connect(ctx, srv.ClientURL())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer nc.Close()

	js, err := SetupJetStream(ctx, nc)
	if err != nil {
		t.Fatalf("SetupJetStream: %v", err)
	}

	// Verify all 3 KV buckets exist and can be accessed.
	for _, bucket := range []string{BucketAgents, BucketLocks, BucketCluster} {
		kv, err := js.KeyValue(ctx, bucket)
		if err != nil {
			t.Fatalf("KeyValue(%q): %v", bucket, err)
		}
		if kv == nil {
			t.Fatalf("KeyValue(%q) returned nil", bucket)
		}
	}

	// Verify TASKS stream exists with correct name.
	stream, err := js.Stream(ctx, StreamTasks)
	if err != nil {
		t.Fatalf("Stream(%q): %v", StreamTasks, err)
	}
	info := stream.CachedInfo()
	if info.Config.Name != StreamTasks {
		t.Fatalf("stream name: got %q, want %q", info.Config.Name, StreamTasks)
	}
}

func TestSetupJetStreamIdempotent(t *testing.T) {
	dir := t.TempDir()
	srv, err := StartEmbeddedNATS(dir, 0)
	if err != nil {
		t.Fatalf("StartEmbeddedNATS: %v", err)
	}
	defer srv.Shutdown()

	ctx := context.Background()
	nc, err := Connect(ctx, srv.ClientURL())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer nc.Close()

	// Call SetupJetStream twice; the second call must not error.
	if _, err := SetupJetStream(ctx, nc); err != nil {
		t.Fatalf("first SetupJetStream: %v", err)
	}
	if _, err := SetupJetStream(ctx, nc); err != nil {
		t.Fatalf("second SetupJetStream: %v", err)
	}
}
