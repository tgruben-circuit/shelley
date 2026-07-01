# Percy Hooks

You can customize Percy's behavior by placing executable scripts in
`$HOME/.config/percy/hooks/<name>`.

If a hook fails (non-zero exit, invalid output, etc.) the operation it belongs
to is aborted. The `end-of-turn` hook is the exception: by the time it fires
there is no operation left to abort, so failures are just logged.

Hooks are opt-in — Percy only runs a hook if an executable file with the
matching name exists. Each hook is given at most 30 seconds to run. Sensitive
request headers (`Cookie`, `Set-Cookie`, `Authorization`,
`Proxy-Authorization`) are stripped before being passed to hooks.

## Available Hooks

| Hook | Stdin | Stdout |
|---|---|---|
| `system-prompt` | system prompt text | replacement prompt (non-empty required) |
| `new-conversation` | JSON | JSON (mutable fields; empty = no-op) |
| `chat-message` | JSON | JSON (`message` field; empty = no-op) |
| `end-of-turn` | JSON | ignored |

## `system-prompt`

Stdin is the rendered system prompt text. Stdout must be the (possibly
modified) replacement prompt text; a non-empty result is required. Fires for
both top-level and subagent conversations.

## `new-conversation`

Fires when a new top-level conversation is created from its first message.

Stdin:

```json
{
  "prompt": "hello percy",
  "model": "predictable",
  "cwd": "",
  "readonly": {
    "conversation_id": "cH3UICU",
    "is_subagent": false,
    "headers": [["X-Custom", "demo"]]
  }
}
```

Stdout (all fields optional; empty stdout = no-op). Only non-empty fields
override the defaults:

```json
{ "prompt": "", "model": "", "cwd": "", "slug": "" }
```

If `slug` is set, it replaces Percy's async LLM-generated slug for the new
conversation. The slug is sanitized before use; if the sanitized form is empty
or collides with an existing slug, Percy falls back to its normal async slug
generation.

## `chat-message`

Fires when a user posts a follow-up message to an existing conversation (the
first message of a new conversation uses `new-conversation` instead).

Stdin:

```json
{
  "message": "follow-up question",
  "readonly": {
    "conversation_id": "cH3UICU",
    "model": "predictable",
    "headers": [["X-Custom", "demo"]]
  }
}
```

Stdout (empty = no-op):

```json
{ "message": "the rewritten message" }
```

## `end-of-turn`

Fires after the agent finishes a turn. Stdout is ignored; failures are logged
but do not abort anything.

```json
{
  "type": "agent_done",
  "conversation_id": "cMT7MTV",
  "timestamp": "2026-07-01T00:34:31.961478145Z",
  "model": "predictable",
  "slug": "my-conversation",
  "final_response": "Done."
}
```
