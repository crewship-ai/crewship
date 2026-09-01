# PRD: Agent-first issue coordination — durable sessions, wake delivery, and clean context

| Field | Value |
|---|---|
| Status | **SUPERSEDED (2026-09-01) by [`PRD-ISSUES-AND-ROUTINES-2026.md`](PRD-ISSUES-AND-ROUTINES-2026.md).** That document covers Issues *and* Routines against a nine-pass code audit, carries this one's sound findings forward, and corrects five claims made here that the code does not support (see its §3). Kept for its research notes and for the audit trail; do not implement from it. |
| Owner | Crewship maintainers |
| Created | 2026-09-01 |
| Baseline | `main` at `3fa36df5` (`nightly-20260901-r1110`) |
| Product principle | **The issue is the bus. The lead is a participant, not the bus.** |
| Scope | Issues, comments, mentions, assignments, agent sessions, context, execution truth, realtime, and human oversight |
| Intended implementer | A coding agent working in small claimed issues and reviewed PRs |

This PRD is the canonical product and implementation contract for turning the
Issues surface into Crewship's durable human-agent coordination room. It does
not claim that every finding below is unimplemented forever. Phase 0 must
re-measure the current branch before each work package starts, because this
repository changes quickly and several older design documents already describe
features that subsequently shipped.

The umbrella tracking issue may link to this PRD, but it must not become one
large implementation PR. The roadmap in section 15 is deliberately split into
independently claimable, testable work packages.

---

## 0. Executive decision

Crewship will treat an issue as the durable home of a piece of work:

- the **issue** owns the goal, acceptance criteria, ownership, dependencies,
  discussion, artifacts, and human-readable history;
- an **agent session** is one durable agent's relationship to that issue;
- a **run** is one execution attempt inside the session, not the session itself;
- an **event** is an immutable fact that happened on the issue;
- a **delivery** records which session was expected to consume an event and
  whether it actually did;
- a **checkpoint** is the compact, structured working state from which a fresh
  model turn can continue without replaying the entire thread.

A human mention or reply must produce an immediate, visible platform
acknowledgement. If the target agent is idle, it starts or resumes the relevant
issue session. If it is already working, the message is durably queued into the
same session and consumed at the next safe boundary. A normal comment must
never race a second model turn into the same session.

The model receives a clean, bounded context pack assembled from authoritative
state. It does not receive an ever-growing paste of the whole issue or the
lead's whole conversation. Full history remains available through tools.

This is a scoped, observable coordination system, not a workspace-global agent
chat. Peer messages convey information; they do not grant capabilities,
approve dangerous work, or override human/policy authority.

---

## 1. Why now

Real dev1 use cases exposed a structural failure: messages that do travel
between agents retain their text, but the lead often becomes the manual context
relay. When the lead stops taking model turns, the workflow may stop even while
child work exists. The board can then show `REVIEW` although children remain
open, omit direct assignments from Runs, or require a refresh before the human
sees a change.

The client promise is not merely "Crewship can start an agent." It is:

> A person can delegate work, steer it from the issue, see who is doing what,
> and trust that a fresh agent turn will continue from the right facts without
> duplicating completed work.

The current product does not meet that promise end to end.

### 1.1 Observed dev1 findings to preserve as regression cases

The original audit was performed on dev1 after two routine runs, two clone
missions, sixteen delegations, and a factory reset. Before implementation,
Phase 0 must reproduce or retire each finding with a database query and a test.

1. An issue reached `REVIEW` while four child issues remained open.
2. One issue had four linked assignments while its Runs endpoint returned one.
3. Another issue had seven linked assignments while Runs returned zero.
4. Human comments without an explicit agent mention did not reach the active
   work; an analyst repeated an already-completed screenshot capture.
5. Issue rows did not carry sufficient run provenance to reconstruct the cause
   of each change.
6. A temporary agent-token `403` made the issue board unavailable from a live
   crew despite unauthenticated reads elsewhere remaining healthy.
7. The board and detail surfaces did not repaint from every server-emitted
   issue lifecycle event.
8. The Issues board fetched at most 100 rows, exposed no total/pagination, and
   converted some fetch failures into an apparently empty board.
9. Issue and mission status vocabularies were mixed (`DONE` versus
   `COMPLETED`).
10. `Stop work` changed database status without proving that active agent
    execution had stopped.
11. Delegation changed the polymorphic issue assignee to the target agent,
    obscuring the responsible human owner.
12. Start validated that an assignee existed, but not that the assignee was an
    executable agent.

### 1.2 Corrections to the original messaging audit

These corrections are load-bearing. The implementer must not build a fix for a
misdiagnosed system.

| Audit claim | Current code truth | Actual gap |
|---|---|---|
| `/assign` is synchronous | `AssignmentHandler.Create` persists a row, starts `runAssignment` in a goroutine, and returns `PENDING` with an ID. | Completion does not re-enter a lead or issue-scoped model session. Prompt ergonomics also encourage dispatch-then-poll rather than launch-all-then-await. |
| Workers cannot read issues | Every agent receives issue list/get/update/comment/link instructions and the sidecar exposes those verbs. | Agent detail omits the comment thread; direct assignments do not carry an authoritative issue identity and cursor. |
| Comments never reach agents | Structured agent mentions are parsed, resolved, deduplicated, persisted, and dispatched through the assignment gate. Mission task briefs include a bounded comment digest. | Plain replies do not wake the active delegate; late comments do not enter an already-running turn; agent-side comment history has no read endpoint. |
| A crew is strictly sequential | Assignments are asynchronous, have a per-crew admission queue, and mission DAGs can dispatch independent work. | There is no durable issue-session continuation or first-class batch fan-out/await contract for the lead. |
| Only the lead sees the board | The sidecar board API is available to all authenticated agents. | The task, issue thread, execution, and session are not bound into one durable context. |

### 1.3 Current reusable substrate

This PRD must extend rather than replace these mechanisms:

- async assignments and the PENDING/QUEUED/RUNNING lifecycle in
  `internal/api/assignments_*`;
- per-crew admission control and the queue pump;
- mission DAGs and durable pipeline waitpoints;
- structured mention parsing and `mission_comment_mentions` dispatch records;
- the shared issue event emitter in `internal/api/issue_events.go`;
- the append-only journal and journal-to-WS bridge;
- the persistent external notification outbox in `internal/notifyroute` as a
  pattern for deduplication, retries, and operator-visible failures;
- conversation compaction in
  `internal/orchestrator/orchestrator_run_conv.go`;
- curated `AgentBrief` context in `internal/orchestrator/agent_brief.go`;
- queued chat steering through `POST /api/v1/chats/{chatId}/steer`;
- issue attachments, code links, relations, sub-issues, policy, paymaster,
  Keeper, and Harbormaster.

The internal agent-delivery ledger is not the same as the outbound human
notification outbox. It may copy its store/CAS/recovery patterns, but it needs
session cursors, consumption acknowledgement, leases, and replay semantics that
external email/webhook delivery does not have.

---

## 2. Research baseline and product implications

This design follows converging frontier patterns rather than inventing a
Crewship-only vocabulary.

### 2.1 Durable work is external to compute

Anthropic's Managed Agents separates an append-only session, the model harness,
and the sandbox. OpenAI's current agent guidance likewise emphasizes durable
state, handoffs, tracing, compaction, and restoration rather than treating one
container process as the work itself.

