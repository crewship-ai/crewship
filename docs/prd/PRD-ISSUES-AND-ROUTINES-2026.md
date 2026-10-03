# Issues and Routines — public design and acceptance reference

Status: technical extract of the 2026-09-01 design, not a current release
readiness statement. Internal audits, live-instance data, work packages,
market research and delivery logs are retained in private context.
Numbered sections remain stable for source and regression references.
Historical finding and work-package identifiers are provenance labels, not
instructions to repeat an internal audit or claims of outstanding defects.
Start with [restricted workflows](../specs/restricted-workflows.md) and
[issue preflight](../specs/private-issue-preflight.md) for current boundaries.

## 1. How to read this document

Read this as design and acceptance context. Verify implementation claims
against current source and tests. Use the public specifications for supported
behaviour; earlier section numbers and finding IDs identify design provenance.

## 2. Verified baseline — what is actually true today

The original source audit and live-instance measurements are maintained in
private context. They are historical observations, not current product state.
The public requirements, state machines and acceptance scenarios follow below.

## 3. Corrections — where earlier assessments were wrong

Internal review corrections are preserved with the original private record.
Public consumers should rely on the requirements and explicit limits below.

## 4. The product model

Seven objects. The whole design is making these real in the schema and honest in the UI.

| Object | Meaning | Today |
|---|---|---|
| **Issue** | The outcome a human cares about, and where work is judged. | `missions` — real. |
| **Event** | An immutable fact: someone said, decided, finished, failed. | Partly `mission_activity`; no ordering guarantee, no delivery semantics. |
| **Delivery** | The record that a specific event was handed to a specific worker, and whether it was consumed. | **Does not exist** (F4). |
| **Session** | One agent's continuing relationship to one issue, across many runs. | **Does not exist** (F1, F13). |
| **Run** | One attempt — one shift. Can fail without the session failing. | `assignments` / `pipeline_runs` — real but unlinked (F1). |
| **Checkpoint** | Structured work state handed from run N to run N+1. | Three fields, one code path (F15). |
| **Outcome** | The client-meaningful result that decides what happens next. | **Does not exist** (F22). |

And two triggers of work: a **Routine** (recurring, defined) and a **Mention/Assignment** (ad hoc, human-initiated). Both must produce the same Run → Outcome → routing behaviour. Today they do not share a single line of that path.

**Non-negotiable framing:** the issue is the control plane for judged work; the comment table is not a message queue. Delivery gets its own row (the generalised mentions table, §9.3) precisely so that a comment stays a comment.

---

## 5. Goals and non-goals

### Goals

G1. A mention or assignment reaches the intended agent exactly once, survives restart, and is visibly acknowledged to the human in under one second.
G2. A follow-up comment while an agent is working does not start a second run; it is delivered to the existing session.
G3. An agent resuming an issue after days knows what it already did before it acts, and does not redo finished work or rediscover state by re-reading everything. (Measured as repeat-work and time-to-first-productive-action, **not** as token reduction — see §11.4.)
G4. Stop means stopped: the process ends, and no late callback can revive the state.
G5. Every run is attributable to an issue and a session, and the Runs surface shows all execution regardless of trigger.
G6. Authoring a routine from chat produces routine **and** trigger atomically, or nothing.
G7. Every routine run ends with an explicit outcome that deterministically decides whether a human is bothered.
G8. The Inbox contains only things a named person can act on, with per-user read state and an immutable record of what was decided.
G9. A trigger that should have fired and did not is visible without reading logs.
G10. Reliability settings that exist in the backend are reachable by the people who need them.

### Non-goals

N1. Mid-token interruption of a model turn. Delivery lands at the next safe checkpoint (F3, F16). Say so in the UI.
N2. Rewriting the pipeline executor. Extend the existing executor.
N3. Cross-replica distributed execution. Leases are for correctness under restart and future replicas, not a scale-out project (F8, F23).
N4. A second orchestration path parallel to missions/pipelines.
N5. Solving cost-accounting completeness. F12/F25 are named and bounded here (A5 fixes the misleading label; a session-level ceiling with a named stop reason is separate work).
N6. Replacing the journal. It stays the audit trail; it stops being the only home for state that must outlive 30 days (F36).

---

## 6. Invariants

