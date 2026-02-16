# Agent Clustering Design: NATS-Native Coordination for Percy

**Date**: 2026-02-16
**Status**: Approved
**Approach**: NATS-Native Coordination (Approach A)

## Problem

Percy is a single-user, single-agent system. We want multiple Percy agents running on different machines to coordinate on shared codebases -- parallel task execution, specialized agents, and multi-user collaboration.

## Key Decisions

- **NATS** as the coordination backbone (embedded server, no external dependencies)
- **Git** as the source of truth for code (each agent works on its own branch)
- **Hybrid orchestration** -- orchestrator plans and assigns, agents can self-organize and sub-delegate
- **A2A** deferred to a future HTTP gateway layer
- **Additive** -- `--cluster` flag opts in, existing single-user mode unchanged

## Architecture

### Agent Identity & Discovery

Each Percy instance registers on startup via NATS KV bucket `agents`.

**Agent Card** (KV key: `agents/{id}`):

```json
{
  "id": "agent-a1b2c3",
  "name": "frontend-specialist",
  "capabilities": ["typescript", "react", "css", "testing"],
  "status": "idle",
  "current_task": null,
  "repo": "github.com/org/project",
  "branch": "agent/a1b2c3/work",
  "machine": "dev-server-1",
  "started_at": "2026-02-16T10:00:00Z",
  "last_heartbeat": "2026-02-16T10:05:00Z"
}
```

**Lifecycle**:
- Startup: write agent card to KV
- Heartbeat: update `last_heartbeat` every 30s
- Other agents watch KV bucket for changes
- Shutdown or missed heartbeats (>90s): marked `offline`, tasks re-queued

**Subjects**:
- `cluster.agent.announce` -- broadcast on join/leave
- `cluster.agent.{id}.ping` -- request/reply health check

### Task Model & Queue

**Task states**:
```
submitted -> assigned -> working -> completed
                                 -> failed
                          |
                    input_required
```

**Task structure** (JetStream stream `TASKS`):

```json
{
  "id": "task-x7y8z9",
  "parent_id": null,
  "type": "implement",
  "specialization": ["backend", "go"],
  "priority": 1,
  "status": "submitted",
  "assigned_to": null,
  "created_by": "agent-orchestrator",
  "title": "Add pagination to /api/users endpoint",
  "description": "...",
  "context": {
    "repo": "github.com/org/project",
    "base_branch": "main",
    "files_hint": ["server/users.go", "db/query/users.sql"]
  },
  "result": null,
  "created_at": "...",
  "updated_at": "..."
}
```

**Subjects**:

| Subject | Pattern | Purpose |
|---------|---------|---------|
| `task.submit` | Publish | Orchestrator submits new tasks |
| `task.{specialization}.available` | Queue group | Agents pull tasks by skill |
| `task.{id}.status` | Pub/sub | Status updates |
| `task.{id}.result` | Publish | Final result |
| `task.claim` | Request/reply | Agent claims task, gets accept/reject |

**Assignment flow**:
1. Orchestrator publishes task to `task.submit` (JetStream persisted)
2. Task router fans out to `task.{specialization}.available`
3. Agents in matching queue groups receive one task each
4. Agent sends `task.claim` request with agent ID and task ID
5. Claim accepted (KV compare-and-swap) or rejected (already claimed)
6. Agent creates branch, does work, pushes, publishes result

**Subtasks**: Agents can break their task into subtasks by publishing new tasks with `parent_id` set.

### Git Coordination & File Locking

**Branch naming**: `agent/{agent-id}/{task-id}`

**Workflow per task**:
1. Agent creates branch from base branch
2. Agent claims file locks via NATS KV
3. Agent does the work, commits, pushes
4. Agent publishes result with branch name
5. Orchestrator merges

**File locks** (KV bucket `locks`, key: `{repo}:{path}`):

```json
{
  "agent_id": "agent-a1b2c3",
  "task_id": "task-x7y8z9",
  "locked_at": "2026-02-16T10:05:00Z",
  "ttl": 3600
}
```

- **Acquire**: KV `Create` (atomic, fails if exists)
- **Release**: KV `Delete` on task completion
- **Stale recovery**: Released when owning agent goes offline
- **Granularity**: File-level

**Merge strategy**:
- Each agent on an isolated branch
- Orchestrator merges completed branches into `main` sequentially

**Merge conflict resolution** (LLM-assisted, three tiers):

| Tier | When | Action |
|------|------|--------|
| Auto-resolve | Trivial conflicts (imports, adjacent changes) | Git resolves automatically |
| LLM-resolve | Semantic conflicts (same function modified) | LLM merges with full task context |
| Human-resolve | Complex conflicts or LLM unsure | Pause pipeline, notify user |

For LLM resolution, the orchestrator provides:
- Conflict diff with markers
- Task descriptions for both sides
- Original file before either change
- What each agent was trying to accomplish