Implication: an issue session must survive process exit, server restart,
container re-provisioning, provider change, and model context reset. A provider
session ID is an optimization, never Crewship's canonical identity.

### 2.2 Mention and follow-up belong to one agent session

Linear creates an `AgentSession` on delegation or mention. Later user input is
a `prompted` event in the existing session, whose visible lifecycle includes
pending, active, awaiting input, error, complete, and stale.

Implication: Crewship must stop modeling each follow-up as an unrelated
assignment. Assignments become attempts inside an issue session.

### 2.3 Clean context means curated state plus delta

Current model guidance favors stable cached instructions, explicit outcomes and
stopping conditions, model-native continuation where available, and intentional
compaction. Long-running harness work also shows that a fresh context plus a
structured handoff can be more coherent than an indefinitely growing raw
conversation.

Implication: retain durable facts, decisions, blockers, IDs, artifacts, and the
next goal. Do not preserve hidden chain-of-thought, every log chunk, or repeated
copies of the issue description.

### 2.4 Coordination needs ownership boundaries

Recent multi-agent research found that a shared forum can improve discovery and
specialization, but role prompts and a CEO hierarchy alone did not solve
dependency-heavy collaboration. Clear resource ownership, review, arbitration,
and bounded fan-out remain necessary.

Implication: the board must support claims, exclusive scope, dependencies,
reviewers, and authoritative control actions. It must not wake every participant
for every message.

### 2.5 References

