# pi-bridge

Wraps [`pi-coding-agent`](https://github.com/earendil-works/pi) as an A2A HTTP
server so Percy (or any A2A client) can dispatch tasks to a remote pi instance.

Run a Raspberry Pi (or any host) as a coding worker that Percy can route
requests to.

## Quick start

```bash
npm install -g @earendil-works/pi-coding-agent
export ANTHROPIC_API_KEY=sk-ant-...
export PI_BRIDGE_TOKEN=secret
export PI_BRIDGE_PORT=9000
export PI_CWD=$(pwd)        # working dir pi sees
node server.mjs
```

## From Percy

With Percy's A2A category activated (`request_tools` → `a2a`), the agent can call:

```json
{
  "name": "a2a_dispatch",
  "input": {
    "url": "http://pi.local:9000",
    "token": "secret",
    "prompt": "List the files in this directory and describe what each does."
  }
}
```

## Protocol

- `GET /.well-known/agent-card.json` — advertises the bridge as an A2A agent.
- `POST /a2a` — JSON-RPC 2.0; supports `message/send` only (v1).
- Bearer-token auth via `PI_BRIDGE_TOKEN`.

Each `message/send` spawns `pi --mode json --no-session <prompt>`, reads the
resulting JSONL event stream, and returns the concatenated assistant text as a
completed A2A Task.

## Limitations (v1)

- No streaming (`message/stream`) — add later if needed.
- `contextId` is echoed back but not used to resume a pi session; each call is
  a fresh pi run. Map `contextId` → `pi --resume <session>` to add continuity.
- No `tasks/get` / `tasks/cancel`. Tasks are synchronous within one HTTP request.
