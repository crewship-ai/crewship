# Handoff: durable work, parallelism and memory — 2026-09-10

Issue [#2488](https://github.com/crewship-ai/crewship/issues/2488) · branch
`feat/durable-work-parallelism` · PR
[#2490](https://github.com/crewship-ai/crewship/pull/2490).

Read with the [decision PRD](WEBHOOKS-AGENT-PARALLELISM-MEMORY-1-0-2026-09-10.md)
and the [implementation contract](WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md).
This file records what is **demonstrated**, what is merely written, and what the
next session should not have to rediscover.

**The release commitment is not met yet.** One Jamie holding a chat and a
background run at the same time is proven at the admission layer only — in a Go
test, against real SQLite, with no runtime. T06 (a real Claude adapter, two live
runtimes, overlapping in wall-clock time) has not been attempted.

## 0. How to read this file

Three levels of claim, kept apart on purpose, because collapsing them is how a
handoff starts lying:

- **Store mechanism implemented and tested.** The operation exists, has tests,
  and several have been mutation-checked. It says nothing about whether anything
  in production calls it.
- **Production path wired.** A real request or a real dispatcher goes through it.
- **End-to-end guarantee proven.** The behaviour the PRD promises has been
  observed through the whole path, with real runtimes where the PRD says real.

Level three itself has three layers, and a claim has to name the one it was
observed at — **mock**, **real process**, or **real CLI**. §4a has the table and
what each can and cannot show.

The webhook path is now proven end to end at the **real-process** layer: the
production route table, a real HTTP server, real IPC back into it, the real
dispatcher assembly, a real SIGKILL and a real orphaned OS process. Everything
else is at level one or two. **Nothing is proven with a real CLI**, and the
release commitment — one Jamie holding a chat and a background run at once — is
not met.

## 1. What is demonstrated

| Commit | What it establishes |
|---|---|
| `64c1c37` | River over SQLite measured and **rejected**; the FULL and cancellation numbers that bind the default path |
| `b6fc820` | `internal/work` — the dispatch ledger, state machine, lease/recovery and the generation fence |
| `77e676f` | `internal/webhook/profiles` — `github` and `standard-webhooks` signature verification |
| `94826c3` | `internal/memory/memdiff` — the deterministic line diff the removals declaration needs |
| `adb2184` | `synchronous=FULL` by default, with `WithSynchronous`/`WithBusyTimeout` as explicit opt-outs |
| `d44a837` | an exhausted item no longer stalls the whole queue (found by self-review) |
| `fdab316` | the acceptance budget guard: a handle whose `busy_timeout` sits under its budget |
| `a6cceaf` | `needs_reconciliation` holds capacity; a blocked prefix stops hiding claimable work; §6 aging and round-robin |
| `4afab60` | the baseline re-measured under the daemon's managed WAL — FULL costs ~3× what the first run said |
| `1b78aa8` | the delivery ledger, and a crash harness that SIGKILLs a real child mid-acceptance |
| `4b4aedc` | **E0 mechanical half** — per-run tmux/args/env/script/FIFO/exit, run-scoped cancel and attach, and the credential file unlinked the instant it is read |
| `4433755` | **the memory mutation contract** and the profile that refuses to fake a guarantee |
| `b135692` | §6 retention and ingress capacity |
| `85798ea` | webhook handlers accept durably, then dispatch — W2, W3, W4, W6, W8, W9 closed |
| `6659886` | work-items and webhook-deliveries API + CLI, with a cancel that reports `requested` rather than claiming a stop it did not make |
| `3af1176` | §10 metrics and journal events, with an allow-listed label set |
| `a2282dc` | the work UI, and three corrected sentences in the routine webhooks tab |
| `79067a2` | **E0 second half** — per-run HOME, output, secrets and sidecar identity; the memory mutation endpoint; backup classification for the new tables |
| `6c31d0c` | a detached run keeps its refresh reachability |
| `fb193af` | the login refresher had no address after the HOME split |

### Stage A is closed

The River ADR is [ADR-QUEUE-RIVER-SQLITE-2026-09-10.md](ADR-QUEUE-RIVER-SQLITE-2026-09-10.md);
raw numbers in [reports/spike-river-sqlite-results.json](reports/spike-river-sqlite-results.json);
harness in [`tools/spike-river/`](../../tools/spike-river/) as its own Go module, so
River never entered the main `go.mod`.

Verdict **reject**, on two measured failures rather than an inconclusive result:

- River's completion statement is `WHERE id = ? AND state = 'running'` with no
  attempt term. Claim → lease loss → rescue → re-claim, and the superseded
  attempt's completion is **accepted**, marking the row `completed` while
  attempt 2 is live. That is invariant I5, so the fencing would be ours to write
  regardless.
- Its driver recommends `SetMaxOpenConns(1)`. Measured: a second connection
  taken inside an open transaction starves at pool 1 (3001 ms to a deadline) and
  is instant at pool 2 and 5. Ours is 5 and is load-bearing.

These numbers bind the default path too, because they are SQLite's properties
and not the library's.

Re-measured under the daemon's managed-WAL configuration
(`wal_autocheckpoint(0)` + a 2 s checkpointer), which the first run of the spike
did not reproduce. FULL costs about **three times** what that first run reported.

| Measurement (managed WAL) | Value | Consequence |
|---|---|---|
| `synchronous=FULL`, bare insert, p50 / p95 | 21 µs → **20844 / 47418 µs** | ~1000× the NORMAL cost |
| `synchronous=FULL`, acceptance transaction, p50 / p95 / p99 | **21488 / 55770 / 95279 µs** | Affordable against a 500 ms budget, with an order of magnitude of headroom; ceiling ≈**42 commits/s** on the single writer, not the ~150/s first reported |
| Uncontended acceptance, pool 5 | p50 22 ms, p95 52 ms, p99 65 ms | Inside §10's p95 ≤ 500 ms / p99 ≤ 2 s |
| 500 ms context vs a lock held 5 s | returned after **5044 ms** | **A context deadline does not bound an acceptance request.** Unchanged by the WAL configuration. |
| The same, through the acceptance guard | refused after **452 ms** | The guard is what bounds it, and it is now in `internal/work`. |

That last one is the important one and it is easy to forget: `modernc.org/sqlite`
passes `context.Background()` into `Commit` and `Rollback`
(`tx.go:36,50,58`), so only statement execution is interruptible. §5's "answer
within 2 s of the full body" cannot be implemented by wrapping the handler in a
context. It needs a handle whose `busy_timeout` is shorter than the budget, or
an admission guard in front of the transaction. No false `202` was produced
either way — the cancelled operation left no row.

### Level 1 — store mechanisms, implemented and tested

Migration `20260910200255_durable_work_ledger.sql`: `work_items`,
`work_attempts`, `work_events`, `webhook_deliveries`, `external_operations`,
`session_mailbox`.

Identity, settled so dependent work can build on it:

- **`work_id`** — one accepted unit of work, stable across every retry. A manual
  replay of terminal work mints a NEW `work_id` carrying `replay_of`.
- **`run_id`** — one attempt. Deliberately the **same namespace as
  `agent_runs.id`**, so the journal's existing `run_id` index keeps meaning one
  thing. Do not mint a fourth identifier.
- **`session_id`** — the conversation. One active turn.
- **`generation`** — bumped on every claim. The fencing term.

Proven by test against real migrated SQLite (`internal/work/work_test.go`):

- a stale attempt cannot complete the work, and cannot renew its lease
- an expired lease **with** a runtime locator → `needs_reconciliation`, and the
  work stays unclaimable while unresolved; **without** one → requeued
- `needs_reconciliation` **holds** an execution slot and its session, so neither
  the same agent nor the same session can start while a runtime may still be
  alive under an unverified locator
- a prefix of 200 blocked items cannot hide claimable work behind it
- §6's aging and round-robin across workspaces and agents
- one chat + one background for the same agent are live simultaneously; a third
  waits `queued`
- 16 concurrent dispatchers claim exactly the background cap, under `-race`
- background cannot borrow the chat reservation
- retry exhaustion fails instead of looping; a deadline expires before start,
  and work without one never expires

Three tests here were **mutation-checked**, and two of them needed rewriting
because the first version passed with the guard removed. That is the habit worth
keeping, not the individual results:

- the fencing test would have passed without the generation term, because the
  state machine refused the stale write for an unrelated reason. It now puts the
  live attempt into `running` first, so only the fence can refuse.
- the blocked-prefix test would have passed without the SQL-side filtering,
  because the fairness ordering floats a free agent's work to the front on its
  own. Its blockers are now blocked by their *session*, carry no agent, and share
  one workspace — every ordering key equal, acceptance order deciding.
- removing `needs_reconciliation` from the capacity set makes the reconciliation
  test fail with `claim B while A is unreconciled = <nil>`.

### Level 1 — durability

`database.Open` now defaults to `synchronous=FULL`, with `WithSynchronous` and
`WithBusyTimeout` as explicit opt-outs. Tests assert the pragma reaches **every
pooled connection**, not just the first — it is per-connection state and setting
it anywhere but the DSN would make durability depend on which connection a
request drew. A source-level assertion in `cmd/crewship` fails the build if the
daemon ever passes `WithSynchronous`.

`internal/testutil`'s migrated template opts **down** to NORMAL, explicitly and
with the reasoning in the source. Measured: FULL costs ~2% on a read-heavy
package and **3.1×** on a write-heavy one (`internal/work`: 4.3 s → 13.4 s), and
the CI per-package cap is 12 minutes. No unit test survives an OS crash either
way, and T14's evidence needs a separate harness by design.

## 2. Invariant status

| | Status |
|---|---|
| **I1** no `202` before a durable commit | **Demonstrated through the real route** — `AcceptDeliveryTx` writes delivery and work in one transaction, a SIGKILL'd child proves the after-commit and before-commit cases, and a real HTTP POST at the production webhook route answers only after the commit (`TestVerticalServer_OneDeliveryTravelsTheWholePath`) |
| **I2** one delivery → at most one work item | **Demonstrated** — duplicates, concurrent duplicates, content-key replay under a fresh id, and same-id-different-body as a conflict |
| **I3** one active turn per session; atomic claim | **Demonstrated**, including that an unreconciled turn keeps the session |
| **I4** state/memory/sidecar changes verify the live run | **State**: demonstrated. **Memory**: the host endpoint verifies run and generation properly (mutation-checked) — but nothing supplies a run id, so no production write is verified yet. **Sidecar**: per-run `agtv2` tokens verified against a crew-scoped key, with a run registry that refuses an ended run |
| **I5** an old attempt cannot overwrite a newer one | **Demonstrated**, mutation-checked |
| **I6** memory writes are not lost under concurrency | **Demonstrated for the host path** — 100 concurrent replaces from one revision yield exactly one winner, and crash recovery never overwrites a third party's content. NOT reachable from the two agent-facing paths (see below) |
| **I7** no producer bypasses admission | **Not met, one producer down.** The agent webhook now goes through `Claim` with declared sources and serial limits; six pumps and two unguarded producers remain |
| **I8** rights checked at acceptance and at dispatch | **Demonstrated for the webhook producer** — `WebhookAuthorizer` is mandatory in `StartWebhookDispatcher` and re-checks agent, deletion, workspace and crew at claim time; an agent deleted while the work waited is refused at dispatch, not at acceptance (`TestVerticalServer_PermissionRemovedWhileWaitingPreventsTheStart`). Not implemented for the other producers, which is part of I7 |

## 3. T01–T14

Nothing is PASS. Saying otherwise would be the exact error the PRD warns about.

| | State |
|---|---|
| T01 signatures, rotation, timestamp edges | Signature half covered, including both ±5 min boundaries and rotation, and a real signed POST now travels the production route. HTTP status mapping and oversized/chunked bodies are in flight |
| T02 duplicates under concurrency | **Covered at the store level** — 24 concurrent duplicates collapse to one work item and one receipt — **and through the real route**: a re-delivery yields one work item, one attempt, one runtime and one run record (`TestVerticalServer_ADuplicateDeliveryDoesNotCreateASecondRun`). The concurrent case still has no HTTP-level test |
| T03 crash before/after commit | **Harness written and passing.** A real child SIGKILLs itself mid-acceptance; after the commit the work survives and a resend finds it, before the commit nothing survives. It does NOT cover OS crash — that is T14 |
| T04 held write lock, checkpoint, FULL everywhere | Measured, and now enforced: the acceptance guard refuses after 452 ms with a 600 ms budget against a lock held 6 s. `FULL` on every pooled connection is asserted |
| T05 all producers at once, mailbox | Mailbox table exists, unused. Not started — and blocked on I7 |
| T06 real Claude, chat + background overlapping | **Not attempted.** Admission-level only, and the parallel flag stays off until it is |
| T07 start/cleanup/cancel B during A | **Eight tests**, all passing: start B leaves A untouched, credentials are not overwritten across runs, cleanup of B leaves A's directories alone, memory stays shared and survives cleanup, cancelling B does not name A, the run-end notification is scoped and secret-safe, and a login refresh reaches every live run. On generated command strings and the provider fake — **no live container** |
| T08 lost heartbeat, late completion, restart | **Fencing, lease loss and recovery demonstrated and mutation-checked**, and the restart half is now done at the real-process layer: a SIGKILL'd dispatcher's orphaned runtime is found by a second process and no second runtime is created (`TestChildCrash_...`) |
| T09 external success without a receipt | Table exists, unused |
| T10 memory CAS, append retry, cap race | **Demonstrated host-side**: 100 concurrent replaces yield one winner; 100 identical append retries yield one increment; the cap race still holds. Unreachable from the agent paths — see §5a |
| T11 memory crash recovery | **Demonstrated** at five crash points, including that a third party's content is never overwritten |
| T12 UI reconnect, two streams | In flight |
| T13 retention, disk full | Retention and ingress limits implemented and mutation-checked; nothing schedules the sweeper and no handler calls the ingress check yet |
| T14 OS/VM crash | Not started, and deliberately cannot be a `kill -9` — T03's SIGKILL harness is explicitly not this |

## 4. Corrections to the PRD documents

Found by auditing the four branches against this checkout. Each is cited; none
is inference. **Fix the documents or carry these forward — do not let the next
session rediscover them.**

1. **"The existing unique active assignment per session" does not exist.**
   (Contract §7, and the annex's §1.) There is no such index and no such table.
   Every `CREATE UNIQUE INDEX` in `internal/database/` was enumerated; none
   touches `assignments`, `chats` or a chat session. What actually holds today is
   an in-memory, non-persistent `AgentRunLock` plus a `NOT EXISTS` predicate in
   `internal/api/assignments_queue.go:277-281` — whose own comments admit the two
   cannot be taken atomically together. Any plan treating this as an existing
   invariant to preserve is building on nothing.

2. **W5 is partly stale.** "The store has a workspace-level key" stopped being
   true at migration v160: the primary key is
   `(workspace_id, pipeline_id, idempotency_key)`. The real defect is the missing
   **endpoint** dimension — and, worse than W5 says, the agent path passes a
   constant `pipeline_id = "webhook"` (`internal/api/webhook.go:45`), so a
   sender-supplied `Idempotency-Key` collapses every agent in a workspace into
   one namespace.

3. **The DSN description is incomplete.** The server always opens with
   `WithManagedWAL()` (`cmd_start.go:197`), i.e. `wal_autocheckpoint(0)` plus a
   dedicated checkpointer goroutine. RESOLVED: the harness now mirrors both and
   the ADR carries the corrected numbers. It mattered — FULL costs ~3× more under
   the production shape, because the WAL is allowed to grow between ticks so each
   fsync flushes more, and the checkpointer competes for the single writer.

4. **§5's "oversized body is refused with 413" describes no current surface.**
   Two paths truncate silently via `io.LimitReader`; the third
   (`internal/api/pages_data.go:105`) detects overflow correctly and answers
   **422** deliberately, with the reasoning written out and consumed by the CLI's
   `ExitValidation`. Changing it to 413 is a CLI contract change, not a bug fix.

5. **§5's "verify the busy wait is interrupted" is not an open question — it is a
   known defect**, now measured. See §1.

6. **M1 understates the sidecar.** `MemoryWriteRequest` has **no `mode` field at
   all**, so every sidecar memory write is an unconditional whole-file overwrite
   — not "replace lacks an expected revision".

7. **M3 understates the bypass.** The annex frames it as a hostile shell.
   `internal/memory/audit_watcher.go:54-58` documents it as the **normal** path:
   ~75% of write events bypass the sidecar via the agent's own Write/Edit tools.
   The entire audit-watcher subsystem exists because of it.

8. **§8's read-after-write requirement is already satisfied.** `memory.read` and
   the sidecar's `/memory/read` both read the canonical file, not the index; only
   `memory.search` is index-served. The real projection gap is `memory_versions`.

9. **Three things in §8 cannot all be true**, each now pinned by a test in
   `memdiff`: (a) append-through-replace cannot carry empty removals when the base
   has no trailing newline, because appending rewrites the last line; (b) CRLF
   normalization cannot be idempotent — `a\r\r\n` → `a\r\n`, and a second pass
   eats the content CR; (c) Myers is quadratic and §8 caps nothing (4000 unrelated
   lines ≈ 239 ms / 265 MiB), so the caller must bound input size before taking
   the mutation lock.

10. **Two outbox patterns already ship** — `notification_deliveries` (with
    `attempts` and a recovery loop) and `workspace_conversation_outbox`. Both lack
    a per-row claim and lean entirely on the leader lease, which
    `internal/notifyroute/recovery.go:190-193` documents. Extend that pattern
    rather than inventing a third shape.

11. **`assignments` is stronger than the annex suggests** — `lease_owner`,
    `lease_expires_at`, a 20 s heartbeat, a 15 s owner-aware sweeper, a
    stuck-RUNNING sweeper and a boot recovery pass. The gaps are (a) no
    `attempts`/`eligible_at` and (b) boot recovery **fails** orphans rather than
    re-queueing them (`assignments_running_recovery.go:197`).

12. **A code comment E0 must not trust.** `orchestrator_run.go:660-662` claims
    cancelling `execCtx` "kills the CLI process". Under the tmux path it does not
    — the session is created detached and `orchestrator_run.go:818-824` exists to
    handle the survivor. Every ctx-based cancel in the system inherits this.
    Likewise `issue_handler_hard_stop.go:250-253` claims it never touches another
    run's session, which the slug-only session name makes false.

## 4a. Level 2 and 3: what is actually wired, and what is proven

**Wired into a production path:** webhook acceptance (both surfaces), **the
webhook dispatcher** (`Router.StartWebhookDispatcher`, started by
`cmd/crewship/cmd_start.go` before the scheduler and stopped with it), the work
and delivery read API and its CLI, the work UI, the metrics collectors, the
memory host mutation endpoint, the sidecar's revocation journal and its
run-status authority, per-run runtime identity in the orchestrator.

**Implemented, tested, and called by NOTHING in production yet:**
`RetentionPolicy.Sweep`, the ingress capacity check on the producers that have
not moved onto shared admission yet (§5's six pumps), and the durable chat
mailbox. `Claim`, `MarkStarting`, `StartRunning`, `Heartbeat`, `Transition`,
`RecoverExpiredLeases` and `RequestCancel`'s live-runtime half are no longer on
this list — the webhook dispatcher uses all of them on the shipped path.

### The three layers a claim can rest on

Every guarantee below names the layer it was observed at. They are not
interchangeable and the difference is not pedantry: each one can be green while
the next is broken, and that is exactly how this programme has failed before.

| Layer | Where | What it can show | What it cannot |
|---|---|---|---|
| **mock** | `internal/dispatch/integration_test.go` | the loop's logic against a Runtime the test can crash, block, silence or make unstoppable at any instruction | nothing about the real assembly: acceptance, routes, the resolver and the ledger's real callers are all absent |
| **real process** | `internal/dispatch/child_crash_test.go`, `internal/work/crash_test.go`, `internal/api/webhook_vertical_test.go` | the wiring, the real HTTP route table, real IPC back into the same server, a real SIGKILL and a real orphaned OS process that a restart has to find | nothing about a real CLI adapter: the agent process is `sleep`, or a fake `agentRunner` |
| **real CLI** | not yet run | T06/T07 — a real Claude adapter, two live runtimes overlapping in wall clock | — |

The end-to-end pass in `internal/api/webhook_vertical_test.go` substitutes
exactly one thing: the `agentRunner` interface (start a process, stop it, is it
there) and the container provider beneath it. Everything above that line is the
code that ships — `api.NewRouter` registers the route, a real `http.Server`
serves it, `chatbridge.IPCResolver` makes real HTTP calls back into the same
process's internal API, acceptance takes the real transaction, and
`StartWebhookDispatcher` builds the real dispatcher with the mandatory
`WebhookAuthorizer` and `work.SerialAgentLimits()`.

### Proven end to end at the real-process layer

These ran, with results, on 2026-09-11. Each is a test, not a description.

| Guarantee | Test |
|---|---|
| a valid delivery reaches a terminal state through claim → attempt → runtime, and the receipt, the attempt, the run record and the locator all carry ONE run id | `TestVerticalServer_OneDeliveryTravelsTheWholePath` |
| a SILENT runtime is still confirmed — the confirmation is the provider's answer, not a stream event | same test (`runtime_phase = confirmed` with no event emitted) |
| a lost or nil hint costs latency and nothing else; the poll finds the work | `TestVerticalServer_ALostHintIsReplacedByPolling` |
| a duplicate delivery produces one work item, one attempt, one runtime and one run record, and answers with the work's CURRENT state | `TestVerticalServer_ADuplicateDeliveryDoesNotCreateASecondRun` |
| this dispatcher claims webhook work and nothing else — another producer's item is left `queued` | `TestVerticalServer_TheDispatcherDoesNotClaimAnotherProducersWork` |
| a server with no webhook route starts no dispatcher (`ErrNoWebhookRoute`) rather than one with no authorizer | `TestVerticalServer_NoRouteMeansNoDispatcher` |
| cancel before any start prevents execution: no runtime, no run record | `TestVerticalServer_CancelBeforeAnyStartPreventsExecution` |
| cancel during a run stops THAT run's runtime, named by its own locator | `TestVerticalServer_CancelDuringARunStopsThatRuntime` |
| a stop that does not take is **not** reported as cancelled — it parks, naming the runtime | `TestVerticalServer_AStopThatDoesNotTakeIsNotCalledCancelled` (waits out the shipped 10s grace on purpose) |
| after shutdown no work is left claiming a runtime nobody supervises; the stop function does not return until that is true | `TestVerticalServer_ShutdownLeavesNoSupervisorOutsideTheLedger` |
| a permission removed while the work waited refuses it at dispatch, not at acceptance | `TestVerticalServer_PermissionRemovedWhileWaitingPreventsTheStart` |
| acceptance writes the input the dispatcher reads — a contract between two halves with no compiler check | `TestVerticalServer_AcceptanceWritesTheInputTheRuntimeReads` |
| an unclear run-record write is resolved by looking the record up by its stable id: only a confirmed absence is retryable, present or unreadable is unclear (mutation-checked) | `TestRunRecordAbsent_AnswersTheQuestionItIsAskedAboutTheRealRecord`, `TestWebhookRun_AnUnclearRunRecordWriteIsNotRetried` |
| a dispatcher killed by SIGKILL after its runtime exists and before confirmation: a restart over the same database starts **no second runtime**, and the orphan is findable at the recorded locator | `TestChildCrash_ARestartFindsTheOrphanedRuntimeAndStartsNoSecondOne` |

### R4 is closed at the real-process layer

An earlier version of this file said "protocol ready, not wired, not proven",
and then that only tests called `MarkStarting`. Both are now out of date.

The production dispatcher writes the start intent before creating the runtime,
and `TestVerticalServer_OneDeliveryTravelsTheWholePath` reads `runtime_phase`
from the database at the instant the runtime is created — it is `starting`, with
a locator, before anything exists to find.

`TestChildCrash_ARestartFindsTheOrphanedRuntimeAndStartsNoSecondOne` is the
crash that R4 exists for, at the real-process layer: a child process runs a real
dispatcher, creates a real OS process, publishes its identity, and SIGKILLs
itself before any confirmation. The runtime is reparented and survives. A second
dispatcher then starts over the same database file with no memory of the first,
and the work lands in `needs_reconciliation` with the orphan still resolvable at
its locator. Mutation-checked: removing the phase test in
`RecoverExpiredLeases` makes the restart re-run the work, and the test goes red
with the work `running` beside a live orphan.

What it still does **not** prove is durability against an OS crash or a power
cut. SIGKILL ends a process; the kernel keeps and eventually writes its page
cache. That is T14 and it needs a storage-fault harness.

### The timeline of one delivery

Printed from the ledger by `TestVerticalServer_TheTimelineOfOneDelivery` (run it
with `-v`), so this cannot drift from the system:

```
identities
  delivery_id  cmtwv54aq00010b73dde2   (workspace, endpoint, source delivery id) — the sender's identity
  work_id      cmtwv54aq0002acc811a7   — stable across every retry; what the receipt named
  run_id       cmtwv54dd0003d0815c24   — ONE attempt; the same value in work_attempts, the run record, the locator
  session_id   webhook-<agent>-<accepted-run-id>   — one active turn
  generation   1                       — the fencing term; every write must present it
  locator      agent-run:cmtwv54dd0003d0815c24   — written BEFORE the runtime existed
  attempt      1 of 5
  source/class webhook / background    filter: accepted

states
  11:18:42.434   -          -> queued     gen=0 run=-                      accepted
  11:18:42.528   queued     -> starting   gen=1 run=cmtwv54dd0003d0815c24  claimed
  11:18:43.530   starting   -> running    gen=1 run=cmtwv54dd0003d0815c24  runtime confirmed
  11:18:43.558   running    -> succeeded  gen=1 run=cmtwv54dd0003d0815c24  completed
```

The generation is 0 at acceptance and 1 from the claim onwards: the fence starts
when an attempt does. The second attempt of the same work would be generation 2
with a different run id and the same work id — which is the whole reason the
three identities are separate.

### Three bugs the vertical pass found that every unit layer was green on

Worth recording, because they are the argument for the pass itself. All three
were invisible to tests that covered each half separately.

1. **Acceptance wrote an input the dispatcher could not read.** `input_json` was
   an ad-hoc map of `event`/`source`/`chat_id`; `webhookRunInput` expects
   `agent_id`/`crew_id`/`payload`. The dispatcher resolved an empty agent id.
   Fixed by typing the write as `webhookRunInput` and storing the payload (the
   retention sweep may drop a raw body, and work that cannot run without a row
   retention may delete is not durable). Regression test:
   `TestVerticalServer_AcceptanceWritesTheInputTheRuntimeReads`.
2. **`runRecordAbsent` queried a table that no longer exists.** The
   unclear-write rule — look the record up by its stable id, and only a
   confirmed absence is retryable — was asking `agent_runs`, removed by
   unified-journal phase J. Every lookup errored, so every unclear run-record
   write said "could not be established". It now reads the `run.started`
   journal entry traced by the run id, which is what the record actually is.
3. **A run that finished before the confirmation probe polled could not record
   its own success.** `starting -> succeeded` was not in the state machine, so a
   short run sat in `starting` holding a slot until its lease expired 60 seconds
   later, and recovery then described a SUCCESS as an abandoned run needing
   reconciliation. With a one-second production poll interval this is the common
   case for anything quick, not an edge. Two fixes, both mutation-checked: the
   missing edge (`TestTransition_ASuccessBeforeConfirmationIsStillRecordable`,
   `TestVertical_AShortRunSettlesEvenIfTheProbeNeverPolled`), and a backstop so
   that an outcome the ledger refuses is parked immediately instead of logged and
   dropped (`TestVertical_AnUnwritableOutcomeIsParkedRatherThanLost`).

### What the parallel profile still rests on

`StartWebhookDispatcher` uses `work.SerialAgentLimits()` — one run per agent, of
either class — for **every** adapter. The parallel profile is off, and it stays
off until T06/T07 run against a real Claude runtime. A dispatcher that quietly
allowed two concurrent runs would be enabling that profile by omission.

**Proven end to end with a real CLI: nothing.** No T01–T14 is a PASS.

## 5. The six pumps I7 has to collapse

Each can start agent work independently today:

| | Started at |
|---|---|
| assignment pump `pumpAndDispatch` | `internal/api/assignments_dispatch_pump.go:222`, edge-triggered from 5 sites |
| routine scheduler | `cmd_start.go:1138`, leader-gated |
| pending-run dispatcher | `cmd_start.go:1147` |
| agent cron (robfig) | `cmd_start.go:590`, leader-gated |
| recurring-issue dispatcher | `cmd_start.go:1159` |
| automation registry → `pending_runs` | `cmd_start.go:502` |

And three producers take **no** shared lock at all: the agent webhook
(`internal/api/webhook.go:535`, which explicitly permits 8 concurrent runs of one
agent), the direct agent-run IPC route (`internal/server/routes_agent.go:167`,
which also mints no run id and writes no `agent_runs` row), and peer query
(`internal/api/query_handler.go:400`).

`cmd/crewship/cmd_start.go` is where every one of them is wired. **Every branch
of this programme touches that file** — coordinate before editing it.

## 5a. The gap that matters most right now

**The guaranteed memory profile is reachable, and nothing supplies the run id
it needs.**

`POST /api/v1/internal/memory/mutation` performs the write host-side under
`ProfileGuaranteed` against the real ledger, and `Authorize` genuinely verifies:
the run exists, the item belongs to this workspace and to the acting agent
resolved from the token rather than the body, the attempt has not ended, the
item is live, and the attempt's generation matches both the item's and the
request's. Replacing that closure with `return nil` turns six subtests green
that should be red.

What is missing is upstream. No dispatch path threads a run id to the agent, so
every real write still degrades — with its reason stated, `revision_checked:
false`, and `degraded: true`. An environment variable is the obvious shortcut
and is wrong: a run id is per attempt and a container outlives many attempts, so
it would carry a stale run for every attempt after the first, which is exactly
what the fencing term exists to catch.

Two smaller limits, both stated where they bite: a guaranteed FIRST replace on a
key is impossible by contract (revision 0 is what an unanchored key has, and the
profile requires `ExpectedRevision > 0`), so the first write to a key must be an
append; and the MCP `memory.write` tool stays legacy because its path
resolution, injection screen and quarantine live in `internal/memory`, which the
container cannot give a database handle.

### The older framing, kept because the shape still matters

`memory.Mutate` has two profiles. `ProfileGuaranteed` refuses a write missing a
caller-supplied operation id, a durable ledger handle, `ExpectedRevision` with
non-nil `Removals`, or a wired `Authorize`. It does not downgrade — a write that
cannot be checked fails, because one that succeeds unchecked and is then
described as revision-checked is worse.

But both agent-facing writers run INSIDE the agent container and have no
`*sql.DB`: the MCP `memory.write` tool (`internal/memory/tools.go`, constructed
at `internal/sidecar/memory_mcp.go:308`) and the sidecar's `POST /memory/write`.
`grep 'sql.DB' internal/sidecar/*.go` returns nothing. Both therefore declare
`ProfileLegacy` out loud and every response carries `revision_checked: false`.

Closing it needs a host mutation endpoint for the sidecar to call, and an
`Authorize` closure that genuinely verifies the run's generation against the
work ledger. That work is in flight. Until it lands, **no agent memory write is
revision-checked**, and the parallel profile must not be enabled.

Two compatibility choices are legacy-only and deliberate: a missing operation id
is synthesised rather than refusing every `memory.write` the model has issued
since the tool shipped, and `Removals == nil` means "undeclared" rather than
"removes nothing", because the strict reading fails every existing whole-file
replace. Both are recorded as unverified rather than presented as checked.

## 6. The next concrete step

In the order the 2026-09-11 review sets, which is also dependency order.

1. ~~**R3 — one dispatcher that actually runs the work.**~~ Done and wired:
   `internal/dispatch` owns execution, the direct path from acceptance into
   `orchestrator.RunAgent` is gone (guarded at source level by
   `TestWebhookAcceptanceCannotStartAnAgent`), and the dispatcher is started by
   the server. Proven at the real-process layer — see §4a.
2. ~~**Prove the crash R4 exists for.**~~ Done at the real-process layer:
   `TestChildCrash_ARestartFindsTheOrphanedRuntimeAndStartsNoSecondOne`. Still
   not proven against an OS crash or a power cut, which is T14.
3. **R6 — per-attempt identity through to the memory clients**, and a parallel
   profile that can never fall back to a legacy write. The run id must travel
   per attempt; a container-boot environment variable is specifically wrong,
   because a container outlives many attempts.
4. **R8 — ingress and dedup under overload.** A throttled same-id/different-body
   delivery must be 409, not a duplicate; a lookup failure is 503, not a capacity
   claim; a busy agent with free ingress capacity is 202 queued, because §6
   separates "the queue is full" from "this agent is busy".
5. **Real T06/T07 and an integration cancel**, over a live runtime. Only then may
   the parallel profile be enabled.

## 6a. The rest of the release gates, which do not disappear

R3/R6/R7/R8 are not the remainder of the PRD. Still outstanding, and still
required by it:

- **Durable chat mailbox wired** (§7). The table exists and nothing uses it, so a
  busy agent still bounces a message before persisting it — finding P1, unfixed.
- **Dispatch-time permission and budget re-checks** (I8). Columns exist; nothing
  re-checks at dispatch, so a right removed while work waited still runs.
- **External operations with an unclear result** (§4, T09). The table exists and
  nothing writes to it: no intent before a call, no receipt after, no
  reconciliation path.
- **Scheduling recovery and retention.** `RecoverExpiredLeases` and
  `RetentionPolicy.Sweep` are implemented and nothing runs them on a timer.
- **The remaining producers onto shared admission** (I7): the agent webhook, the
  direct IPC start route and peer query still start runs with no shared lock.
- **UI reconnect and stream separation under a real server** (T12) — built
  against mocks only.
- **Load and soak** (§10): acceptance p95/p99 at 10 req/s for 60 minutes, the
  1000-request burst, the capacity invariant under 50 producers.
- **T14**: OS-crash or storage-fault evidence, explicitly not a `kill -9`.
- **Upgrade, drain and rollback drill** (§12), and the adapter capability matrix
  that may only list adapters actually verified.

## 7. Practical notes for the next session

- The pre-commit hook's `golangci-lint` step is shared across sessions. "parallel
  golangci-lint is running" is another session, not your code — retry in a loop.
- The hook now skips directories belonging to a nested module. Before that fix, a
  nested `go.mod` blocked every commit with a message naming nothing.
- The skip budget (`scripts/skip-budget.txt`) is at its 141 baseline. Inside an
  `f.Fuzz` body, reject uninteresting input with `return`, not `t.Skip`.
- The foreign-key index ratchet
  (`internal/database/migrate_index_hot_foreign_keys_test.go`) will go red if a new
  migration adds an FK whose child column leads no index. It caught two in the work
  ledger; both are now indexed, because retention does hard-delete the parents.
- `internal/database` and `internal/api` both take many minutes. Use `-run`
  filters while iterating, and never conclude "green" from a run that timed out.
- **`internal/api` is close to the CI cap and this branch is not why.** Measured
  724.6 s on crewship-dev while seven agents were competing for the box, against
  CI's 12-minute per-package `-timeout`. Everything this programme added to that
  package — the work API, the delivery API, the acceptance tests, the memory
  mutation endpoint, the credential path tests — totals **~15 s**. The other
  ~710 s predates it. So a timeout there is a load or hardware signal, not a
  reason to go deleting new coverage; the same suite measured 543 s and 613 s on
  a quieter box earlier the same day. If it does start failing in CI, the lever
  is the package's existing bulk, not this branch's tests.
- **`scripts/docs-inventory -strict` is red on this branch and not because of
  this work.** Four environment variables have no documentation row:
  `CREWSHIP_MEMORY_REQUIRE_GUARANTEED`, `CREWSHIP_RUNEND_AUTH`,
  `CREWSHIP_SIDECAR_RUN_AUTHORITY`, `CREWSHIP_SIDECAR_STATE_DIR`. All four come
  from the sidecar and memory work already committed here, none appears in this
  session's diff, and documenting them means describing someone else's feature —
  so they are named here rather than guessed at. They must be documented before
  the branch merges.
- The vertical pass adds ~35 s to `internal/api`, most of it one test that waits
  out the shipped 10 s cancel grace on purpose. If that package's runtime becomes
  the problem, that test is the first candidate for a build tag — but shortening
  the grace would make it test a value no deployment uses.
- Published Standard Webhooks test vectors trip gitleaks. `.gitleaks.toml` has a
  narrow entry for that file, and the "these are public vectors" claim is enforced
  by a test that verifies each published signature against its secret.