- [Anthropic — Scaling Managed Agents](https://www.anthropic.com/engineering/managed-agents)
- [Anthropic — Patterns and problems in emerging multiagent systems](https://www.anthropic.com/research/multiagent-systems)
- [Anthropic — Harness design for long-running application development](https://www.anthropic.com/engineering/harness-design-long-running-apps)
- [Linear — Agent interaction](https://linear.app/developers/agent-interaction)
- [Linear — AI agents](https://linear.app/docs/agents-in-linear)
- [OpenAI — current model and agent guidance](https://developers.openai.com/api/docs/guides/latest-model?model=gpt-5.5)
- [LangGraph — durable interrupts](https://langchain-ai.github.io/langgraph/concepts/breakpoints/)
- [OpenAI — Hugging Face incident](https://openai.com/index/hugging-face-incident-and-the-road-ahead/)
- [METR — independent incident investigation](https://evals.alignment.org/blog/2026-08-26-openai-hugging-face-incident-investigation/)

---

## 3. Product vocabulary

The implementation, API, UI, documentation, and tests must use these terms
consistently.

| Term | Definition |
|---|---|
| Issue | Durable work object and human-readable coordination room. Stored in `missions` with `mission_type='issue'` during the current schema era. |
| Human owner | Person accountable for the outcome. Delegating to an agent does not remove this ownership. |
| Primary delegate | Agent currently responsible for progressing the issue. Additional agent participants may work in parallel. |
| Participant | Human or agent subscribed to some issue events. Participation alone does not authorize execution. |
| Agent session | Durable issue × agent coordination state spanning zero or more runs. |
| Run | One bounded model/tool execution attempt. Runs may fail, retry, be cancelled, or be replaced without losing the session. |
| Issue event | Immutable typed fact in the issue coordination stream. |
| Delivery | Per-session disposition of an issue event: queued, claimed, consumed, failed, or cancelled. |
| Checkpoint | Structured, bounded continuation state produced after a run or safe boundary. |
| Context pack | Stable instructions + issue snapshot + latest checkpoint + unread delta + relevant memory + artifact manifest. |
| Control event | Server-enforced operation such as stop, hold, resume, claim, release, approve, or veto. It is not merely prose. |
| Activity | Human-readable projection of events. Tool noise may be collapsed; decisions and results may not be hidden. |

---

## 4. Goals

1. A mention, direct reply, or delegated-issue comment is never silently lost.
2. The UI acknowledges human input immediately and displays whether work is
   active, queued, waiting, blocked, failed, stale, or complete.
3. Follow-up input resumes the correct issue session rather than creating an
   unrelated agent run.
4. One session never executes two concurrent model turns.
5. A fresh model turn continues from a bounded, truthful context without the
   lead re-pasting the issue or earlier results.
6. The issue timeline answers who acted, what caused the action, which run did
   it, what state changed, and whether a recipient consumed the message.
7. Human ownership remains visible after delegation.
8. Runs, sub-issues, mission tasks, direct assignments, and mention work roll up
   to a truthful issue state.
9. Stop/cancel changes execution reality, not only database labels.
10. Coordination remains workspace/crew scoped, rate limited, budgeted,
    injection-aware, and auditable.
11. The design remains provider-agnostic while exploiting provider session,
    compaction, or streaming-input capabilities when available.
12. Every critical contract has restart, retry, duplicate-delivery, tenancy,
    and failure-path tests before client beta.

---

## 5. Non-goals

- A workspace-global unmoderated agent social network.
- Persisting or exposing private chain-of-thought.
- Waking every agent subscribed to an issue on every comment.
- Replacing pipelines, missions, the assignment queue, journal, inbox, or
  external notification router.
- Assuming a provider process or container remains alive indefinitely.
- Guaranteeing exactly-once side effects in arbitrary external systems. The
  delivery contract is at-least-once with idempotent claims and deduplication.
- Solving cross-workspace agent federation in the first release.
- Automatically granting approval because an agent, comment, or imported
  artifact says "approved", "GO", or equivalent.
- Sending every tool call or token to the human issue timeline.

---

## 6. Global invariants

These are testable requirements, not guidance.

### 6.1 Identity and authority

1. Actor identity comes from authenticated user/session context or the
   per-agent token, never a request body.
2. Every issue, session, event, delivery, checkpoint, assignment, and run is
   workspace scoped at the database query and authorization layer.
3. A peer message cannot grant a capability, satisfy a Harbormaster approval,
   change policy, or impersonate a human.
4. Human owner and agent delegate are separate facts.
5. `Start` must resolve an executable agent target; a human owner ID is never
   passed to the orchestrator as an agent ID.

### 6.2 Delivery

1. The comment/event and its intended deliveries commit atomically.
2. A duplicate HTTP request, webhook retry, reconnect replay, or dispatcher
   retry creates no duplicate logical delivery or concurrent run.
3. A queued delivery survives API/server restart.
4. A claimed delivery has a lease and is recoverable after worker death.
5. Consumption advances a cursor only after the context was accepted for the
   run/checkpoint transaction.
6. An undeliverable mention is visible to the author and on the issue.

### 6.3 Session execution

1. At most one active run exists for an issue-agent session.
2. New normal input during an active run is queued for the next safe boundary.
3. A control event follows its own priority path and may request cancellation
   or interrupt where supported.
4. Session state is never inferred solely from a WebSocket connection.
5. Provider session IDs may be cleared or rotated without changing the
   Crewship session ID.

### 6.4 Issue truth

1. `DONE` and `REVIEW` are impossible while non-terminal required children or
   linked active work remain, unless a human uses an explicit audited override.
2. Runs includes every execution causally linked to the issue, regardless of
   whether it came from a mission task, direct `/assign`, mention, routine, or
   resumed session.
3. A cancelled issue cannot be resurrected by a late completion callback.
4. WebSocket events are invalidation hints. Reconnect always reconciles through
   the authorized HTTP read model.
5. The canonical issue terminal status is `DONE`; `COMPLETED` remains a run/task
   status and must not be written as an issue status.

### 6.5 Context

1. Stable prompt material remains byte-stable and precedes volatile context for
   provider prompt caching.
2. Context contains no hidden reasoning transcript.
3. The latest checkpoint records completed actions in past tense with temporal
   anchors so a future turn does not execute them again.
4. Untrusted comments, descriptions, PR metadata, and artifacts remain fenced
   as untrusted data when inserted into a model prompt.
5. Context assembly has explicit token/byte budgets and a deterministic
   fallback when summarization or retrieval is unavailable.

---

## 7. Phase 0 — mandatory truth audit before implementation

No schema or runtime work begins until this audit is checked into
`docs/prd/reports/agent-first-issues-baseline.md` plus a machine-readable JSON
companion. Each work package re-runs the relevant slice.

### 7.1 Repository inventory

Produce an emitter/consumer matrix covering:

- every public and internal issue/comment/mention/start/stop/review route;
- every sidecar issue verb;
- every assignment producer (`/assign`, mention, mission task, routine action,
  lead planning, queue recovery);
- every issue, assignment, task, run, steering, inbox, journal, and WS event;
- every frontend subscriber and its reconnect reconciliation path;
- every persisted provenance field and every place it is written/read.

For each row record source file, authorization, request/response shape,
transaction boundary, idempotency mechanism, journal entry, WS event, frontend
consumer, CLI parity, and tests.

### 7.2 State-transition inventory

Generate tables for:

- issue statuses from `internal/statuses.ValidIssueTransitions`;
- mission task statuses;
- assignment statuses;
- run statuses;
- mention dispatch states;
- chat/steering states;
- all terminal/non-terminal classifications used by completion, Runs, active
  counters, UI filters, and cancellation.

Fail the audit if the same status has conflicting terminal semantics or if the
frontend sends a value the API refuses.

### 7.3 Live-data audit

Against a disposable, backed-up dev instance:

1. enumerate issues, children, mission tasks, direct assignments, mention
   assignments, runs, comments, activity, journal rows, and provenance;
2. compare the database truth with board, issue detail, Runs, Activity, and
   Chain responses;
3. reproduce the twelve findings in section 1.1 or attach evidence that a
   current test already prevents each one;
4. measure how many rows have NULL `author_run_id`, `parent_run_id`,
   `chain_origin`, or an agent assignee with no resolvable ID;
5. record the exact queries in the report so another agent can repeat them.

The audit must be read-only. Factory reset, data repair, or status mutation is
not part of evidence collection.

### 7.4 Context baseline

Capture at least these scenarios:

- first delegation to an issue;
- follow-up after one run;
- follow-up after ten comments;
- resume after seven idle days;
- lead delegating two independent children;
- comment arriving while the agent is active;
- restart between comment commit and agent start.

For each, measure input tokens, repeated bytes, prompt-cache hit information
when provided, time to first visible acknowledgement, time to first agent
activity, tool calls used to rediscover context, and whether completed work was
repeated.

No prompt text containing secrets is checked into the report. Store counts,
hashes, bounded redacted samples, and structural sections.

### 7.5 Provider capability matrix

For Claude, Codex, Gemini, Cursor, Droid, and OpenCode adapters, record:

- provider-native continuation/session handle;
- provider compaction support;
- streaming input or interrupt support;
- cancellation support and observed termination behavior;
- whether a fresh process can restore from a Crewship checkpoint;
- event fields carrying resolved model/session IDs;
- behavior on container or server restart.

The product contract must work on the lowest common denominator. Faster live
steering is a capability enhancement, not a requirement for correctness.

### 7.6 Security and failure analysis

Threat-model:

- cross-workspace/crew event injection;
- spoofed actor or target agent;
- self-mention loops and mention storms;
- peer text masquerading as approval/control;
- prompt injection in comments, descriptions, PRs, and attachments;
- duplicate/reordered deliveries;
- stale lease recovery;
- late callbacks after cancellation;
- one agent claiming too many issues or files;
- secrets copied into event bodies, checkpoints, summaries, or UI;
- shared filesystem artifacts changed after a checkpoint references them.

### 7.7 Phase 0 exit criteria

- The two baseline report files exist and cite current source/tests.
- Every section 1.1 finding is reproduced, disproved, or already test-pinned.
- The proposed schema below is reconciled with migrations that landed after
  this PRD's baseline.
- Every new mutating route planned below has an owner and CLI parity decision.
- Initial red tests exist for the first implementation work package.
- Existing issues #2256, #2257, #2125, #1768, #1836, #1623, #1692, #2240, and
  #2241 are checked for overlap before creating new tracker items.

---

## 8. Target architecture

```text
human / agent / routine
          |
          | comment, mention, assignment, result, control
          v
  transactional issue event writer
          |
          +----> issue activity + journal + WS invalidation
          |
          +----> per-session delivery outbox
                          |
                          v
                 session dispatcher/lease
                          |
                 +--------+---------+
                 |                  |
            idle session       active session
                 |                  |
             start run         queue/steer/control
                 |                  |
                 +--------+---------+
                          v
                    context assembler
        stable prompt + issue snapshot + checkpoint + delta
                          |
                          v
                     agent runtime
                          |
            semantic activity + checkpoint + artifacts
                          |
                          v
                 consume cursor / next delivery
```

The issue event writer owns the atomic boundary. Activity, journal, WS, inbox,
and external notifications are projections. A projection failure may be
repaired; it must not erase the source event or its delivery obligation.

---

## 9. Data model

Exact migration timestamps are generated when each work package starts. Do not
copy a timestamp from this PRD. One SQL file per migration; append after the
latest shipped version and run `go run ./scripts/lint-migrations`.

### 9.1 Ownership fields on `missions`

Add canonical, separately typed ownership:

```sql
ALTER TABLE missions ADD COLUMN owner_user_id TEXT REFERENCES users(id);
ALTER TABLE missions ADD COLUMN delegate_agent_id TEXT REFERENCES agents(id);
```

Rules:

- backfill `owner_user_id` from `assignee_type='user'` where resolvable;
- backfill `delegate_agent_id` from `assignee_type='agent'` where resolvable;
- retain `assignee_type/assignee_id` as a compatibility projection during the
  migration window;
- delegation changes `delegate_agent_id`, never `owner_user_id`;
- issue Start uses `delegate_agent_id` or an explicitly selected executable
  agent, never a polymorphic human ID;
- public DTOs expose `owner` and `delegate` independently;
- the eventual removal of legacy assignee fields is a separate versioned PRD.

### 9.2 `issue_agent_sessions`

```sql
CREATE TABLE issue_agent_sessions (
    id                    TEXT PRIMARY KEY,
    workspace_id          TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    mission_id            TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
    agent_id              TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    state                 TEXT NOT NULL CHECK (state IN (
                              'pending','queued','active','awaiting_input',
                              'complete','error','stale','cancelled')),
    active_run_id         TEXT,
    provider_session_id   TEXT,
    last_consumed_event_id TEXT,
    checkpoint_version    INTEGER NOT NULL DEFAULT 0,
    wake_generation       INTEGER NOT NULL DEFAULT 0,
    lease_owner           TEXT,
    lease_expires_at      TEXT,
    last_activity_at      TEXT,
    completed_at          TEXT,
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL,
    UNIQUE (mission_id, agent_id)
);
```

The one-row-per-issue-agent rule gives the product a stable conversation. A
terminal session may be re-opened by incrementing `wake_generation`; its prior
runs and checkpoints remain attached. If future product research requires
multiple independent conversations for the same pair, add an explicit thread
dimension later rather than silently minting unrelated sessions now.

`active_run_id` receives a real FK only if Phase 0 confirms a single canonical
run table/ID across assignment and agent-run producers. Do not add a misleading
FK to one partial run table.

### 9.3 `issue_coordination_events`

This is the durable coordination stream, not a replacement for the journal's
platform-wide audit. It must not be deleted by the journal's retention policy.

```sql
CREATE TABLE issue_coordination_events (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    mission_id     TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
    kind           TEXT NOT NULL CHECK (kind IN (
                       'message','question','request','finding','result',
                       'blocker','decision','handoff','alert','control','system')),
    actor_type     TEXT NOT NULL CHECK (actor_type IN ('user','agent','system')),
    actor_id       TEXT,
    body           TEXT,
    payload_json   TEXT NOT NULL DEFAULT '{}',
    source_type    TEXT NOT NULL,
    source_id      TEXT NOT NULL,
    reply_to_id    TEXT REFERENCES issue_coordination_events(id),
    target_agent_id TEXT REFERENCES agents(id),
    author_run_id  TEXT,
    created_at     TEXT NOT NULL,
    UNIQUE (workspace_id, source_type, source_id, kind, target_agent_id)
);
```

SQLite treats NULLs as distinct in UNIQUE constraints. Producers that emit a
non-targeted event must use a deterministic non-NULL dedup key or a dedicated
`dedup_key TEXT NOT NULL UNIQUE` column after Phase 0 chooses the precise
shape. The implementer must not assume the illustrative UNIQUE above dedups
NULL targets.

The body is an immutable bounded snapshot of the human-readable message. The
original comment remains in `mission_comments` during compatibility. Comment
editing, if introduced, creates a new revision event; it never mutates history
that a session may already have consumed.

### 9.4 `issue_session_deliveries`

```sql
CREATE TABLE issue_session_deliveries (
    id              TEXT PRIMARY KEY,
    workspace_id    TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    session_id      TEXT NOT NULL REFERENCES issue_agent_sessions(id) ON DELETE CASCADE,
    event_id        TEXT NOT NULL REFERENCES issue_coordination_events(id) ON DELETE CASCADE,
    status          TEXT NOT NULL CHECK (status IN (
                        'queued','claimed','delivered','consumed','failed','cancelled')),
    dedup_key       TEXT NOT NULL,
    priority        INTEGER NOT NULL DEFAULT 0,
    attempts        INTEGER NOT NULL DEFAULT 0,
    available_at    TEXT NOT NULL,
    claimed_at      TEXT,
    lease_expires_at TEXT,
    delivered_at    TEXT,
    consumed_at     TEXT,
    last_error      TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    UNIQUE (session_id, dedup_key)
);
```

`delivered` means accepted into a run's context assembly. `consumed` means the
run/checkpoint transaction advanced beyond it. The distinction lets an
operator answer whether the agent merely received a message or demonstrably
continued after it.

### 9.5 `issue_session_checkpoints`

```sql
CREATE TABLE issue_session_checkpoints (
    id                 TEXT PRIMARY KEY,
    workspace_id       TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    session_id         TEXT NOT NULL REFERENCES issue_agent_sessions(id) ON DELETE CASCADE,
    version            INTEGER NOT NULL,
    run_id             TEXT,
    through_event_id   TEXT REFERENCES issue_coordination_events(id),
    summary_md         TEXT NOT NULL,
    plan_json          TEXT NOT NULL DEFAULT '[]',
    facts_json         TEXT NOT NULL DEFAULT '[]',
    blockers_json      TEXT NOT NULL DEFAULT '[]',
    artifact_refs_json TEXT NOT NULL DEFAULT '[]',
    context_hash       TEXT NOT NULL,
    created_at         TEXT NOT NULL,
    UNIQUE (session_id, version)
);
```

Checkpoint fields contain conclusions and working state, not hidden reasoning.
Writes are monotonic: `version = previous + 1` under a transaction/CAS.

### 9.6 Optional subscriptions

`issue_participants` may be added when the UI needs durable human unread state
or agent topic subscriptions. It must distinguish `owner`, `delegate`,
`reviewer`, and `watcher`, plus a delivery mode (`direct`, `all`, `muted`). Do
not block the initial agent-session delivery on building a full notification
preference system.

---

## 10. State machines

### 10.1 Agent session

```text
pending -> queued -> active -> awaiting_input -> active
                    |   |             |
                    |   +-----------> complete
                    +---------------> error
                    +---------------> cancelled

complete/error/stale --new prompt--> pending
```

- `pending`: a delivery exists and dispatch has not evaluated capacity.
- `queued`: capacity/admission prevents immediate execution.
- `active`: exactly one run owns the session lease.
- `awaiting_input`: the agent explicitly asked a human/agent question or an
  approval waitpoint exists.
- `complete`: current requested outcome was reported; future prompts may reopen.
- `error`: attempt failed and no retry is currently scheduled.
- `stale`: no heartbeat/activity within the configured threshold while work is
  expected; this is visible, not silently treated as complete.
- `cancelled`: an authoritative control action ended the session.

Every transition uses a compare-and-swap on the expected prior state. A late
run may append diagnostic evidence but cannot overwrite a newer generation's
state.

### 10.2 Delivery

```text
queued -> claimed -> delivered -> consumed
   |         |            |
   +-------> failed <------+
   +-------> cancelled
```

Expired `claimed` rows return to `queued` with `attempts+1`. Retry limits and
backoff are configured. After the final failure the issue receives an `alert`
event and the human owner sees a visible failure state.

### 10.3 Input routing

| Input | Recipient | Active-session behavior |
|---|---|---|
| Explicit `@agent` mention | Mentioned agent session(s), max existing mention fan-out | Queue into each existing session; create missing sessions. |
| Reply to an agent activity | That activity's agent session | Queue for next safe boundary; optionally live-steer if adapter supports it. |
| Plain comment with one primary delegate | Primary delegate session | Queue and acknowledge. |
| Plain comment with several participants | Primary delegate only; others receive human notification according to subscription | UI asks for an explicit target when ambiguity matters; never wake all by default. |
| Agent result/handoff | Coordinator/parent session waiting on that assignment | Resume only the causally waiting session. |
| Human approval | Exact waitpoint/session that requested it | Resume the waitpoint; do not create a generic assignment. |
| `STOP`, `HOLD`, `RESUME`, `VETO` | Server control path | Enforce policy and state transition; text alone is not authoritative. |

### 10.4 Issue rollup

Define one server function that computes required open work from:

- non-terminal child issues;
- non-terminal mission tasks;
- all issue-linked assignments, including direct and mention assignments;
- active/queued issue agent sessions;
- unresolved mandatory waitpoints.

`REVIEW` is allowed only when required work is terminal and at least one
successful result exists. `DONE` requires a human/policy-approved review where
review is configured. `FAILED` must explain which required branch failed.

An explicit human override records actor, reason, prior rollup, and ignored
open work in the coordination stream and journal.

---

## 11. Context assembly contract

### 11.1 Context pack order

1. Stable Crewship security/identity/tool instructions.
2. Stable workspace/team guidance and agent persona.
3. Current issue snapshot:
   - identifier, title, current description revision;
   - outcome and acceptance criteria;
   - owner, primary delegate, participants;
   - status, priority, dependencies, children summary;
   - allowed scope, budget, deadline, approval policy.
4. Latest structured session checkpoint.
5. Unread coordination events after `last_consumed_event_id`, exact and in
   order, with actor and timestamps.
6. Relevant memory retrieval, separately labeled from issue facts.
7. Attachment/code-link/artifact manifest with IDs, digests, versions, and
   safe read paths.
8. The immediate requested outcome and stopping/escalation conditions.

### 11.2 Budget policy

Phase 0 sets empirical defaults. Initial guardrails:

- stable prefix: provider-cache optimized, not dynamically summarized;
- issue snapshot: hard bounded;
- checkpoint: 15–25% of available volatile context;
- unread exact delta: newest directive thread preserved verbatim within bound;
- older issue events: compacted, never silently dropped;
- memory: relevance-scored and capped;
- artifacts: manifest only, contents fetched on demand.

When the pack exceeds budget:

1. collapse machine logs and already-consumed system activity;
2. compact older events while retaining decisions, completed actions, IDs,
   blockers, and acceptance criteria;
3. drop low-relevance memory;
4. retain the newest human directive and unresolved questions verbatim;
5. fail visibly if the remaining authoritative input cannot fit.

### 11.3 Checkpoint production

After each run or safe boundary, Crewship asks for or derives structured output:

```json
{
  "summary": "Past-tense account of work completed",
  "plan": [{"step":"...","status":"pending|active|complete|cancelled"}],
  "facts": [{"text":"...","source_event_id":"..."}],
  "blockers": [{"text":"...","needs":"human|agent|system"}],
  "artifacts": [{"attachment_id":"...","digest":"...","purpose":"..."}],
  "next_action": "One concrete next action",
  "completion": "continue|awaiting_input|complete|failed"
}
```

Use Structured Outputs when a provider supports them; validate server-side for
all providers. Invalid output falls back to a deterministic minimal checkpoint
from terminal run fields and remains marked degraded.

### 11.4 Provider continuation

- If the adapter supports a provider-native session, store its opaque ID on the
  Crewship session and resume it when safe.
- If context health degrades, the provider session disappears, or the model
  changes, start a fresh provider context from the Crewship context pack.
- Never make correctness depend on provider-native memory.
- Existing conversation compaction remains available for long same-provider
  turns; issue checkpointing is the cross-run, cross-provider contract.

---

## 12. API and event contracts

Phase 0 must reconcile exact paths with the generated OpenAPI conventions.
Prefer extending existing issue comment routes over adding redundant message
routes.

### 12.1 Public API

Proposed reads:

```text
GET /api/v1/crews/{crewId}/issues/{identifier}/sessions
GET /api/v1/crews/{crewId}/issues/{identifier}/events?after={eventId}&limit={n}
GET /api/v1/crews/{crewId}/issues/{identifier}/runs
```

Extend comment creation:

```json
POST /api/v1/crews/{crewId}/issues/{identifier}/comments
{
  "body": "Please use the compact header on mobile",
  "kind": "request",
  "reply_to_event_id": "...",
  "target_agent_ids": ["..."]
}
```

The response includes the persisted comment/event and delivery dispositions:

```json
{
  "comment": {"id":"..."},
  "event": {"id":"...","kind":"request"},
  "deliveries": [
    {"agent_id":"...","session_id":"...","status":"queued"}
  ]
}
```

Proposed authoritative control route:

```text
POST /api/v1/crews/{crewId}/issues/{identifier}/sessions/{sessionId}/control
{"action":"stop|hold|resume|veto","reason":"..."}
```

Every new mutation receives a CLI command and an executable contract test, per
repository convention. Candidate CLI surface:

```text
crewship issue sessions <IDENT>
crewship issue events <IDENT> --after <ID>
crewship issue session control <IDENT> <SESSION> --action stop --reason "..."
```

### 12.2 Agent sidecar

Add scoped reads:

```text
GET /issue/{identifier}/comments?after={eventId}&limit={n}
GET /issue/{identifier}/context?after={eventId}
```

The context endpoint returns authoritative structured data, not a pre-rendered
system prompt. Prompt fencing and assembly remain server-owned. The bearer
token selects the actor and limits issue visibility to the bound scope.

The existing comment/update/link/attachment verbs remain. Comment responses
must expose mention/delivery outcomes to an agent author instead of requiring a
human inbox recipient.

### 12.3 WebSocket events

New dotted event names:

```text
issue.comment.created
issue.session.created
issue.session.queued
issue.session.active
issue.session.awaiting_input
issue.session.completed
issue.session.failed
issue.session.stale
issue.delivery.failed
issue.control.applied
```

Existing `issue.created`, `issue.updated`, `issue.deleted`, and `issue.started`
remain. Every server emitter needs:

- a frontend allowlist entry;
- at least one consumer or a documented intentional absence;
- a reconnect reconciliation read;
- a coverage test proving the event is not dropped.

WS payloads contain IDs and bounded state only. The client re-reads authorized
content through HTTP; sensitive comment/artifact contents are not broadcast.

### 12.4 Runs response

Runs must use an explicit issue link, not inference from `mission_tasks` alone
or overloading `chat_id/group_id`. Add `issue_id` to assignments if Phase 0
confirms no equivalent canonical column landed.

Each run row exposes:

- assignment/run ID and issue session ID;
- agent and triggering actor;
- cause kind and cause event ID;
- parent assignment/run and chain origin;
- status, queue/start/end times, duration, cost;
- bounded result/error;
- whether it is current, retry, cancelled, stale, or superseded.

---

## 13. Human experience

### 13.1 Board

The board must update without refresh for create, delete, status, ownership,
delegate, session, child progress, and failure events. It must show:

- human owner avatar;
- primary agent delegate and additional active participants;
- active/queued/waiting/stale/error state;
- child completion rollup;
- last meaningful update;
- unread human/agent messages;
- blocker and review indicators;
- cost when available.

Pagination is server-driven and reports a total or next cursor. A failed fetch
renders an error/retry state, never an empty-board lie.

### 13.2 Issue detail

One readable timeline merges:

- human and agent messages;
- questions, findings, decisions, blockers, handoffs, and results;
- assignment/session lifecycle;
- child progress and approvals;
- artifacts and code links;
- control actions and overrides.

Low-level tool calls and logs are collapsed behind an expandable run detail.
The timeline must answer "why is this agent running?" through a causal link to
the comment, assignment, routine, or parent run that triggered it.

### 13.3 Immediate response semantics

"Immediate" means the platform truth appears immediately, not that a cold model
must finish thinking instantly.

- comment persisted acknowledgement: p95 under 500 ms;
- session visible as pending/queued/active: p95 under 1 s;
- warm-capacity first agent acknowledgement/activity: p95 under 10 s;
- cold/provisioning/queue path: UI shows the real reason and progress within
  1 s; no false "working" state;
- a message arriving during a run shows `queued for current session` or
  `steering requested`, not a second agent spinner.

### 13.4 Ownership language

The UI uses:

- **Owner:** responsible person;
- **Delegate:** primary working agent;
- **Participants:** other agents/people contributing;
- **Reviewer:** person or policy gate accepting completion.

Never label an agent as the owner merely because it received a task.

---

## 14. Runtime, cancellation, and recovery

### 14.1 Dispatcher

The issue-session dispatcher uses the existing assignment admission queue.
It claims a session and delivery atomically, creates or links an assignment,
and starts a run. If capacity is unavailable, both the assignment and session
show queued state.

Dispatch claims require a lease. Startup and periodic recovery scan:

- expired session leases;
- expired claimed deliveries;
- PENDING/QUEUED assignments without a live dispatcher;
- active sessions whose run is terminal or missing;
- queued events behind a just-completed run.

Recovery is idempotent and emits one visible repair event.

### 14.2 Safe boundary behavior

For ordinary input during a run:

1. persist event and delivery;
2. acknowledge immediately;
3. if live steering is supported and safe, notify the active harness;
4. otherwise consume after the current tool/model step or current run;
5. checkpoint, advance cursor, and continue in the same session.

Do not interrupt every informational comment. Classification may use explicit
UI intent first (`message`, `correction`, `stop`, `approval`) and model
classification only as a non-authoritative fallback.

### 14.3 Cancellation

`Stop work` must:

1. transactionally record a control event and cancellation generation;
2. stop future scheduling;
3. cancel queued deliveries/assignments for the affected scope;
4. signal the active orchestrator/run cancellation registry;
5. terminate the provider process/container exec within a bounded timeout;
6. mark the run/assignment/session/issue consistently;
7. reject late terminal writes through generation/state CAS;
8. show partial artifacts/results as cancelled evidence, not success.

`MissionEngine.StopMission` or an equivalent narrow interface must be wired
from the issue handler. Merely updating `mission_tasks` is insufficient.

### 14.4 Stalled work

Configurable stale detection uses last activity/heartbeat and expected state.
A lead exiting while required child work remains is `stale` or `awaiting`, not
implicitly successful. The system may retry within policy; after retry budget
it alerts the owner with the exact stalled session and last checkpoint.

---

## 15. Implementation roadmap

Each work package is one tracker issue and normally one PR. Claim it before the
first commit. If a package grows beyond reviewable size, split it along the
listed acceptance boundaries rather than combining packages.

### WP-0 — Truth audit and failing acceptance harness

Deliver section 7 reports, reusable fixtures, and failing tests for the first
vertical slice. No production behavior change.

Acceptance:

- emitter/consumer/state/provenance inventories are machine-readable;
- all twelve findings have evidence;
- baseline latency/token/readiness numbers are recorded;
- the first red E2E proves mention/follow-up cannot currently complete the
  target lifecycle.

### WP-1 — Execution truth: explicit issue linkage and ownership

Add canonical issue linkage to assignments/runs and separate owner/delegate.
Expand Runs to every producer. Validate Start target type.

Acceptance:

- direct `/assign`, mention, mission-task, routine, and resumed-session runs
  all appear exactly once on the linked issue;
- a human owner remains after delegation;
- Start with only a human owner returns a clear 4xx and starts no run;
- provenance fields resolve causally in Chain/Activity;
- legacy assignee clients remain compatible during migration.

### WP-2 — Durable coordination events and session delivery outbox

Create event/session/delivery schemas and one transactional writer. Route human
and agent comment mentions through it without changing dispatch semantics yet.

Acceptance:

- comment + event + intended delivery are atomic;
- duplicate requests and mention duplicates create one logical event/delivery;
- foreign-workspace targets produce no resolvable row or side channel;
- restart after commit leaves a queued recoverable delivery;
- delivery failure is visible to the author and issue.

### WP-3 — Issue agent session lifecycle and dispatcher

Create/reopen sessions, lease them, bind assignments/runs, and enforce one
active run per session.

Acceptance:

- an idle mention creates one session and one run;
- ten concurrent duplicate wakes still create one active run;
- capacity limits move the session to queued and later active in order;
- server death after claim recovers the delivery after lease expiry;
- state is visible through API and WS.

### WP-4 — Agent issue context API and context assembler

Expose agent-scoped comment/event delta, build the context pack, and attach
issue ID/session ID/event cursor to every issue assignment.

Acceptance:

- agent can read the exact issue thread/delta in its permitted crew scope;
- first run receives snapshot + directive; later run receives checkpoint +
  unread delta without replaying the entire thread;
- cross-workspace and sibling-crew probes fail closed;
- untrusted data is fenced;
- deterministic no-summarizer fallback works;
- context token use falls by the Phase 0-agreed target without reducing eval
  completion quality.

### WP-5 — Checkpoints and durable continuation

Persist validated structured checkpoints; resume provider-native sessions when
healthy and fresh contexts otherwise.

Acceptance:

- restart/container recreation/provider-session loss resumes from checkpoint;
- completed work remains dated past-tense and is not repeated in the eval set;
- invalid checkpoint output produces a visible degraded fallback;
- checkpoint versions cannot race or go backwards;
- no hidden reasoning is stored.

### WP-6 — Replies, plain delegated comments, and steering

Route explicit mentions, replies, and primary-delegate comments into the same
session. Connect active-session input to the existing steer path and implement
adapter-aware safe-boundary behavior.

Acceptance:

- reply to an agent activity reaches that exact session;
- plain comment on a singly delegated issue reaches its primary delegate;
- active input starts no second concurrent run;
- unsupported adapters queue safely;
- supported live steering is capability-detected and injection-scanned;
- UI shows queued/steering/consumed disposition.

### WP-7 — Truthful rollup, review, stop, and stale detection

Centralize open-work computation; wire real cancellation; prevent terminal
resurrection; detect stale coordination.

Acceptance:

- parent cannot enter REVIEW/DONE with required open children or work;
- explicit override is human-only, reasoned, and audited;
- Stop terminates active execution and queued work;
- late completion cannot overwrite cancellation;
- all-failed work becomes FAILED, not REVIEW;
- stale lead/session produces a visible state and owner alert.

### WP-8 — Realtime issue board and readable activity

Complete event allowlist/subscribers, reconnect reconciliation, pagination,
error states, session badges, ownership, and semantic timeline.

Acceptance:

- create/update/delete/comment/session/run/child changes repaint board and
  detail without refresh;
- disconnect/reconnect converges to HTTP truth within five seconds;
- >100 issues are navigable with correct total/cursor;
- a failed fetch is visibly failed;
- `DONE` issues remain visible under Done filters/actions;
- machine logs are collapsed while decisions, blockers, results, and control
  actions remain readable.

### WP-9 — Typed coordination, claims, and artifacts

Add semantic event composer/renderers, server-enforced CLAIM/RELEASE/HOLD/VETO,
and artifact digest/provenance.

Acceptance:

- typed events round-trip via UI/API/CLI/agent tools;
- control text without the control API has no authority;
- conflicting exclusive claims are refused atomically;
- artifact references resolve to immutable digest/version evidence;
- replacing a shared-path file cannot silently change an earlier handoff.

### WP-10 — Evals, load, rollout, and documentation

Run the complete section 17 matrix, add dashboards/alerts, publish public docs,
and progressively enable the feature.

Acceptance:

- all reliability/SLO/client-beta gates in sections 17–19 pass;
- old/new read models are compared during shadow mode with no unexplained
  divergence;
- rollback does not lose events or comments;
- OpenAPI, CLI docs, guides, security docs, and screenshots are current;
- readiness is re-scored from evidence, not copied from this PRD.

---

## 16. Files and subsystems likely affected

Phase 0 owns the final inventory. Expected touch points:

| Area | Likely packages/files | Purpose |
|---|---|---|
| Database | `internal/database/migrations/` | ownership, explicit issue link, sessions, events, deliveries, checkpoints, indexes |
| Issue API | `internal/api/issue_handler_*`, `issues_internal*`, `issue_events.go`, `issue_mentions.go` | transactional writes, routing, rollup, reads, controls |
| Assignment runtime | `internal/api/assignments_*`, `delegation_limits.go` | issue/session provenance, queue integration, cancellation, completion wake |
| Mission engine | `internal/orchestrator/mission_tasks*`, `mission.go` | parent completion invariants, coordinator resume, stop |
| Context | `internal/orchestrator/orchestrator_run*`, `agent_brief.go`, `memory.go`, `session_context.go` | bounded pack, checkpoint, provider continuation |
| Steering | `internal/api/chat_steer.go`, `internal/chatbridge/steer.go`, adapter/provider exec paths | safe active-turn input and queued fallback |
| Sidecar | `internal/sidecar/issue_verbs.go`, sidecar routing/prompt docs | thread/context delta, delivery response, actor scope |
| Journal/inbox | `internal/journal`, `internal/notifyroute`, `internal/inbox` | projections, alerts, external human notifications |
| Realtime | `internal/ws`, `hooks/use-realtime.tsx`, issue hooks | event allowlist, subscribers, reconnect reconciliation |
| Frontend | `app/(dashboard)/issues`, `components/features/orchestration`, new issue-session components | board/detail/timeline/ownership/session UX |
| Types/contracts | `lib/types`, OpenAPI generator, CLI commands/docs | canonical vocabulary and parity |
| Security | `internal/policy`, Keeper, Harbormaster, untrusted/scrubber paths | authority, injection, cancellation, artifact trust |
| Tests | Go unit/integration/race, Vitest, Playwright, migration lint | full contract |

Avoid broad refactors unrelated to the work package. Preserve user WIP and
claim the relevant tracker issue before the first commit.

---

## 17. Acceptance and test matrix

### 17.1 Golden end-to-end scenarios

1. **Idle mention:** human mentions an idle agent; one session/run appears,
   acknowledgement is live, agent responds on the issue, no refresh.
2. **Duplicate delivery:** the same comment request/webhook is replayed ten
   times; one event, delivery, session wake, and active run exist.
3. **Active follow-up:** human comments during a tool-heavy run; no parallel
   turn starts; next safe boundary receives the comment exactly once.
4. **Course correction:** human marks a follow-up as correction; the prior plan
   is superseded and checkpoint records why.
5. **Awaiting input:** agent asks a question; session becomes awaiting input;
   reply resumes that same session.
6. **Approval:** an exact waitpoint resumes on human approval; peer text saying
   "approved" does nothing authoritative.
7. **Stop:** human stops work; process terminates, queued work is cancelled,
   late result cannot resurrect state.
8. **Restart:** server restarts after event commit, after delivery claim, and
   during active run; no message is lost and recovery is visible.
9. **Seven-day return:** a fresh container/model continues from checkpoint and
   does not repeat completed work.
10. **Parallel children:** lead creates independent child issues, dispatches in
    parallel within admission limits, receives each handoff, and parent waits.
11. **Failure rollup:** every child run fails; parent becomes FAILED with causal
    evidence, not REVIEW.
12. **Human ownership:** delegating/redelegating never removes the owner.
13. **Ambiguous comment:** several agent participants exist; plain comment wakes
    only the primary delegate and UI makes targeting explicit.
14. **Malicious input:** comment/PR/artifact contains instruction injection and
    fake approval; it remains untrusted and gains no authority.
15. **Large board:** 500+ issues paginate, update live, reconnect, and retain
    correct counts/filters.

### 17.2 Unit and property tests

- state transition tables reject illegal edges;
- rollup truth tables cover every child/task/assignment/session combination;
- routing tables cover input kind × participants × active state;
- context selection is deterministic under a fixed clock and budget;
- checkpoint validation rejects hidden/unknown fields and oversized values;
- dedup keys are stable;
- cursor ordering never omits or repeats committed events;
- actor/target scoping is tenant-safe;
- cancellation generation/CAS rejects late writers.

### 17.3 Concurrency and race tests

- 100 simultaneous wakes for one session produce one lease/active run;
- concurrent checkpoint writes yield one next version;
- delivery lease expiry and completion racing do not double-consume;
- comment commit and Stop racing have a defined order and terminal result;
- parent completion racing child creation cannot incorrectly finalize;
- run completion racing cancellation cannot resurrect state;
- run targeted tests under `go test -race`.

### 17.4 Migration tests

- clean database applies all new migrations;
- representative legacy database upgrades without data loss;
- owner/delegate backfill is deterministic;
- foreign key check passes;
- duplicate legacy links do not violate new uniqueness;
- rollback/feature-disable leaves legacy issue reads functional;
- `go run ./scripts/lint-migrations` passes and no shipped SQL changed.

### 17.5 API/CLI/security tests

- every new mutation has authentication, role, tenancy, malformed body,
  idempotency, and happy-path coverage;
- every mutation has CLI parity and a binary-level contract test;
- OpenAPI response/error envelope matches the surrounding handler;
- agent token cannot spoof actor/workspace/crew/session;
- read endpoints reveal no foreign event existence;
- control actions enforce human/policy authority;
- rate/fan-out/depth/budget gates apply to session wakes.

### 17.6 Frontend tests

- reducer/hook tests for every realtime event;
- reconnect invokes reconciliation;
- optimistic acknowledgement reconciles to server disposition;
- error and queue states are accessible and not color-only;
- owner/delegate labels remain distinct;
- timeline collapses noisy activity but preserves semantic events;
- pagination/filter URL state survives refresh/back-forward;
- static-export Playwright journey covers the golden issue flow.

### 17.7 Required verification loop per implementation PR

```bash
go test ./... -count=1
go vet ./...
pnpm lint
pnpm build
go run ./scripts/lint-migrations   # when migrations are touched
go run ./scripts/agents-invariants
```

Run focused `go test -race` and Playwright jobs for affected high-risk paths.
Do not merge before CodeRabbit has actually posted a review; a green skipped or
rate-limited check is not a review.

---

## 18. Metrics and service levels

Phase 0 records baseline and may tighten these targets. Client beta requires:

| Metric | Gate |
|---|---:|
| Comment persistence acknowledgement | p95 < 500 ms |
| Session state visible after accepted mention | p95 < 1 s |
| First visible agent activity, warm and capacity available | p95 < 10 s |
| Board/detail live invalidation | p95 < 1 s |
| Reconnect convergence to HTTP truth | p95 < 5 s |
| Committed events lost across restart tests | 0 |
| Duplicate active runs from redelivery | 0 |
| Issue Runs coverage against DB-linked executions | 100% |
| Stop to provider process termination | p95 < 5 s, bounded hard timeout |
| Repeated completed work in continuity eval | < 1% of cases |
| Context input reduction on repeated wake | >= 60% versus full-thread replay, with no quality regression |
| Unexplained rollup mismatch | 0 |
| Cross-workspace disclosure/dispatch in security suite | 0 |

Also record cost per completed issue, context tokens per wake, duplicate tool
calls, queue time, cold-start time, recovery count, delivery retry count,
checkpoint fallback rate, stale-session rate, and human time-to-understand in
dogfood review.

---

## 19. Readiness model and release gates

The pre-implementation estimate is:

| Capability | Estimated readiness at baseline |
|---|---:|
| Execution, async delegation, mission DAG, queue | 75% |
| Container/sidecar/policy/budget substrate | 70% |
| Issue API, comments, mentions, attachments | 60% |
| Compaction and curated brief primitives | 60% |
| Durable issue session and wake continuation | 30% |
| Guaranteed internal delivery and cursors | 25% |
| Execution truth, rollup, cancellation | 40% |
| Realtime human board and explanation | 40% |
| **Weighted end-to-end estimate** | **~55%** |

This number is a planning hypothesis. WP-0 replaces it with measured evidence.

Release stages:

- **Internal alpha (~65–70%)**: WP-0 through WP-4; messages are durable and
  sessions/context exist, but UX/controls may be incomplete.
- **Dogfood (~75%)**: WP-0 through WP-7; continuation, rollup, and cancellation
  are truthful.
- **Client beta (>=85%)**: WP-8 through WP-10 pass all golden, restart,
  concurrency, security, and SLO gates.
- **General availability**: at least two real client workflows meet SLOs for a
  sustained observation window with no P0/P1 coordination-loss defect.

Do not use 100% as a release claim. Report measured success, failure, latency,
and recovery rates instead.

---

## 20. Rollout and compatibility

1. Add an instance/workspace feature gate for agent issue sessions.
2. Ship schema and dual-write event/session data while old readers remain.
3. Shadow-compare legacy Runs/rollup with the new canonical read model.
4. Enable for an internal workspace and record divergences.
5. Enable session wake routing for explicit mentions only.
6. Add replies and plain primary-delegate comments after duplicate/restart
   metrics are clean.
7. Enable authoritative controls and rollup enforcement.
8. Switch board/detail to new reads with legacy fallback.
9. Remove fallback only in a later cleanup after a complete release window.

Disabling the feature stops new session dispatch but never deletes coordination
events, comments, checkpoints, or deliveries. Rollback readers must continue to
render ordinary comments and issue state.

---

## 21. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Every comment wakes a swarm | Explicit routing; primary delegate default; mention fan-out cap; subscriptions do not imply wake. |
| Duplicate or reordered work | Durable dedup keys, leases, session mutex/CAS, monotonic checkpoints, idempotent recovery. |
| Context summary invents facts | Source event IDs on facts, exact newest directive, deterministic fallback, evals for completed-action repetition. |
| Event log duplicates journal/activity | Define coordination events as canonical delivery source and journal/activity as projections; one shared writer. |
| Schema becomes another mission abstraction | Keep issue/session/run/event definitions explicit; add canonical `issue_id` rather than overloading chat/group IDs. |
| Provider-specific continuation locks the product in | Crewship checkpoint is canonical; provider handle is optional/opaque. |
| Immediate steering destabilizes a live tool call | Queue normal input to safe boundary; explicit controls use bounded cancellation; capability detect live injection. |
| Peer manipulates authority | Server-enforced controls and approvals; actor identity from auth; peer prose never grants capability. |
| Artifacts mutate under a handoff | Issue attachment IDs, digest/version, provenance; shared path alone is not evidence. |
| Timeline becomes unreadable | Semantic event types; collapsed tool/log details; filters and audience-aware projection. |
| Costs rise through excessive multi-agent fan-out | Existing depth/fan-out/admission/paymaster gates; complexity-based delegation; per-issue/session budgets. |
| Stale documents mislead implementers | Phase 0 truth audit, source/test citations, revalidation at each claimed issue. |

---

## 22. Existing issue and PRD relationship

Before creating tracker work, inspect and link rather than duplicate:

- **#2256** — retain as umbrella evidence if still open; split implementation
  invariants into this PRD's work packages.
- **#2257 / #2125** — realtime symptom and dropped-event coverage; correct the
  diagnosis against the current emitter/allowlist/consumer matrix.
- **#1768** — older agent-first issue work; mark shipped mention/attachment
  slices and retain unresolved pagination/search items.
- **#1836** — composition/provenance and "anything starts anything" trace.
- **#1623 / #1692** — memory delivery/peer memory gaps.
- **#2240 / #2241** — crew network and egress containment.
- `.claude/context/prd/AGENT-CONTINUITY-2026.md` — conversation compaction,
  steering, memory, and user-model roadmap.
- `docs/prd/agent-memory-on-wake.md` — wake-context delivery findings.
- `.claude/context/prd/QUEUE-MECHANISM-2026.md` — admission control contract.
- `docs/prd/chat-as-a-primary-surface.md` — chat/realtime/static-export test
  constraints.

This PRD owns issue-scoped coordination. It references but does not absorb
global memory, network isolation, generic chat UX, or notification-channel
roadmaps.

---

## 23. Decision log

| Decision | Rationale |
|---|---|
| Issue is the durable work room | It is already the shared human object for goals, status, comments, children, and artifacts. |
| Session is distinct from run | Processes fail and contexts reset; the human relationship must survive both. |
| Fresh bounded context is preferred over full replay | Lower cost/noise and better coherence while preserving exact unread directives. |
| Human owner remains after delegation | Accountability and client control must not disappear when an agent starts work. |
| Plain comments do not wake every participant | Prevents storms, duplicated work, and unpredictable cost. |
| Delivery is persisted separately from WS | WS is best-effort UI invalidation, not a durable queue. |
| Coordination event is separate from 30-day journal retention | Session continuation cannot depend on an audit row that may be compacted/deleted. |
| Controls are typed server operations | Text from peers or untrusted sources cannot carry authority. |
| Existing queue, compaction, brief, mention, and notification patterns are reused | Reduces parallel mechanisms and preserves proven gates. |
| One large PRD, many small implementation PRs | One product contract keeps the model coherent; small claimed work avoids conflicts and enables review. |

---

## 24. Definition of done for this PRD

This PRD is complete only when:

1. Phase 0 evidence replaced every unverified baseline claim.
2. All WP-0 through WP-10 acceptance criteria pass on the current branch.
3. The golden E2E suite passes against the production static export.
4. Restart, duplicate, concurrency, cancellation, and tenancy tests pass.
5. Board, issue detail, API, CLI, sidecar, journal, inbox, and Chain agree on
   ownership, active work, cause, and terminal state.
6. A human can mention, delegate, correct, stop, approve, and resume work while
   seeing immediate truthful status.
7. An agent can resume after a week or container loss from a compact checkpoint
   plus unread delta without redoing completed work.
8. Peer collaboration remains bounded by policy, authority, scope, budget,
   depth, fan-out, and artifact trust.
9. Client-beta SLOs and readiness gate are met from measured runs.
10. Public documentation describes actual, test-protected behavior and all
    overlapping issues/PRDs are reconciled.