**Subjects**:
- `git.lock.acquire` -- request/reply
- `git.lock.release` -- publish
- `git.branch.ready` -- agent signals branch ready for merge
- `git.merge.complete` -- broadcast, agents rebase if needed

### Orchestration & Agent Communication

**Orchestrator role** (Percy in coordinator mode):
- Receives high-level tasks from users
- LLM breaks tasks into subtasks with dependency ordering
- Publishes subtasks, monitors progress, sequences merges
- Can reassign failed/stalled tasks
- Is itself a Percy agent -- can do work directly

**Dependency management**:
```
User -> "Refactor auth to JWT"
Orchestrator plans:
  Task 1: Add JWT library              (no deps)
  Task 2: Update login endpoint         (blocked by 1)
  Task 3: Update auth context           (blocked by 1)
  Task 4: Integration tests             (blocked by 2, 3)

Tasks published as dependencies resolve.
Tasks 2 & 3 run in parallel after Task 1 completes.
```

**Agent-to-agent subjects**:

| Subject | Pattern | Purpose |
|---------|---------|---------|
| `agent.{id}.message` | Pub/sub | Direct message |
| `cluster.broadcast` | Pub/sub | Message all agents |
| `agent.{id}.request` | Request/reply | Ask agent, wait for response |
| `task.{id}.discussion` | Pub/sub | Task conversation thread |

**Self-organization**:
- Agents publish subtasks for help they need
- Idle agents claim unclaimed tasks from the queue
- No orchestrator involvement required for self-organized work

### Percy Integration

**New package**:

```
cluster/
  cluster.go          Agent lifecycle (register, heartbeat, shutdown)
  tasks.go            Task queue management (submit, claim, complete)
  locks.go            File lock acquire/release
  orchestrator.go     Task planning, dependency tracking, merge sequencing
  nats.go             NATS connection management, subject helpers
  embedded.go         Embedded nats-server lifecycle
```

**Changes to existing packages**:

| Package | Change |
|---------|--------|
| `cmd/percy/` | `--cluster` flag (NATS listen address or URL), `--agent-name`, `--capabilities`. New `orchestrate` subcommand. |
| `server/` | ConversationManager gains cluster task awareness. New cluster status endpoints. |
| `loop/` | Loop can be started by cluster task. On completion, publishes result to NATS. |
| `server/convo.go` | `onConversationDone` extended to report task completion to cluster. |
| `subpub/` | Unchanged -- handles local SSE. NATS is separate. |
| `ui/` | New cluster dashboard: agent status, task queue, progress, merge pipeline. |

**CLI**:

```bash
# Orchestrator: embeds NATS server + coordinates
percy orchestrate --cluster :4222 --repo github.com/org/project

# Worker: connects to orchestrator's embedded NATS
percy serve --cluster nats://orchestrator-host:4222 \
            --agent-name "backend-specialist" \
            --capabilities go,sql,api

# Single-user (unchanged)
percy serve
```

**Embedded NATS**:
- `percy orchestrate --cluster :4222` starts embedded `nats-server` in-process
- JetStream enabled with file-based storage for persistence
- Workers connect as clients
- Auto-reconnect built into NATS client library

### Error Handling & Resilience

**Agent failure**:
- 3 missed heartbeats (90s) -> marked `offline`
- File locks released, in-progress tasks re-queued

**Embedded NATS host goes down**:
- JetStream persists to disk, recovers on restart
- Workers auto-reconnect with jitter
- Workers buffer unsent results locally, replay on reconnect

**Task failure**:
- Agent reports `status: failed` with error details
- Orchestrator decides: retry (different agent), skip, or escalate to user
- Max 1 retry on a different agent by default

**Merge conflicts**:
- LLM-assisted resolution (see merge conflict section above)
- Persistent failures escalate to user

**Split brain**:
- One orchestrator enforced via KV compare-and-swap on `kv:cluster/leader`
- No automatic failover in v1 -- user restarts orchestrator

**Explicitly NOT in v1**:
- Automatic orchestrator failover / leader election
- Multi-region NATS clustering
- Partial task results / checkpointing mid-task

## NATS Infrastructure Summary

| NATS Feature | Used For |
|---|---|
| Embedded `nats-server` | Zero-dependency deployment |
| JetStream stream `TASKS` | Durable task queue |
| JetStream KV `agents` | Agent registry, heartbeats |
| JetStream KV `locks` | File locking with atomic CAS |
| JetStream KV `cluster` | Leader/orchestrator tracking |
| Queue groups | Load-balanced task distribution by specialization |
| Request/reply | Task claims, health checks, direct queries |
| Pub/sub | Status updates, merge notifications, broadcasts |

## Implementation Process

Each implementation phase ends with a code review checkpoint. After completing a phase, request a review from Codex to validate the work before proceeding to the next phase. This ensures quality and catches integration issues early across the multi-phase build.
