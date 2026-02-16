package memory

import (
	"fmt"
	"sort"
)

const (
	ftsWeight    = 0.4
	vectorWeight = 0.6
)

// SearchResult represents a single FTS5 search hit.
type SearchResult struct {
	ChunkID    string
	SourceType string
	SourceID   string
	SourceName string
	Text       string
	Score      float64
}

// InsertChunk inserts or replaces a chunk in the chunks table.
func (d *DB) InsertChunk(chunkID, sourceType, sourceID, sourceName string, chunkIndex int, text string, embedding []byte) error {
	_, err := d.db.Exec(
		`INSERT OR REPLACE INTO chunks (chunk_id, source_type, source_id, source_name, chunk_index, text, token_count, embedding)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		chunkID, sourceType, sourceID, sourceName, chunkIndex, text, EstimateTokens(text), embedding,
	)
	if err != nil {
		return fmt.Errorf("memory: insert chunk: %w", err)
	}
	return nil
}

// DeleteChunksBySource removes all chunks for a given source.
func (d *DB) DeleteChunksBySource(sourceType, sourceID string) error {
	_, err := d.db.Exec(`DELETE FROM chunks WHERE source_type = ? AND source_id = ?`, sourceType, sourceID)
	if err != nil {
		return fmt.Errorf("memory: delete chunks: %w", err)
	}
	return nil
}

// SearchFTS performs a full-text search using FTS5 MATCH with BM25 ranking.
// If sourceType is empty, all source types are searched.
func (d *DB) SearchFTS(query, sourceType string, limit int) ([]SearchResult, error) {
	q := `SELECT c.chunk_id, c.source_type, c.source_id, c.source_name, c.text, f.rank
		FROM chunks_fts f
		JOIN chunks c ON c.rowid = f.rowid
		WHERE chunks_fts MATCH ?`

	var args []any
	args = append(args, query)
	if sourceType != "" {
		q += ` AND c.source_type = ?`
		args = append(args, sourceType)
	}
	q += ` ORDER BY f.rank LIMIT ?`
	args = append(args, limit)

	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("memory: search fts: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var sr SearchResult
		if err := rows.Scan(&sr.ChunkID, &sr.SourceType, &sr.SourceID, &sr.SourceName, &sr.Text, &sr.Score); err != nil {
			return nil, fmt.Errorf("memory: scan search result: %w", err)
		}
		results = append(results, sr)
	}
	return results, rows.Err()
}

// SearchVector performs a brute-force cosine similarity search over all chunks.
// TODO: consider sqlite-vec or ANN index at scale.
//
// SearchVector scans all chunks
// with non-NULL embeddings. If sourceType is non-empty, only matching chunks
// are considered. Returns the top `limit` results sorted by similarity descending.
func (d *DB) SearchVector(queryVec []float32, sourceType string, limit int) ([]SearchResult, error) {
	q := `SELECT chunk_id, source_type, source_id, source_name, text, embedding
		FROM chunks WHERE embedding IS NOT NULL`

	var args []any
	if sourceType != "" {
		q += ` AND source_type = ?`
		args = append(args, sourceType)
	}

	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("memory: search vector: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var sr SearchResult
		var blob []byte
		if err := rows.Scan(&sr.ChunkID, &sr.SourceType, &sr.SourceID, &sr.SourceName, &sr.Text, &blob); err != nil {
			return nil, fmt.Errorf("memory: scan vector result: %w", err)
		}
		emb := DeserializeEmbedding(blob)
		sr.Score = float64(CosineSimilarity(queryVec, emb))
		results = append(results, sr)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// HybridSearch combines FTS and vector search results using weighted scoring.
// If queryVec is nil, only FTS results are returned.
func (d *DB) HybridSearch(query string, queryVec []float32, sourceType string, limit int) ([]SearchResult, error) {
	ftsResults, err := d.SearchFTS(query, sourceType, 20)
	if err != nil {
		return nil, err
	}

	if queryVec == nil {
		if len(ftsResults) > limit {
			ftsResults = ftsResults[:limit]
		}
		return ftsResults, nil
	}

	vecResults, err := d.SearchVector(queryVec, sourceType, 20)
	if err != nil {
		return nil, err
	}
	if len(vecResults) == 0 {
		if len(ftsResults) > limit {
			ftsResults = ftsResults[:limit]
		}
		return ftsResults, nil
	}

	return mergeResults(ftsResults, vecResults, limit), nil
}

// mergeResults combines FTS and vector results with weighted scoring.
// FTS scores are inverted (BM25 ranks are negative, lower = better).
// Vector scores are kept as-is (higher = better).
func mergeResults(ftsResults, vecResults []SearchResult, limit int) []SearchResult {
	ftsNorm := normalizeScores(ftsResults, true)
	vecNorm := normalizeScores(vecResults, false)

	// Build a map of all chunks by ID, preferring FTS metadata (arbitrary choice).
	type combined struct {
		result   SearchResult
		ftsScore float64
		vecScore float64
	}
	byID := make(map[string]*combined)

	for _, r := range ftsResults {
		byID[r.ChunkID] = &combined{result: r}
	}
	for _, r := range vecResults {
		if _, ok := byID[r.ChunkID]; !ok {
			byID[r.ChunkID] = &combined{result: r}
		}
	}

	for id, c := range byID {
		c.ftsScore = ftsNorm[id]
		c.vecScore = vecNorm[id]
	}

	var merged []SearchResult
	for _, c := range byID {
		sr := c.result
		sr.Score = ftsWeight*c.ftsScore + vectorWeight*c.vecScore
		merged = append(merged, sr)
	}

	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Score > merged[j].Score
	})
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

// MemoryResult is a unified search result from the two-tier search.
type MemoryResult struct {
	ResultType string  // "topic_summary" or "cell"
	TopicID    string
	TopicName  string
	CellID     string
	CellType   string
	SourceType string
	SourceID   string
	SourceName string
	Salience   float64
	Content    string // summary text for topics, cell content for cells
	Score      float64
	UpdatedAt  string
}

// TwoTierSearch performs a two-tier search: topic summaries first, then individual cells.
// queryVec can be nil for FTS-only search.
func (d *DB) TwoTierSearch(query string, queryVec []float32, sourceType string, limit int) ([]MemoryResult, error) {
	topicLimit := 3
	cellLimit := limit - topicLimit
	if cellLimit < 1 {
		cellLimit = 1
	}

	var results []MemoryResult

	// Tier 1: Topic summaries via FTS.
	topicResults, topicErr := d.SearchTopicsFTS(query, topicLimit)
	if topicErr == nil {
		for _, tr := range topicResults {
			results = append(results, MemoryResult{
				ResultType: "topic_summary",
				TopicID:    tr.TopicID,
				TopicName:  tr.Name,
				Content:    tr.Summary,
				Score:      tr.Score,
				UpdatedAt:  tr.UpdatedAt,
			})
		}
	}

	// Tier 2: Individual cells via FTS.
	cellResults, cellErr := d.SearchCellsFTS(query, sourceType, cellLimit)
	if cellErr == nil {
		for _, cr := range cellResults {
			results = append(results, MemoryResult{
				ResultType: "cell",
				TopicID:    cr.TopicID,
				CellID:     cr.CellID,
				CellType:   cr.CellType,
				SourceType: cr.SourceType,
				SourceID:   cr.SourceID,
				SourceName: cr.SourceName,
				Salience:   cr.Salience,
				Content:    cr.Content,
				Score:      cr.Score,
			})
		}
	}

	// If both tiers failed, return the cell error (or topic error).
	if topicErr != nil && cellErr != nil {
		return nil, cellErr
	}

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// normalizeScores maps each result's ChunkID to a normalized score in [0,1].
// If invert is true, lower raw scores map to higher normalized scores (for BM25
// where more-negative = better match).
func normalizeScores(results []SearchResult, invert bool) map[string]float64 {
	m := make(map[string]float64, len(results))
	if len(results) == 0 {
		return m
	}

	minScore, maxScore := results[0].Score, results[0].Score
	for _, r := range results[1:] {
		if r.Score < minScore {
			minScore = r.Score
		}
		if r.Score > maxScore {
			maxScore = r.Score
		}
	}

	rng := maxScore - minScore
	for _, r := range results {
		if rng == 0 {
			m[r.ChunkID] = 1.0
		} else if invert {
			m[r.ChunkID] = 1.0 - (r.Score-minScore)/rng
		} else {
			m[r.ChunkID] = (r.Score - minScore) / rng
		}
	}
	return m
}
