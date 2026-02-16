package memory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
)

// AssignedCell is an ExtractedCell with a resolved topic ID.
type AssignedCell struct {
	ExtractedCell
	TopicID string
}

const similarityThreshold = 0.7

// AssignCellsToTopics assigns each extracted cell to a topic using this priority:
//  1. Name match -- normalize the cell's TopicHint and compare against existing topic names.
//  2. Embedding similarity -- if an embedder is provided and best similarity >= 0.7, use that topic.
//  3. Create new topic -- generate a topic ID from the hint, persist via db.UpsertTopic.
func AssignCellsToTopics(ctx context.Context, db *DB, cells []ExtractedCell, embedder Embedder) ([]AssignedCell, error) {
	existingTopics, err := db.AllTopics()
	if err != nil {
		return nil, fmt.Errorf("memory: assign cells: %w", err)
	}

	// Build name index: normalized name -> topic_id.
	nameIndex := make(map[string]string, len(existingTopics))
	for _, t := range existingTopics {
		nameIndex[normalizeName(t.Name)] = t.TopicID
	}

	// Batch-embed all cell contents if embedder is non-nil.
	var embeddings [][]float32
	if embedder != nil {
		texts := make([]string, len(cells))
		for i, c := range cells {
			texts[i] = c.Content
		}
		embeddings, err = embedder.Embed(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("memory: embed cells: %w", err)
		}
	}

	result := make([]AssignedCell, len(cells))
	for i, cell := range cells {
		ac := AssignedCell{ExtractedCell: cell}

		// Priority 1: Name match.
		hint := normalizeName(cell.TopicHint)
		if topicID, ok := nameIndex[hint]; ok {
			ac.TopicID = topicID
			result[i] = ac
			continue
		}

		// Priority 2: Embedding similarity.
		if embeddings != nil && len(existingTopics) > 0 {
			bestID, bestSim := findBestTopic(embeddings[i], existingTopics)
			if bestSim >= similarityThreshold {
				ac.TopicID = bestID
				result[i] = ac
				continue
			}
		}

		// Priority 3: Create new topic.
		topicID := generateTopicID(cell.TopicHint)
		newTopic := Topic{
			TopicID: topicID,
			Name:    strings.TrimSpace(cell.TopicHint),
		}
		if embeddings != nil {
			newTopic.Embedding = SerializeEmbedding(embeddings[i])
		}
		if err := db.UpsertTopic(newTopic); err != nil {
			return nil, fmt.Errorf("memory: create topic for %q: %w", cell.TopicHint, err)
		}

		// Add to local state so subsequent cells can match.
		existingTopics = append(existingTopics, newTopic)
		nameIndex[hint] = topicID

		ac.TopicID = topicID
		result[i] = ac
	}

	return result, nil
}

// findBestTopic iterates all topics, computes cosine similarity against each
// topic embedding, and returns the best topic ID and similarity score.
func findBestTopic(queryVec []float32, topics []Topic) (string, float32) {
	var bestID string
	var bestSim float32
	for _, t := range topics {
		topicVec := DeserializeEmbedding(t.Embedding)
		if topicVec == nil {
			continue
		}
		sim := CosineSimilarity(queryVec, topicVec)
		if sim > bestSim {
			bestSim = sim
			bestID = t.TopicID
		}
	}
	return bestID, bestSim
}

// normalizeName lowercases and trims whitespace from a topic name.
func normalizeName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// generateTopicID produces a deterministic topic ID from a hint string
// using the first 8 bytes of its SHA-256 hash.
func generateTopicID(hint string) string {
	h := sha256.Sum256([]byte(hint))
	return fmt.Sprintf("topic_%x", h[:8])
}
