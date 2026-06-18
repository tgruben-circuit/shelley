# Conversation keyword search: snippets + jump-to-match

**Date:** 2026-06-18
**Status:** Design approved, pending implementation plan

## Problem

Percy already has keyword search across past conversations:

- **DB:** `SearchConversationsWithMessages` (`db/query/conversations.sql`) matches a
  keyword against the conversation slug, user message text (`user_data.text`), and
  raw `llm_data` of `user`/`agent` messages.
- **API:** `GET /api/conversations?q=<kw>&search_content=true` (`server/handlers.go:481`).
- **UI:** the ⌘K / Ctrl+K command palette (`ui/src/components/CommandPalette.tsx`)
  debounces a content search and lists matching conversations.

The search **works** but its results are not useful: it returns conversations only.
The user cannot see *what* matched or *where*, and selecting a result opens the
conversation without navigating to the matching message.

## Goal

Enhance the existing ⌘K palette so a keyword search shows, per matching
conversation, a **highlighted snippet** of the matched text, and selecting a
result **opens the conversation scrolled to the matching message** with a
transient highlight.

### Scope decisions (from brainstorming)

- **One row per conversation** with a single best-matching snippet (not per-message).
- **Stay in the ⌘K palette** — no dedicated search page.
- **Keep `LIKE`-based search**; build snippets in Go. No FTS5 migration now, but
  isolate the snippet logic behind a helper so FTS5 (`snippet()`/`bm25()`) can drop
  in later without UI changes.

### Non-goals

- Full-text search / relevance ranking (FTS5). Deferred; results stay ordered by
  conversation `updated_at`.
- Searching archived conversations or `tool`-type messages.
- Search within a single open conversation; CLI/TUI search.

## Architecture & data flow

```
⌘K palette → GET /api/conversations/search?q=kw
           → SearchMessageHits (one best-matching message per conversation)
           → Go builds snippet + match offsets
           → [{conversation, match_message_id, snippet, match_ranges}]
palette renders title + highlighted snippet per row
click → selectConversation(conv, matchMessageId)
      → ChatInterface scrolls to msg-<id>, briefly highlights it
```

The existing `q`/`search_content` behavior on `GET /api/conversations` is left
untouched (it is used by the conversation list). Search-with-snippets is a **new
endpoint** so the conversation-list response shape does not change.

## Backend

### New query (`db/query/conversations.sql`)

One best (most recent) matching `user`/`agent` message per non-archived conversation:

```sql
-- name: SearchMessageHits :many
SELECT c.*, m.message_id AS match_message_id,
       COALESCE(json_extract(m.user_data,'$.text'), m.llm_data) AS match_text
FROM conversations c
JOIN messages m ON m.conversation_id = c.conversation_id
WHERE c.archived = FALSE
  AND m.type IN ('user','agent')
  AND m.message_id = (
    SELECT m2.message_id FROM messages m2
    WHERE m2.conversation_id = c.conversation_id
      AND m2.type IN ('user','agent')
      AND (json_extract(m2.user_data,'$.text') LIKE '%'||?1||'%'
           OR m2.llm_data LIKE '%'||?1||'%')
    ORDER BY m2.sequence_id DESC LIMIT 1)
ORDER BY c.updated_at DESC
LIMIT ?2 OFFSET ?3;
```

Regenerate with `sqlc generate`.

### New handler `handleSearchMessages`

Route `GET /api/conversations/search?q=&limit=&offset=`. For each row: take
`match_text`, locate the keyword case-insensitively, cut a snippet (~80 chars of
context on each side, ellipses at cut points), and compute match offsets within the
snippet so the UI highlights without re-scanning.

```go
type SearchHit struct {
    Conversation   ConversationWithState `json:"conversation"`
    MatchMessageID string                `json:"match_message_id"`
    Snippet        string                `json:"snippet"`
    MatchRanges    [][2]int              `json:"match_ranges"` // [start,end) in snippet runes
}
```

A small unexported helper `extractSnippet(text, kw string) (string, [][2]int)`
holds the snippet logic and is the **seam** for a future FTS5 `snippet()`
implementation. Operates on runes (no multibyte panics).

## Frontend

- **`ui/src/services/api.ts`** — new `searchMessages(query): Promise<SearchHit[]>`
  hitting `/api/conversations/search`. `SearchHit` type comes from the generated
  Go→TS types (`pnpm run generate-types`).
- **`ui/src/components/CommandPalette.tsx`** — debounced search calls
  `api.searchMessages` instead of `api.searchConversations`. Conversation rows
  render the title plus a snippet line, with `match_ranges` wrapped in `<mark>` via
  a `HighlightedSnippet` helper (no `dangerouslySetInnerHTML`). On select, call
  `onSelectConversation(conv, matchMessageId)`.
- **`ui/src/App.tsx`** — `selectConversation` gains an optional `targetMessageId`,
  stored in state (`pendingScrollMessageId`) and passed to `ChatInterface`.
- **`ui/src/components/ChatInterface.tsx`** —
  1. Each message row gets `id={`msg-${message.message_id}`}`.
  2. A `useEffect` watching `pendingScrollMessageId` + messages-loaded: once the
     target row is in the DOM, `scrollIntoView({block:'center'})`, add a transient
     highlight class (~2s), then clear the pending id. Missing target → no-op.

No new routes or pages.

## Error handling

(Repo convention: propagate or fail, no fallbacks.)

- **Backend:** empty `q` → `400`. DB error → log + `500` (matches
  `handleConversations`). Malformed `user_data` JSON → `json_extract` returns null,
  `COALESCE` falls back to `llm_data`; if both empty, the row is skipped.
- **Snippet helper:** keyword not found in `match_text` (e.g. matched only inside
  `llm_data` JSON keys) → return a head-of-text snippet with empty `match_ranges`.
  Rune-safe.
- **Frontend:** search request failure → existing palette behavior (clear results,
  `console.error`). Jump target missing from DOM → silent no-op; conversation still
  opens.

## Testing

- **Go** (`server/*_test.go`): seed conversations/messages; assert `SearchMessageHits`
  returns one hit per matching conversation with the newest matching message winning.
  Unit-test `extractSnippet` (match at start/middle/end, multibyte text, not-found,
  multiple occurrences → first range). Endpoint test: `q=""` → 400; happy-path JSON
  shape.
- **E2E** (Playwright, `--model predictable`, no sleeps): open ⌘K, type a known
  keyword, assert a result row shows a `<mark>`-highlighted snippet, click it, assert
  the conversation opens and the matching message scrolls into view and is
  highlighted. Wait on the highlight class / scroll, not timers.

## Future work

- Swap the `LIKE` query + `extractSnippet` for SQLite FTS5 (`snippet()`, `bm25()`)
  behind the same handler/response shape: real ranking, phrase/boolean queries, scale.
- Optionally search archived conversations and `tool` output.