I1. **Exactly-once consumption.** An event is delivered to a worker at most once successfully; redelivery after crash must not duplicate work. Enforced by a unique key on `(event_id, target)` plus a consumption CAS.
I2. **One active turn per session.** Two runs of the same session cannot be RUNNING simultaneously. Enforced by a partial unique index, not by application politeness.
I3. **A terminal state is terminal.** No write may move a row out of CANCELLED/COMPLETED/FAILED without an explicit, audited override (fixes F7).
I4. **Truth beats label.** A UI state must be derived from the thing it claims. "Running" means a live run row with an unexpired lease, not a status guess (F31's static export makes client-derived state especially tempting — resist).
I5. **Human owner, agent delegate.** Delegating to an agent never changes the human owner of an issue.
I6. **A peer's approval is not a human approval.** Agent-sourced acknowledgement can never satisfy a waitpoint, gate, or policy decision.
I7. **Workspace scoping is absolute.** Every new table carries `workspace_id` and every query filters on it — the existing mention triggers (`trg_mission_comment_mentions_consistency_ins`) are the model to copy.
I8. **Nothing that must persist lives only in `journal_entries`** (F36).
I9. **Every new API endpoint ships with a CLI command and an acceptance test that drives the binary** (project rule; also the three-gate trap in `docs-inventory -strict`).
I10. **Silence is a decision.** Any path that chooses not to notify a human must record that it chose (outcome + reason), so "why didn't I hear about this" is answerable.

---

## 7. A0 — mandatory truth audit before any schema change

Before schema changes, inspect current migrations, API authorization, event
writers and regression tests. Verify table/column names and compatibility.
Use synthetic fixtures for repeatable validation; keep instance audit results
and work coordination in private context.

## 8. Target architecture

One substrate, two entry points.

```
   HUMAN                          TIME / EVENT
   comment, @mention, /assign     schedule, webhook, automation
        |                                   |
        v                                   v
   +----------------------------------------------------+
   |  EVENT  (immutable fact, ordered per issue)         |
   +----------------------------------------------------+
        |
        v
   +----------------------------------------------------+
   |  DELIVERY  (event -> target, exactly-once claim)    |
   |  unique(event_id, agent_id)  (§9.3)                 |
   +----------------------------------------------------+
        |
        +--- session idle -----> wake: start a RUN
        |
        +--- session active ---> enqueue for the next safe
                                 checkpoint of the live RUN
                                 (STOP is the exception: it
                                  cancels immediately)
        |
        v
   +----------------------------------------------------+
   |  RUN  (leased, cancellable, attributable to issue)  |
   |     assignments.*        |     pipeline_runs.*      |
   +----------------------------------------------------+
        |
        v
   +----------------------------------------------------+
   |  CHECKPOINT  (structured state for the next run)    |
   +----------------------------------------------------+
        |
        v
   +----------------------------------------------------+
   |  OUTCOME  (NO_CHANGE | SUCCEEDED | WORK_CREATED |   |
   |            PARTIAL | NEEDS_HUMAN | FAILED |         |
   |            CANCELLED)                               |
   +----------------------------------------------------+
        |                |                |            |
        v                v                v            v
    history only    issue comment /    INBOX      System Health
    (digest)        new issue          (a person   (operator, not
                                        must act)   the client)
```

Why this shape:

- **Delivery is a separate record from the comment** — a row in the generalised mentions table, never a column on `mission_comments` — so the comment table stays a comment table (F4), so redelivery is provable, and so "the agent never saw it" stops being indistinguishable from "the agent ignored it".
- **Session sits between issue and run** so a run may fail without losing the relationship, and so I2 has something to be unique on (F1, F13).
- **Outcome is separate from status** because status answers "did the process end" and outcome answers "should a human care" (F22). Routing off status is why success and infrastructure noise land in the same list today.
- **Both entry points converge before Outcome**, so the Inbox has one contract to honour rather than three producers with three conventions (F20, F30).

---

## 9. Data model — three new tables, not six

**Rev 3 rewrite.** Rev 1 proposed six new tables. A simplification pass against the code found that three of them fold into tables that already exist and already have the right shape. The guarantees are unchanged; the number of things to reason about is halved.

Rules that apply to everything below: migrations are timestamped SQL files with strictly ascending stamps generated at rebase time (F54); every table carries `workspace_id` with its own cascade, never relying on the mission chain (F55, I7); every state machine copies the CAS shape of `PendingRunStore.MarkFired` (F57); every new table passes the §16.1 integration checklist before it is done.

### 9.1 Events → widen `mission_activity`, do not create `issue_events`

`mission_activity` (`internal/database/migrate_consts_v33_v41.go:198-206`) already has `mission_id`, `actor_type` with the exact `CHECK(actor_type IN ('user','agent','system'))` this design wants, `actor_id`, `action`, `details`, `created_at`, and one central writer (`internal/api/issue_events.go:196-206`).

Add four nullable columns: `seq INTEGER`, `payload_json TEXT`, `source_kind TEXT`, `source_id TEXT`. Plus `UNIQUE(mission_id, seq)` and an index on `(source_kind, source_id)`.

**Three things this costs, all of which must be done and none of which are optional:**

1. **`mission_activity` has no `workspace_id`** — verified against the live schema. I7 requires one. Add it nullable, backfill by joining `missions`, then enforce. This is the single biggest cost of the merge and it is still cheaper than a parallel table.
2. **`action` has no CHECK constraint** — it is an open set today, and the emitter's own comment admits it. Constrain it now, while the table is being widened anyway. Live values to preserve: `status_changed`, `task_completed`, `parent_changed`, `assignee_changed`, `created`, `task_failed`, `review_approved`, `priority_changed`. Note what is *absent* from that list: **there is no `commented` action**. Today this table is a status-change audit, not an event log — widening it is a genuine change of purpose and the PR must say so.
3. **Two writers bypass the emitter** and insert directly (named in `internal/api/issue_events.go:60-66`: `assignments_run.go` and `orchestrator/mission_tasks_completion.go`). They must move to the emitter or allocate `seq` themselves. A row without `seq` is invisible to every cursor.

`seq` is allocated with the `nextIssueIdentifierTx` pattern re-keyed on `mission_id` (F58) — `UPDATE ... RETURNING` inside the caller's transaction, race-free because SQLite serializes writers. Wall-clock `created_at` is not orderable enough under concurrent writes.

### 9.2 `issue_agent_sessions` — new, irreducible, thinner than rev 1

Nothing in the schema has `UNIQUE(mission_id, agent_id)`. `mission_comment_mentions` is keyed `UNIQUE(comment_id, agent_id)` — many rows per agent per mission, the wrong cardinality to hold one evolving cursor. `mission_tasks.handoff_context` is one overwritten column scoped to a task. So a durable row is genuinely required.

| Column | Notes |
|---|---|
| `id`, `workspace_id`, `mission_id`, `agent_id` | `UNIQUE(mission_id, agent_id)`. Cascade on `workspace_id` (F55). |
| `state` | `pending` \| `active` \| `awaiting_input` \| `idle` \| `error` \| `stale` \| `closed` |
| `last_consumed_seq` | The cursor. Everything above it is unread for this session. |
| `active_run_id` | NULL unless a run is live. |
| `agent_version` | Stamped from `agent_config_history` at creation (§11.6). |
| `last_activity_at`, `created_at`, `updated_at` | |

Dropped from rev 1: `opened_by_user_id` and `opened_reason` — log them in the event payload instead of carrying columns nothing queries.

### 9.3 Deliveries → generalise `mission_comment_mentions`, do not create `agent_deliveries`

`mission_comment_mentions` already has `dispatch_state`, `assignment_id`, a workspace-consistency trigger set, and a unique index. It is 90% of the delivery table.

Widen it: `comment_id` becomes nullable; add `event_id TEXT` referencing the event row, `state`, `claimed_by_run_id`, `priority`; replace `UNIQUE(comment_id, agent_id)` with `UNIQUE(event_id, agent_id)` — that index is invariant I1 in one line.

**Two costs, both verified:**

1. **`dispatch_state` and `state` are different questions.** The existing column answers "did the dispatcher create an assignment" (`dispatched|refused|skipped|failed`); the new one answers "did a run consume this" (`pending|claimed|consumed|failed|superseded`). Keep both. Conflating them loses the distinction between "never dispatched" and "dispatched but never consumed", which is exactly the F4 blind spot.
2. **A nullable `comment_id` breaks the existing trigger.** `trg_mission_comment_mentions_consistency_ins` does `SELECT mission_id FROM mission_comments WHERE id = NEW.comment_id`; with a NULL comment id that subquery yields NULL, `NULL IS NOT NEW.mission_id` is true, and the insert **aborts**. SQLite has no `ALTER TRIGGER`, so the migration must drop and recreate both the insert and update triggers with a `NEW.comment_id IS NOT NULL AND ...` guard. Verified directly against the live schema — do not discover this in review.

Claim and consume are separate CAS statements (F57). A crash between them leaves a `claimed` row whose run is dead; the lease sweep returns it to `pending` with `attempts+1`. This is the shape `notification_deliveries` already uses in production (`UNIQUE(channel_id, dedup_key)` plus `internal/notifyroute/recovery.go:197-218`) — and note its backup classification is `IntentExcludeOperational`, which is a decision this table must make consciously (F37).

### 9.4 `assignments` — six new columns, not eight

| Column | Why it is required |
|---|---|
| `mission_id` | F1. Track A2. |
| `session_id` | Links a run to its session; only needed because §9.2 exists. |
| `lease_owner`, `lease_expires_at` | No lease mechanism exists at all (F8). |
| `cancel_requested_at` | `status` carries no cooperative-cancel signal (F6). Track A1. |
| `outcome` | §9.6. Routing today can only read `status`, which `internal/consolidate/mission_outcome.go:33-46` shows collapsing to a 3-way squash. |

Dropped from rev 1: `outcome_reason` (reuse the existing `result_summary`/`error_message` rather than adding a third free-text column) and `cancel_requested_by` (attribute it in the journal entry, the way `harbormaster.Decide` already attributes approvals).

Indexes: `(mission_id, created_at DESC)`; `(session_id)`; `(lease_expires_at) WHERE status='RUNNING'`; and

```
CREATE UNIQUE INDEX idx_assignments_one_active_per_session
  ON assignments(session_id)
  WHERE status IN ('PENDING','QUEUED','RUNNING') AND session_id IS NOT NULL;
```

That index is invariant I2. **It guards nothing until `insertCappedAssignment` sets the column** — `cappedAssignment` (`internal/api/delegation_limits.go:509-516`) has no `SessionID` field and its INSERT (`:566-568`) does not list one. Resolve-or-create the session inside the same transaction as the fan-out guard, or the TOCTOU this index exists to close comes straight back.

Prior art: `chatbridge.tryMarkRunStart` (`steer.go:60-77`) was this same guarantee, in memory, keyed on `chat_id`, and it left the assignment door open (F51). A9 has since extracted it as `AgentRunLock`, re-keyed it on the agent, and closed that door. B3 is the next step: the same guarantee in the database, keyed on the session, so it survives a restart and a second replica.

### 9.5 `agent_session_checkpoints` — new, one JSON column

`HANDOFF` (`internal/orchestrator/mission.go:100-137`, enforced at `mission_tasks.go:321-329`) is the right parsing and enforcement machinery and should be reused as-is. Its *storage* cannot serve: `mission_tasks.handoff_context` is a single overwritten column, per task, not per session, with no sequence marker. §9.5's "keep all rows" requirement rules it out.

`id`, `workspace_id`, `session_id`, `run_id`, `seq_at_write`, `checkpoint_json`, `created_at`.

One JSON column, not four — that is this codebase's convention (`payload_json`, `approvals_queue.payload`, `pipeline_waitpoints.decision_payload`). The document schema inside it stays as rev 1 specified: `done`, `plan`, `facts`, `blockers`, `next_step`, `confidence`.

Explicitly not in `journal_entries` (I8, F36).

### 9.6 Outcome — shared enum for both entry points

Unchanged from rev 2. `outcome` on `assignments` and `pipeline_runs`, with a CHECK:

| Value | Meaning | Default routing |
|---|---|---|
| `NO_CHANGE` | Ran, nothing to do. | History only. Digest-eligible. |
| `SUCCEEDED` | Did the work, nothing needed. | History + digest. |
| `WORK_CREATED` | Produced or updated an issue. | Comment on the issue, deduped by `thread_key`. |
| `PARTIAL` | Some done, some failed, no human needed yet. | History + issue comment. |
| `NEEDS_HUMAN` | Blocked on a decision, input or credential. | **Inbox**, with an action contract (§12). |
| `FAILED` | Ran and failed after retries. | Inbox once retries are exhausted. |
| `CANCELLED` | Stopped by a human or superseded. | History. |

Set by the runner, never inferred by a consumer. A run ending without one is `FAILED` with `outcome_reason='no outcome reported'` — an absent outcome is a bug, not a silent success. `status` stays technical; `runverdict` stays an advisory LLM judgment. Three fields is one more than ideal, so the PR that adds `outcome` also documents the difference in `docs/guides/routines.mdx`.

### 9.7 `inbox_item_reads` — new, and already minimal

`(inbox_item_id, user_id)` composite PK, `read_at`. Read state becomes a LEFT JOIN. The existing `read_at`/`read_by_user_id` columns stay and keep answering "someone dealt with it", which is a different question. This is Track A7.

### 9.8 Decision receipts → two columns, not a table

Rev 1 proposed `decision_receipts`. Almost all of it already exists:

- `approvals_queue` has `decided_by`, `decided_at`, `payload` (the verbatim request) and `Decide` already emits a matching journal entry (`internal/harbormaster/store.go:23-38`).
- `pipeline_waitpoints` has `decided_by_user_id`, `decided_at`, `decision_payload`, `status`.
- `inbox_items` has `resolved_by_user_id`, `resolved_at`, `resolved_action`.
- Session control (Stop) gets the same via `cancel_requested_at` plus its journal entry.

The one thing genuinely missing across all four is the question §9.8 was invented to answer: *was it the same version that then ran?* So add **`routine_version` (or `definition_hash`)** to `approvals_queue` and `pipeline_waitpoints`. Nothing else. The journal entry already carries payload, refs and trace id as the effect record.

What is lost: a single cross-kind query target — four call sites instead of one JOIN. That is a real but small cost, and it is smaller than a seventh table whose "append-only" would have been convention-only anyway (F42).

### 9.9 Migration and backfill

- One migration per package; never edit a shipped one (`scripts/lint-migrations`, plus the strictly-ascending scheme).
- **Stamp at rebase, not at branch start** (F54).
- Backfill `assignments.mission_id` from `mission_comment_mentions.assignment_id`; expect ~0 rows and write the non-zero test anyway.
- `mission_activity.workspace_id` backfills by joining `missions`.
- The `mission_comment_mentions` triggers must be dropped and recreated, not altered (§9.3).
- **Net: 3 new tables, 4 widened tables, ~7 migrations.** The migration count barely moves; what halves is the number of tables and joins anyone has to hold in their head.

### 9.10 Ownership fields on `missions` (ported from rev 1 — Track A10)

Invariant I5 says the human owner stays the owner when an agent is delegated to, and scenario 9 tests it. Until rev 3 this document had no schema that could enforce either: `missions` carries a polymorphic `assignee_type`/`assignee_id`, and delegation overwrites it with the agent (historical design observation). A UI that renders the agent in the owner slot is a truth defect in a shipped surface.

```sql
ALTER TABLE missions ADD COLUMN owner_user_id    TEXT REFERENCES users(id)  ON DELETE SET NULL;
ALTER TABLE missions ADD COLUMN delegate_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL;
```

Rules:
- backfill `owner_user_id` from rows where `assignee_type='user'`, `delegate_agent_id` from `assignee_type='agent'`;
- `assignee_type`/`assignee_id` stay as a compatibility projection for the migration window; their removal is a separate versioned change;
- delegation writes `delegate_agent_id`, never `owner_user_id`;
- Start uses `delegate_agent_id` or an explicitly chosen executable agent — the typed FK is what closes F62 (Start validated existence, not executability);
- public DTOs expose `owner` and `delegate` independently.

**Why Track A and not B:** it is additive, nullable, backfilled — the same shape as A2's `mission_id` — and it is the schema behind an invariant Track A already claims and a scenario Track A already lists. It is the largest A item and should ship last in A. `ON DELETE SET NULL` for the same reason A2 chose it (F55): deleting a user or agent must not delete the issue.

## 10. State machines

### 10.1 Session

```
            mention / assign / routine
  (none) ─────────────────────────────> pending
                                           │ run claimed
                                           v
                    ┌──────────────────> active ──────────────┐
                    │                      │                  │
   delivery arrives │       run ends       │ run needs human  │ run fails
                    │                      v                  v
                  idle <───────────── awaiting_input        error
                    │                      │                  │
                    │  no activity 14d     │ human answers    │ human retries
                    v                      └──────────────────┘
                  stale ──────── human or agent reopens ──────> pending
                    │
                    v  issue terminal
                 closed
```

Rules: `pending → active` requires winning the run-claim CAS; only a live run may hold `active`; the lease sweeper moves an `active` session whose run lease expired to `error` with a reason; `closed` is set when the issue reaches DONE/CANCELLED/DUPLICATE and is the only state that refuses new deliveries (they are recorded `superseded`, never dropped silently — I10).

### 10.2 Delivery

```
pending ──claim CAS──> claimed ──consume CAS──> consumed
   ^                      │
   └── lease reaped ──────┘ (attempts+1; after 5 → failed, raises NEEDS_HUMAN)
   │
   └── target closed ────> superseded
```

### 10.3 Run cancellation — two tiers, honestly labelled (fixes F6, F7)

Rev 1 claimed a hard kill. The code has no kill primitive and the container is shared by the crew. So cancellation ships as **two separate guarantees, delivered in order, each labelled in the UI as what it actually is.**

**Tier 1 — Cooperative stop (achievable now, no new container capability).**

```
RUNNING ──human Stop──> cancel_requested_at set + journal entry (§9.8)
                          │
                          ├─ in-process: the run's context is cancelled, so no
                          │  further model call, tool call or step is started
                          │
                          └─ other replica / after restart: the runner polls
                             cancel_requested_at at every step boundary
                          │
                          v
                 status=CANCELLED, outcome=CANCELLED
                          │
                          v
   every later write for this run is REFUSED by the terminal-state guard
```

This alone fixes F6's worst half (the run keeps *starting new work* today) and all of F7. The honest label is **"Stopping — will finish the current step"**, and the UI must say that rather than implying an instant kill.

**Tier 2 — Hard termination (new capability, separately scoped).**
Requires: persisting `ExecResult.ExecID` (`internal/provider/container.go:194-197`) on the run; discovering the PID inside the container from `ExecInspect`; and signalling that process — Docker has no "kill this exec" call, so this is `kill -TERM <pid>` executed *into* the same container, never `docker kill` on the container itself, which would take out every sibling agent on the crew. Only when Tier 2 lands may the UI promise a bounded stop time.

Terminal-state guards are Tier 1 and non-negotiable: every write to `mission_tasks.status` and `assignments.status` gains `AND status NOT IN ('CANCELLED','COMPLETED','FAILED')`, matching what `missions` already does at `internal/orchestrator/mission.go:497`.

Golden scenario 5 is split accordingly (§18).

### 10.4 Issue status and children (fixes F10)

Add one rule: an issue with non-terminal sub-issues (`parent_issue_id`) or non-terminal `mission_tasks` may not transition to `DONE` or `REVIEW` without `?force=true`, which emits a hash-chained journal entry naming who forced it (§9.8). Everything else in `ValidIssueTransitions` stays.

### 10.5 Input routing (ported from rev 1 — the Track B contract)

Sessions do not exist yet (§9.2 is B1), so this table is the contract B2 implements, not current behaviour. It is here so that "which session does this input wake" is decided once, in writing, rather than per call site.

| Input | Recipient | While a session is active |
|---|---|---|
| Explicit `@agent` mention | The mentioned agent's session(s), within the existing mention fan-out cap | Queue into each existing session; create the missing ones. |
| Reply to an agent's activity | That activity's agent session | Queue for the next safe boundary (N1); live steer only if the adapter supports it — none does today (F45). |
| Plain comment, one delegate | The delegate's session | Queue and acknowledge. |
| Plain comment, several participants | The primary delegate only; others get a human notification per subscription | Ask for an explicit target when it matters. **Never wake everyone by default.** |
| Agent result or hand-off | The parent session waiting on that assignment | Resume only the causally waiting session. |
| Human approval | The exact waitpoint or session that asked | Resume that waitpoint; never mint a generic assignment. |
| `STOP`, `HOLD`, `RESUME`, `VETO` | The server control path (§14.1) | Enforce policy and the state transition; **text alone is never authoritative** (I6). |

---

## 11. Context assembly — the "clean mind" contract

The target is *not* more memory. It is a small, exact packet plus the delta since the agent last looked.

### 11.1 What a woken agent receives

Ordered, and budgeted:

1. **Stable system prompt** — agent persona and permissions. Unchanged; must stay byte-stable for cache reuse (F16).
2. **Issue snapshot** (≤ 800 tokens): identifier, title, goal, acceptance criteria, status, human owner, labels, dependencies, links.
3. **Latest checkpoint** (≤ 600 tokens): `done`, `plan`, `facts`, `blockers`, `next_step`, `confidence` (§9.5).
4. **Unread delta only** (≤ 1200 tokens): `mission_activity` rows where `seq > last_consumed_seq` (§9.1), oldest first, each rendered as `#seq · actor · kind · text`. Over budget → oldest are summarised into one line each, never dropped silently.
5. **Relevant memory** — existing tiered memory and episodic recall, unchanged (`internal/orchestrator/memory.go:111-163`, `internal/episodic/hybrid.go:98-247`).
6. **Artifact manifest** (≤ 300 tokens): files touched, PRs, prior runs, with ids to fetch.

Everything else stays retrievable through tools, never auto-injected. Add a sidecar verb `GET /issue/{id}/comments` — today the sidecar can write a comment but cannot read the thread (`internal/sidecar/issue_verbs.go` is write-only for comments), which is why an agent that wants history has no option but to be handed all of it.

### 11.2 Where it goes

Into the **volatile user-message block** (`[SESSION CONTEXT]`), never the cached system prefix (F16). This is not a style preference: putting per-wake content in the system prompt breaks the prompt cache for every subsequent turn that day.

### 11.3 What the agent must write back

A checkpoint at the end of every run, and at every waitpoint. Enforce it the way HANDOFF is enforced (`internal/orchestrator/mission_tasks.go:321-329`) but for all session runs, and record `Parsed=false` explicitly when the model does not comply (`mission_tasks_completion.go:98` is the existing precedent) so the failure is measurable rather than invisible.

### 11.4 What this is optimising for — and what it is not

Rev 1 set a "≥60% fewer input tokens" target. That was wrong: on the mention path there is no conversation history in the prompt to remove, and today's brief is a few hundred tokens. The pack proposed here is *bigger*, deliberately — the defect is not bloat, it is that the agent arrives knowing nothing and must rediscover state by tool calls, every time, with no record of what it already did.

The correct objectives, all measurable:

| Objective | Metric | Direction |
|---|---|---|
| The agent does not redo finished work | repeat-work rate: steps in run N+1 that duplicate a step recorded done in run N's checkpoint | → 0 |
| The agent does not spend its first minutes rediscovering state | context-gathering tool calls before the first productive action | ↓, measured against the A0 baseline |
| Context stays bounded as an issue ages | assembled pack size in tokens | **capped**, not minimised: ≤ 2900 + memory, and it must not grow with thread length |
| A degraded context is visible | share of runs whose context was truncated rather than summarised | reported, alarmed above a threshold |

Note the third row: the property that matters is that a 200-comment issue and a 5-comment issue produce the *same size* wake, because only the delta and the checkpoint are injected. That is the actual win, and it is a bound, not a reduction.

**Compaction must also stop failing silently.** `buildConversationContextWithStats` records which path it took — fit, summarised, or truncated — as a field on the run, not only as a journal entry (F14, F36, I8). A run whose context was truncated is today indistinguishable from one that fit, and it is the likeliest cause of "the agent forgot".

### 11.5 Interruption is an event type, not a side channel

Interruption is a session event with a priority, consumed at the next safe
boundary. Keep one ordered history so the stop request and observed stop can
be reconstructed. Delivery priorities remain `stop` > `correction` > `normal`.

### 11.6 Pin the agent version to the session

`agent_config_history` already exists (`agent_id`, `version`, `changes`, `snapshot`, `UNIQUE(agent_id, version)`) and **nothing reads it** — its only references are the schema, the backup manifest and a test. Meanwhile an agent's system prompt can be edited mid-session and no run records which version it ran under. Pin the agent version so a session can identify the configuration it used.

`issue_agent_sessions` therefore carries `agent_version INTEGER NULL`, stamped at session creation from `agent_config_history`. Cheap, and it makes "why did it behave differently on Thursday" answerable.

---

## 12. Inbox — the attention contract

The Inbox is a queue of things a *named person* can act on. Three lanes, server-decided.

**Requires action.** Only where a specific, permitted action exists: approve/reject, supply missing input, connect a credential, choose between options, review output, resume a disabled routine, take over a blocked issue. Every item carries an **action contract**:

```json
{
  "attention_class": "decision|input|review|repair",
  "thread_key": "routine:daily-triage:2026-09-01",
  "who_can_act": ["role:MANAGER", "user:usr_123"],
  "actions": [
    {"id": "approve", "label": "Approve", "effect": "Resumes run prn_… at step 4", "irreversible": false}
  ],
  "deadline_at": "2026-09-02T09:00:00Z",
  "context": {"issue": "WEB-13", "routine": "daily-triage", "run": "prn_…"}
}
```

`thread_key` is **server-side** (fixes the client heuristic at `inbox-v2-derive.ts:192-220`): the same recurring condition updates one card instead of creating a new one every morning.

**Updates.** Agent finished, routine created an issue, a rule auto-disabled, non-blocking warnings, and the **digest** of successful and no-change runs — the `'digest'` state that already exists in the schema and has no scheduler (F30). B10 gives it one.

**History.** Resolved items plus the decision records of §9.8 — `decided_by`/`decided_at`/`routine_version` on the subject rows and their hash-chained journal entries — searchable.

Two hard rules:
- `NO_CHANGE` and `SUCCEEDED` never create an item (I10 means they are still *recorded*, in the digest).
- Infrastructure failures a client cannot fix (sidecar down, disk, provider outage) go to operator System Health, not the client Inbox. That routing decision belongs to the producer, which is why outcome (§9.6) must be set at the source.

Convergence: `/inbox-v2` stops merging on the client (F28) once the server returns one list with the contract above. Keep it behind the existing feature-flag system (F35) until parity is proven, then promote it to `/inbox`.

---

## 13. Routines — from "engine" to "product"

### 13.1 Atomic authoring (fixes F17)

Extend `save_routine` (`internal/sidecar/routine_mcp.go:22-53`) with an optional `trigger` block:

```json
{
  "definition": {...},
  "trigger": {"kind": "schedule", "cron": "0 9 * * 1-5", "timezone": "Europe/Prague",
              "catchup_policy": "once", "max_consecutive_failures": 5},
  "activation": "draft"
}
```

Server-side, routine + version + trigger are created **in one transaction**; either all exist or none do. `activation: "draft"` creates the trigger disabled and raises a `NEEDS_HUMAN` inbox item — "Routine X is ready. First run would be tomorrow 09:00. Activate?" — which on approval stamps `routine_version` on the approval row and emits the hash-chained journal entry that is the decision record (§9.8). The authoring skill (`internal/skills/bundled/crewship/routine-author/SKILL.md`) must be updated in the same PR to require the trigger and to state, in its final message: what was created, when it first runs, and whether it is active or awaiting approval.

A routine with no trigger and no explicit `"trigger": "manual"` is a warning on the routine page, not a silent success.

### 13.2 The reliability surface, made reachable (fixes F18)

The schedule editor replaces the raw cron box with:

| Control | Backed by |
|---|---|
| **When** — natural language + cron + **preview of the next five fire times in the chosen timezone** | `cron_expr`, `timezone` (`internal/pipeline/schedules.go:735,751`) |
| **If it overlaps** — skip / queue / replace / parallel | `concurrency_key`, `max_concurrent` (F23 — and fix the 429-vs-queue doc lie, `docs/guides/routines.mdx:2047`) |
| **After downtime** — skip / run once / backfill all (cap 20) | `catchup_policy` (`schedules.go:785-822`) |
| **Only run if** — wake gate + fail-open/fail-closed | `wake_pipeline_id`, `wake_fail_closed` (`:1192-1218`) |
| **On repeated failure** — disable after N | `max_consecutive_failures` (`:1073-1129`) |
| **Version** — latest or pinned | `target_pipeline_version` (`executor.go:739-754`) |
| **Health** — last run, next run, consecutive failures, **disabled reason**, missed count, wake stats | the seven fields the UI currently never renders |

Nothing new in the backend. This WP is almost entirely frontend plus CLI parity, which is why it is cheap and high-value.

### 13.3 Triggers that can fire (fixes F19, F20, F21)

- **Closed event registry.** Export the journal's entry types as a real registry and validate `event_type` membership, not shape (`internal/api/automations.go:71`). The existing code comment says the registry does not exist; create it, and generate it from `internal/journal/types.go` so it cannot drift.
- **Payload keys validated** against the registered payload schema for that event type at save time.
- **"Test against recent events"** promoted from the opt-in preview endpoint (`internal/api/automations_preview.go:39-91`) into the create flow: you cannot save a rule that matched nothing in 7 days without acknowledging it.
- **Uniform failure observability.** Webhook fire failures and automation enqueue failures emit a journal entry and, on repetition, an inbox item — matching what schedules already do (`internal/pipeline/schedules.go:1058-1069`). Fixes the `logger.Error`-only path at `internal/automation/registry.go:734-736`.
- **"Should have fired, didn't" detector.** A periodic check comparing expected fire times against created runs, surfacing to System Health. `last_missed_count` already exists; nothing reads it.
- **Webhook update route** (F21): `PATCH /api/v1/crews/{crewId}/pipeline-webhooks/{id}` with explicit, opt-in token rotation. Currently editing means losing the URL.

### 13.4 Continuity between runs (three layers, not one)

1. `pipeline_routine_state` — deterministic cursors and watermarks. Exists, keep as is.
2. **Run checkpoint** — the structured state of §9.5, written by agent steps.
3. **Outcome ledger** — the last N outcomes, changes, failures and human decisions for this routine, rendered into the next run's context.

Do not copy the previous run's chat into the next run. The delta plus the checkpoint is the point.

---

## 14. API, events, CLI

Every endpoint below ships with a CLI command and a binary-driving acceptance test (I9), and each one trips three gates: OpenAPI regeneration, `docs-inventory -strict`, and the read-scope invariant test (`ci.yml:1955,1984`; see also open issue #2144).

### 14.1 HTTP

| Method | Path | Role | Purpose |
|---|---|---|---|
| GET | `/api/v1/crews/{crewId}/issues/{identifier}/sessions` | read | Sessions on an issue, with state and unread count. |
| GET | `/api/v1/crews/{crewId}/issues/{identifier}/events?after_seq=` | read | The ordered event log. |
| POST | `/api/v1/crews/{crewId}/issues/{identifier}/sessions/{sessionId}/control` | mutate, MANAGER | `{"action":"stop"\|"pause"\|"resume"\|"handoff"}`. Stamps `cancel_requested_at` and emits the hash-chained journal entry that is the decision record (§9.8). |
| GET | `/api/v1/crews/{crewId}/issues/{identifier}/checkpoints` | read | Checkpoint history for the issue. |
| PATCH | `/api/v1/crews/{crewId}/pipeline-webhooks/{id}` | mutate | F21. |
| GET | `/api/v1/inbox` | read | Now returns `attention_class`, `thread_key`, `actions[]`, per-user `state`. |

The existing `POST .../issues/{identifier}/stop` is **not** left as a second, weaker stop path. A1 makes it delegate to the real cancellation path; its behaviour changes from "flip rows" to "actually stop", which is a fix, not a break. Say so in the changelog.

CLI parity (naming must follow the existing `issue <verb>` convention — `issue session control` would be the first `issue <noun> <verb>` in 28 subcommands; prefer `crewship issue stop <IDENT> [--session S]`, `crewship issue sessions <IDENT>`, `crewship issue events <IDENT> --after 42`, `crewship routine webhooks update`).

### 14.2 Realtime events

New: `issue.session.created`, `issue.session.state`, `issue.delivery.acked`, `issue.checkpoint.written`, `run.outcome`, `routine.trigger.missed`, `inbox.item.actionable`.

**Every one of them must be added to `VALID_REALTIME_TYPES` (`hooks/use-realtime.tsx:141-183`) in the same PR that emits it, with a test.** This is the single enforcement point (F32), and three existing issue events are already dropped there (#2125). A6 fixes those three first; adding seven more (B11) on top of a broken allowlist is how you ship a board that silently never updates. Registration is not delivery — see F43 for the resync rule B11 must also carry.

---

## 15. Human surfaces

**Issue page.** A session strip: which agent is engaged, its state, unread count, current run with elapsed time and a working stop button, and the last checkpoint's `next_step` rendered as one sentence. Sub-agent output must stream (F5) or the strip must say plainly that it will only appear on completion — a spinner that means nothing is worse than a label that admits the limit (I4).

**Acknowledgement under one second.** On comment, the server writes the event and delivery and pushes `issue.delivery.acked` before any model call. The human sees "Frontend received your message" immediately; "Frontend is working" follows when the run claims the session.

**Routine page.** One page answering: what it does, when it next runs, what it did last time (with outcome, not just status), how healthy it is, who it bothers, and what it costs.

**Runs.** `/journal?tab=runs` cannot show routine runs at all today — the read-side SQL excludes them (F33 rev 3). A6 makes the header say so. Until the read side is fixed in its own package, the honest label is the deliverable; a subscription to `pipeline.run.*` alone changes nothing the user can see.

**Owner vs delegate.** The human owner stays the owner. Agent engagement renders as delegation (I5). Never render an agent in the owner slot.

---

## 16. Security, tenancy, privacy

- **Workspace scoping** on every new table and query (I7), with consistency triggers copied from the mention tables.
- **RBAC named per route** (§14.1). No mutating route ships without a role; A0 step 6 confirms the pattern.
- **Peer ≠ human** (I6): the control endpoint and every waitpoint resume must reject an agent-sourced actor for approval semantics.
- **Scrub before persist.** `mission_activity.payload_json` (§9.1) and checkpoint bodies pass through `internal/scrubber` — issues #2215, #2228, #2229 are all "raw prompt text reached an append-only sink". Do not add a fourth.
- **Erasure.** There is no cascade to join (F38): each new table that can hold user text gets `data_subject_id` and the four hand-edits of §16.1. Note open issue #2233: `approvals_queue` currently has no retention sweep — do not copy that omission.
- **Retention.** Checkpoints and deliveries are excluded from the 30-day journal compaction sweep (I8, F36) and get their own retention policy, stated explicitly.


### 16.1 The integration checklist every new table must pass

Rev 2 addition. Each item is a real gate, not advice.

| Step | Where | Consequence of skipping |
|---|---|---|
| Classify for backup | `internal/backup/intent.go:266` (`BackupTableIntent`), plus `dbdump.go:10-14` if there is no FK | CI fails with `ErrDiscoveryDrift` (F37) |
| **Decide, in writing, whether deliveries ride backups** | same map — note `notification_deliveries` is `IntentExcludeOperational` (`intent.go:254`) | Exactly-once survives a crash but not a restore, silently (F37) |
| Add `data_subject_id` migration | pattern at `migrate_consts_v107_gdpr_cascade.go:1-45` | User text becomes unerasable |
| Add the `DELETE` block | `internal/api/admin_gdpr.go:276` (`:404,424,438,525`) | Erasure request misses the table (F38) |
| Add the mirrored `SELECT` | `admin_gdpr.go:637` (`:704,740`) + `gdprActionScope` | Access request returns an incomplete answer (F38) |
| Add metrics | `internal/server/metrics_domain.go:157` (`collectDomainMetrics` fan-out) | The SLO is unmeasurable (F39) |
| Injection-scan replayed content | `internal/lookout` — currently request-scoped only | Stored text is re-fed to a later agent outside any guard (F40) |
| Seed the feature flag | migration, plus one canonical key constant | `IsEnabled` silently returns false everywhere (F49) |

Two further rules:

- **Decision records need integrity, not just convention.** Rev 3 dropped the `decision_receipts` table (§9.8): the decision columns already exist on `approvals_queue` and `pipeline_waitpoints`, and the journal entry each decision already emits is hash-chained (`internal/journal/verify.go:20-41`). That chain, not a new table, is the tamper evidence (F42). The one addition is `routine_version` on both tables so "which version was approved" is answerable; nothing else is append-only by convention.
- **Sweepers are a solved shape here.** Copy `harbormaster.StartTimeoutSweeper` (`internal/harbormaster/gate.go:238`) or `internal/ephemeral/expiry.go`; do not add a third scheduler (F48). And reconcile the two expiry clocks: an ephemeral agent expiring mid-session must close or error its session, not leave it rendered `active` (F41).

---

## 17. Work packages — two tracks, because 1.0 does not mean this

Internal release tracks and work allocation are preserved in private context.
Public acceptance criteria are in §18–§19. Track identifiers in the retained
design are historical references, not a release promise.

## 18. Golden end-to-end scenarios

These are the acceptance suite. Each must fail on current `main` before it passes — **except 11, 12 and 13**, which test engine behaviour that was correct all along and simply never exercised; there, the first green run is the proof (A8), and they pass on `main`.

1. Mention an idle agent → visible acknowledgement < 1s, exactly one session, exactly one run.
2. Ten duplicate deliveries of the same event → one run.
3. Follow-up comment during an active run → no second run, message consumed exactly once.
4. Correction during an active run → reflected in the next step, not the next unrelated run.
5a. **(Track A)** STOP during a run → no further step is started, run ends `CANCELLED`, a late callback changes nothing.
5b. **(Track B7)** STOP during a container exec → the target process is terminated within 5s and a sibling agent on the same crew keeps running.
6. Server restart between event and consumption → nothing lost, no duplicate run.
7. Agent resumes an issue after 7 simulated days → no repeated completed work, and the assembled pack is the same size as on a 5-comment issue (bounded, not reduced — §11.4).
8. Parent issue with an open child → cannot be marked DONE without force; force writes a receipt.
9. Human owner stays owner after delegating to an agent.
10. A peer agent's "GO" cannot satisfy a waitpoint.
11. Scheduled routine fires on time; a wake gate returning false suppresses the run and says so.
12. Downtime spanning three fire times → `catchup_policy` honoured exactly (all three variants tested).
13. Duplicate webhook with the same idempotency key → one run, second returns the first run id.
14. Routine authored in chat → routine + trigger exist together, first fire time stated; rollback test proves atomicity.
15. `NEEDS_HUMAN` → exactly one inbox item with a valid action contract; acting on it resumes the run, writes a receipt, and updates the same thread rather than creating a new card.

---

## 19. Test matrix, metrics, and the commands CI runs

### 19.1 Layers

| Layer | What it must cover | Where |
|---|---|---|
| Go unit | Delivery CAS, session state machine, outcome routing, event-registry validation, terminal-state guards, catch-up arithmetic, DST fire-time computation | table-driven `*_test.go`, `testutil.MigratedSQLDB` |
| **Persistence, not mocks** | Any test of a scheduled or webhook-triggered run must wire a `RunStore` (`Executor.WithRunStore`, `PipelineHandler.SetRunStore`) and assert a `pipeline_runs` row exists. **Every pre-existing schedule and webhook test omits this**, so none could catch a persistence regression — found by `t1`, whose tests failed with zero rows until the store was wired. Needs its own issue. | `internal/pipeline`, `internal/api` |
| Go concurrency | I1 and I2 under `t.Parallel` + `-race`; ten concurrent claims; two concurrent finishers | `internal/api`, `internal/pipeline` |
| Restart | Kill between event and consumption; between claim and consume; mid-step; lease expiry recovery | pattern from `internal/server/running_recovery_boot_test.go` |
| Migration | Upgrade from a populated old DB; backfill correctness; immutability | `scripts/lint-migrations`, `migrate_upgrade_path_oldest_test.go` |
| Security | Cross-workspace read/write refusal on every new route; RBAC per route; peer-vs-human | existing route contract tests |
| Frontend unit | Allowlist registration test; schedule form ↔ backend field parity test | Vitest |
| E2E | Scenarios 1, 3, 5, 11, 15 in a browser — **new Playwright specs**, none exist for inbox or mentions (F34) | `e2e/` |

### 19.2 Commands (from CI, not from memory)

```bash
go test ./... -count=1 && go vet ./...
go test -race -timeout 40m ./internal/api/        # ~23 min; the default 10m timeout is a false failure
go run ./scripts/lint-migrations                  # if migrations changed
golangci-lint run --timeout=5m ./...              # Go Lint gate 1
go run ./scripts/lint-tsformat origin/main        # gate 2
go run ./scripts/docker-api-surface               # gate 3
# gate 4: OpenAPI spec regenerated and committed
go run ./scripts/docs-inventory -strict           # gate 5 — every new endpoint AND CLI command documented
pnpm lint && pnpm build && pnpm test
pnpm test:e2e
```

Two traps worth stating: "Go Lint" is five gates and the annotation names none of them; and a green `gh pr checks` on a short list means CI did not run, not that it passed.

### 19.3 Service levels — targets, not measured

| Metric | Target | Measured |
|---|---|---|
| Comment persisted → acknowledgement visible | p95 < 500 ms | server timestamp → WS emit |
| Session visible as `pending`/`active` | p95 < 1 s | event → session state change |
| First agent acknowledgement (capacity available) | p95 < 10 s | delivery → run claim |
| Lost deliveries | **0** | `pending` older than 5 min, alarmed |
| Duplicate runs per event | **0** | count runs per `event_id` |
| Scheduled fire punctuality | p95 < 60 s of due time | note the 30 s poll floor (F24) *(B16: `crewshipd_schedule_fire_punctuality_seconds`, `started_at − due_at`)* |
| Wake size is bounded, not growing | assembled pack size on a 200-comment issue vs a 5-comment one | equal within tolerance (§11.4) |
| Repeat work after a wake | steps duplicating a checkpointed done step | 0 |
| Inbox items per successful run | **0** | outcome routing *(B16: `crewshipd_inbox_items_per_successful_run`, items joined to `SUCCEEDED`/`NO_CHANGE` runs in both run tables)* |
| Checkpoint compliance | >95% of session runs | `Parsed` flag |

Do not claim any of these before they are instrumented. An uninstrumented SLO is a slogan.

**And instrumentation here is a net-new capability, not wiring (F39).** `/metrics` is hand-rolled Prometheus text computed from DB aggregates at scrape time (`internal/server/metrics_domain.go:94,157`); there is no Prometheus client in `go.mod`, no histograms, and SQLite has no `percentile_cont`. Every p95 above must be built. That is B12, and until it lands the correct statement is "not measured", not "meets target".

Two further honesty notes:

- **Session state is not process state (F47).** `admission.Controller.Admit` (`internal/admission/admission.go:302`) gates container start on host capacity, *after* the run-claim CAS. "Session active within 1s" is a DB fact; under host pressure the process may still be queued. Either report both, or name the metric so it cannot be misread.
- **Acknowledgement targets need measurement.** Instrument acknowledgement and idle transitions; design targets are not verified service levels.

---

## 20. Rollout

For operational delivery use the [runbooks](../runbooks/README.md).
Validate compatibility and rollback against the actual release. This design
does not authorize deployment to a shared development or production instance.

## 21. Risks

| Risk | Mitigation |
|---|---|
| The scope is large enough to become one unreviewable PR | One work package per claimed issue and PR. A PR touching more than one is rejected in review. The one exception is `a1`, which deliberately carries A2 because they touch the same rows. |
| Journal instability undermines run truth (F36) | A0 gates on journal health; nothing durable lives only in `journal_entries` (I8). |
| The realtime allowlist silently swallows the new board (F32) | A6 fixes the existing three and adds a test that fails on unregistered emissions; B11 adds the rest plus client gap-detection, because the hub also drops frames silently under load (F43). |
| Static export limits the live UI (F31) | Everything realtime rides the existing WS provider; no server components are introduced. |
| "One turn per session" makes agents feel slower | It is correct, not slower: the alternative was the second run killing the first (F51). Surface the queued follow-up in the UI so waiting is visible. |
| Outcome becomes a third confusing status field | One PR documents `status` vs `outcome` vs `runverdict` in `docs/guides/routines.mdx`, or B6 is not done. |
| Cost truth stays broken (F12, F25) | Explicitly out of scope (N5), named here so nobody claims budgets work. |
| Estimates drift because nothing was measured first | Rev 2 replaced the −60% token target with the bounded-context metrics of §11.4; A0 still gates on measuring the baseline. |
| **This PRD gets treated as 1.0 scope and delays the release** | Treat the proposal as design context; establish release scope and acceptance explicitly. |
| Track A ships and the deferred limits are quietly forgotten | The deferred list at the end of Track A is a documentation deliverable under 1.0 condition #7, not a footnote. |
| Someone wires a provider's native session resume as an "optimization" | F45: all six adapters are stateless today and OpenCode's `--continue`/`--session` sit unused one line from `BuildCommand`. A second, invisible continuity channel would silently diverge from checkpoints. State the prohibition in the adapter package doc. |
| Hard kill is attempted with `docker kill` | It would SIGKILL every sibling agent on the crew (`crew_resource_drift.go:49`). B7's accept criterion tests exactly this. |

---

## 22. Relationship to existing issues and documents

Related public contracts: [restricted workflows](../specs/restricted-workflows.md),
[issue preflight](../specs/private-issue-preflight.md),
[API response shapes](../specs/response-shape-contract.md), and
[Inbox design](inbox-maximum-wireframe.md). Internal issue assignment and
release planning stay in private context.

## 23. Decision log

Dated internal decision/review notes are preserved privately. The retained
public technical choices are stated with their affected contracts above.

## 24. Readiness — stated honestly, and split by release

This historical design is not a release-readiness report. Prove supported
behaviour with the relevant scenarios and current CI; record unverified
limits explicitly. Local execution and a merged PR do not prove user acceptance.

## 25. Definition of done for this PRD

Public documentation must match supported behaviour; state machines,
authorization and acceptance scenarios need reproducible regression coverage.
Document known limits and distinguish test execution from product acceptance.

## 26. Appendix — what the field does now, and what it changes here

Competitive research and its bibliography are retained in private context.
The product requirements in this document stand on their explicit technical
contracts and acceptance scenarios, without requiring those research notes.

## 27. The empirical gap — what no audit can settle

Static code review does not establish end-to-end behaviour, concurrency
latency or user comprehension. Verify the scenarios in §18 with isolated
fixtures and measure §19 targets before claiming them.
