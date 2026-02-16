package memory_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/tgruben-circuit/percy/llm"
	"github.com/tgruben-circuit/percy/memory"
)

// consolidationMockLLM returns a fixed response string for any LLM request.
type consolidationMockLLM struct {
	response string
}

func (m *consolidationMockLLM) Do(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return &llm.Response{
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: m.response}},
	}, nil
}
func (m *consolidationMockLLM) TokenContextWindow() int { return 128000 }
func (m *consolidationMockLLM) MaxImageDimension() int  { return 0 }

func TestConsolidateTopic(t *testing.T) {
	mdb := openTestDB(t)

	topicID := "topic-consolidate"
	err := mdb.UpsertTopic(memory.Topic{
		TopicID: topicID,
		Name:    "authentication",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Insert 6 cells.
	for i := 0; i < 6; i++ {
		err := mdb.InsertCell(memory.Cell{
			CellID:     fmt.Sprintf("cell-%d", i),
			TopicID:    topicID,
			SourceType: "conversation",
			SourceID:   "conv-1",
			SourceName: "Auth Discussion",
			CellType:   "fact",
			Salience:   0.8,
			Content:    fmt.Sprintf("Auth fact number %d", i),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Mock LLM returns a consolidation result that supersedes cells 0 and 1.
	result := memory.ConsolidationResult{
		Summary:           "Auth uses JWT with RS256 signing. Tokens expire after 1 hour. Refresh tokens stored in httpOnly cookies.",
		SupersededCellIDs: []string{"cell-0", "cell-1"},
	}
	respJSON, _ := json.Marshal(result)
	mock := &consolidationMockLLM{response: string(respJSON)}

	err = memory.ConsolidateTopic(context.Background(), mdb, mock, nil, topicID)
	if err != nil {
		t.Fatalf("ConsolidateTopic: %v", err)
	}

	// Verify topic summary was updated.
	topic, err := mdb.GetTopic(topicID)
	if err != nil {
		t.Fatal(err)
	}
	if topic == nil {
		t.Fatal("expected topic, got nil")
	}
	if topic.Summary != result.Summary {
		t.Errorf("expected summary %q, got %q", result.Summary, topic.Summary)
	}

	// Verify superseded cells are excluded from non-superseded query.
	cells, err := mdb.GetCellsByTopic(topicID, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cells {
		if c.CellID == "cell-0" || c.CellID == "cell-1" {
			t.Errorf("superseded cell %s should not appear in non-superseded results", c.CellID)
		}
	}
	if len(cells) != 4 {
		t.Errorf("expected 4 non-superseded cells, got %d", len(cells))
	}
}

func TestConsolidateSkipsSmallTopics(t *testing.T) {
	mdb := openTestDB(t)

	topicID := "topic-small"
	err := mdb.UpsertTopic(memory.Topic{
		TopicID: topicID,
		Name:    "small topic",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Insert only 1 cell.
	err = mdb.InsertCell(memory.Cell{
		CellID:     "cell-only",
		TopicID:    topicID,
		SourceType: "conversation",
		SourceID:   "conv-1",
		SourceName: "Small Conv",
		CellType:   "fact",
		Salience:   0.5,
		Content:    "Single fact",
	})
	if err != nil {
		t.Fatal(err)
	}

	needs, err := memory.NeedsConsolidation(mdb, topicID)
	if err != nil {
		t.Fatal(err)
	}
	if needs {
		t.Error("expected NeedsConsolidation to return false for 1 cell")
	}
}

func TestNeedsConsolidationTrue(t *testing.T) {
	mdb := openTestDB(t)

	topicID := "topic-needs"
	err := mdb.UpsertTopic(memory.Topic{
		TopicID: topicID,
		Name:    "needs consolidation",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Insert 6 cells (above the threshold of 5).
	for i := 0; i < 6; i++ {
		err := mdb.InsertCell(memory.Cell{
			CellID:     fmt.Sprintf("nc-%d", i),
			TopicID:    topicID,
			SourceType: "conversation",
			SourceID:   "conv-1",
			SourceName: "Consolidation Test",
			CellType:   "fact",
			Salience:   0.6,
			Content:    fmt.Sprintf("Fact %d for consolidation", i),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	needs, err := memory.NeedsConsolidation(mdb, topicID)
	if err != nil {
		t.Fatal(err)
	}
	if !needs {
		t.Error("expected NeedsConsolidation to return true for 6 cells")
	}
}
