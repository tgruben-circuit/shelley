-- name: CreateConversation :one
INSERT INTO conversations (conversation_id, slug, user_initiated, cwd, model)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetConversation :one
SELECT * FROM conversations
WHERE conversation_id = ?;

-- name: GetConversationBySlug :one
SELECT * FROM conversations
WHERE slug = ?;

-- name: ListConversations :many
SELECT * FROM conversations
WHERE archived = FALSE AND parent_conversation_id IS NULL
ORDER BY updated_at DESC
LIMIT ? OFFSET ?;

-- name: ListArchivedConversations :many
SELECT * FROM conversations
WHERE archived = TRUE
ORDER BY updated_at DESC
LIMIT ? OFFSET ?;

-- name: SearchConversations :many
SELECT * FROM conversations
WHERE slug LIKE '%' || ? || '%' AND archived = FALSE AND parent_conversation_id IS NULL
ORDER BY updated_at DESC
LIMIT ? OFFSET ?;

-- name: SearchConversationsWithMessages :many
-- Search conversations by slug OR message content (user messages and agent responses, not system prompts)
-- Includes both top-level conversations and subagent conversations
SELECT DISTINCT c.* FROM conversations c
LEFT JOIN messages m ON c.conversation_id = m.conversation_id AND m.type IN ('user', 'agent')
WHERE c.archived = FALSE
  AND (
    c.slug LIKE '%' || ? || '%'
    OR json_extract(m.user_data, '$.text') LIKE '%' || ? || '%'
    OR m.llm_data LIKE '%' || ? || '%'
  )
ORDER BY c.updated_at DESC
LIMIT ? OFFSET ?;

-- name: SearchArchivedConversations :many
SELECT * FROM conversations
WHERE slug LIKE '%' || ? || '%' AND archived = TRUE
ORDER BY updated_at DESC
LIMIT ? OFFSET ?;

-- name: UpdateConversationSlug :one
UPDATE conversations
SET slug = ?, updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?
RETURNING *;

-- name: UpdateConversationTimestamp :exec
UPDATE conversations
SET updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?;

-- name: DeleteConversation :exec
DELETE FROM conversations
WHERE conversation_id = ?;

-- name: CountConversations :one
SELECT COUNT(*) FROM conversations WHERE archived = FALSE AND parent_conversation_id IS NULL;

-- name: CountArchivedConversations :one
SELECT COUNT(*) FROM conversations WHERE archived = TRUE;

-- name: ArchiveConversation :one
UPDATE conversations
SET archived = TRUE, updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?
RETURNING *;

-- name: UnarchiveConversation :one
UPDATE conversations
SET archived = FALSE, updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?
RETURNING *;

-- name: UpdateConversationCwd :one
UPDATE conversations
SET cwd = ?, updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?
RETURNING *;


-- name: CreateSubagentConversation :one
INSERT INTO conversations (conversation_id, slug, user_initiated, cwd, parent_conversation_id)
VALUES (?, ?, FALSE, ?, ?)
RETURNING *;

-- name: GetSubagents :many
SELECT * FROM conversations
WHERE parent_conversation_id = ?
ORDER BY created_at ASC;

-- name: GetConversationBySlugAndParent :one
SELECT * FROM conversations
WHERE slug = ? AND parent_conversation_id = ?;

-- name: UpdateConversationModel :exec
UPDATE conversations
SET model = ?
WHERE conversation_id = ? AND model IS NULL;

-- name: SearchMessageHits :many
-- One best (most-recent) matching user/agent message per non-archived conversation.
-- The keyword placeholder (?) appears twice; pass the same value for both.
SELECT c.conversation_id, c.slug, c.user_initiated, c.created_at, c.updated_at,
       c.cwd, c.archived, c.parent_conversation_id, c.model,
       m.message_id AS match_message_id,
       m.type       AS match_type,
       m.user_data  AS match_user_data,
       m.llm_data   AS match_llm_data
FROM conversations c
JOIN messages m ON m.conversation_id = c.conversation_id
WHERE c.archived = FALSE
  AND m.message_id = (
    SELECT m2.message_id FROM messages m2
    WHERE m2.conversation_id = c.conversation_id
      AND m2.type IN ('user', 'agent')
      AND (json_extract(m2.user_data, '$.text') LIKE '%' || ? || '%'
           OR m2.llm_data LIKE '%' || ? || '%')
    ORDER BY m2.sequence_id DESC
    LIMIT 1)
ORDER BY c.updated_at DESC
LIMIT ? OFFSET ?;

-- name: UpdateConversationTags :one
UPDATE conversations
SET tags = ?, updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?
RETURNING *;
