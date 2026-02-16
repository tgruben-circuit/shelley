package memory_test

import (
	"context"
	"testing"

	"github.com/tgruben-circuit/percy/memory"
)

// fixedEmbedder returns the same vector for every text input.
type fixedEmbedder struct {
	vec []float32
}

func (f *fixedEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vecs := make([][]float32, len(texts))
	for i := range texts {
		vecs[i] = f.vec
	}
	return vecs, nil
}

func (f *fixedEmbedder) Dimension() int { return len(f.vec) }

func TestAssignCellsToTopics(t *testing.T) {
	mdb := openTestDB(t)

	// Pre-create a topic "authentication".
	err := mdb.UpsertTopic(memory.Topic{
		TopicID: "topic_auth",
		Name:    "authentication",
	})
	if err != nil {
		t.Fatal(err)
	}

	cells := []memory.ExtractedCell{
		{CellType: "fact", Salience: 0.8, Content: "JWT tokens expire after 1 hour", TopicHint: "authentication"},
		{CellType: "decision", Salience: 0.9, Content: "Use React Router for navigation", TopicHint: "frontend"},
	}

	assigned, err := memory.AssignCellsToTopics(context.Background(), mdb, cells, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(assigned) != 2 {
		t.Fatalf("expected 2 assigned cells, got %d", len(assigned))
	}

	// First cell should match the existing "authentication" topic.
	if assigned[0].TopicID != "topic_auth" {
		t.Errorf("expected topic_auth for authentication cell, got %s", assigned[0].TopicID)
	}

	// Second cell should get a new topic created from "frontend" hint.
	if assigned[1].TopicID == "" {
		t.Fatal("expected non-empty topic ID for frontend cell")
	}
	if assigned[1].TopicID == "topic_auth" {
		t.Error("frontend cell should not match authentication topic")
	}

	// Verify the new topic was persisted.
	topic, err := mdb.GetTopic(assigned[1].TopicID)
	if err != nil {
		t.Fatal(err)
	}
	if topic == nil {
		t.Fatal("expected new frontend topic to be persisted in DB")
	}
	if topic.Name != "frontend" {
		t.Errorf("expected topic name 'frontend', got %q", topic.Name)
	}
}

func TestAssignToTopicByEmbeddingSimilarity(t *testing.T) {
	mdb := openTestDB(t)

	// Pre-create topic "database" with a known embedding.
	dbVec := []float32{0.9, 0.1, 0}
	err := mdb.UpsertTopic(memory.Topic{
		TopicID:   "topic_db",
		Name:      "database",
		Embedding: memory.SerializeEmbedding(dbVec),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Use a fixed embedder that returns a vector close to the database topic.
	emb := &fixedEmbedder{vec: []float32{0.85, 0.15, 0}}

	cells := []memory.ExtractedCell{
		{CellType: "fact", Salience: 0.7, Content: "Connection pool uses 20 connections", TopicHint: "data layer"},
	}

	assigned, err := memory.AssignCellsToTopics(context.Background(), mdb, cells, emb)
	if err != nil {
		t.Fatal(err)
	}

	if len(assigned) != 1 {
		t.Fatalf("expected 1 assigned cell, got %d", len(assigned))
	}

	// "data layer" does not name-match "database", but embedding similarity should.
	if assigned[0].TopicID != "topic_db" {
		t.Errorf("expected topic_db (embedding match), got %s", assigned[0].TopicID)
	}
}

func TestAssignCreatesNewTopicWhenNoMatch(t *testing.T) {
	mdb := openTestDB(t)

	// No pre-existing topics.
	cells := []memory.ExtractedCell{
		{CellType: "fact", Salience: 0.8, Content: "Server uses Go 1.22", TopicHint: "backend"},
		{CellType: "preference", Salience: 0.6, Content: "User likes dark mode", TopicHint: "ui preferences"},
	}

	assigned, err := memory.AssignCellsToTopics(context.Background(), mdb, cells, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(assigned) != 2 {
		t.Fatalf("expected 2 assigned cells, got %d", len(assigned))
	}

	// Both should have new topic IDs.
	if assigned[0].TopicID == "" {
		t.Error("expected non-empty topic ID for backend cell")
	}
	if assigned[1].TopicID == "" {
		t.Error("expected non-empty topic ID for ui preferences cell")
	}

	// They should be different topics.
	if assigned[0].TopicID == assigned[1].TopicID {
		t.Error("expected different topic IDs for different hints")
	}

	// Verify topics were persisted.
	for i, ac := range assigned {
		topic, err := mdb.GetTopic(ac.TopicID)
		if err != nil {
			t.Fatal(err)
		}
		if topic == nil {
			t.Fatalf("assigned[%d]: topic %s not found in DB", i, ac.TopicID)
		}
	}

	// Verify topic names.
	t0, _ := mdb.GetTopic(assigned[0].TopicID)
	if t0.Name != "backend" {
		t.Errorf("expected topic name 'backend', got %q", t0.Name)
	}
	t1, _ := mdb.GetTopic(assigned[1].TopicID)
	if t1.Name != "ui preferences" {
		t.Errorf("expected topic name 'ui preferences', got %q", t1.Name)
	}
}
