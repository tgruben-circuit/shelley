package memory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
)

// hashMessages returns a SHA-256 hex digest of concatenated role+text.
func hashMessages(messages []MessageText) string {
	h := sha256.New()
	for _, m := range messages {
		h.Write([]byte(m.Role))
		h.Write([]byte(m.Text))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// hashString returns a SHA-256 hex digest of s.
func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}

// IsIndexed returns true if the stored hash for (sourceType, sourceID)
// matches the provided hash and the hash is non-empty.
func (d *DB) IsIndexed(sourceType, sourceID, hash string) (bool, error) {
	if hash == "" {
		return false, nil
	}
	var stored string
	err := d.db.QueryRow(
		`SELECT hash FROM index_state WHERE source_type = ? AND source_id = ?`,
		sourceType, sourceID,
	).Scan(&stored)
	if err != nil {
		return false, nil // not found is not an error
	}
	return stored == hash, nil
}

// SetIndexState records (or updates) the content hash for a source.
func (d *DB) SetIndexState(sourceType, sourceID, hash string) error {
	_, err := d.db.Exec(
		`INSERT OR REPLACE INTO index_state (source_type, source_id, indexed_at, hash)
		 VALUES (?, ?, datetime('now'), ?)`,
		sourceType, sourceID, hash,
	)
	if err != nil {
		return fmt.Errorf("memory: set index state: %w", err)
	}
	return nil
}

// IndexConversation indexes a conversation's messages into the chunks table.
// It skips re-indexing when the content hash has not changed.
// If embedder is non-nil, embeddings are generated (errors are ignored for
// graceful degradation).
func (d *DB) IndexConversation(ctx context.Context, conversationID, slug string, messages []MessageText, embedder Embedder) error {
	hash := hashMessages(messages)

	indexed, err := d.IsIndexed("conversation", conversationID, hash)
	if err != nil {
		return err
	}
	if indexed {
		return nil
	}

	chunks := ChunkMessages(messages, 1024)

	if err := d.DeleteChunksBySource("conversation", conversationID); err != nil {
		return err
	}

	// Batch-embed all chunk texts if an embedder is provided.
	var embeddings [][]float32
	if embedder != nil {
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.Text
		}
		embeddings, _ = embedder.Embed(ctx, texts) // ignore errors — graceful degradation
	}

	for i, c := range chunks {
		chunkID := fmt.Sprintf("conv_%s_%d", conversationID, i)
		var embBlob []byte
		if i < len(embeddings) && embeddings[i] != nil {
			embBlob = SerializeEmbedding(embeddings[i])
		}
		if err := d.InsertChunk(chunkID, "conversation", conversationID, slug, c.Index, c.Text, embBlob); err != nil {
			return err
		}
	}

	return d.SetIndexState("conversation", conversationID, hash)
}

// IndexFile indexes a file's content into the chunks table.
// It skips re-indexing when the content hash has not changed.
func (d *DB) IndexFile(ctx context.Context, filePath, fileName, content string, embedder Embedder) error {
	hash := hashString(content)

	indexed, err := d.IsIndexed("file", filePath, hash)
	if err != nil {
		return err
	}
	if indexed {
		return nil
	}

	chunks := ChunkMarkdown(content, 1024)

	if err := d.DeleteChunksBySource("file", filePath); err != nil {
		return err
	}

	var embeddings [][]float32
	if embedder != nil {
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.Text
		}
		embeddings, _ = embedder.Embed(ctx, texts)
	}

	// Use first 8 chars of the hash for chunk IDs.
	short := hash
	if len(short) > 8 {
		short = short[:8]
	}

	for i, c := range chunks {
		chunkID := fmt.Sprintf("file_%s_%d", short, i)
		var embBlob []byte
		if i < len(embeddings) && embeddings[i] != nil {
			embBlob = SerializeEmbedding(embeddings[i])
		}
		if err := d.InsertChunk(chunkID, "file", filePath, fileName, c.Index, c.Text, embBlob); err != nil {
			return err
		}
	}

	return d.SetIndexState("file", filePath, hash)
}

// sourceName returns a display name for a conversation, falling back to
// a truncation of the first message.
func sourceName(slug string, messages []MessageText) string {
	if slug != "" {
		return slug
	}
	if len(messages) > 0 {
		t := messages[0].Text
		if len(t) > 60 {
			t = t[:60]
		}
		return strings.TrimSpace(t)
	}
	return ""
}
