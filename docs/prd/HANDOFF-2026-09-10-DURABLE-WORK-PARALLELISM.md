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

### A blocker the review found, and what it cost

`webhook` is not a work type. Two producers write it:

| Producer | Source | Domain kind | Agent id | Executed by |
|---|---|---|---|---|
| agent webhook (`webhook.go`) | `webhook` | `agent_run` | set | the dispatcher in `internal/dispatch` |
| routine webhook (`pipeline_webhooks.go`) | `webhook` | `pipeline_run` | **empty** | the pipeline engine |

The first version of `StartWebhookDispatcher` declared `Sources:
[]work.Source{work.SourceWebhook}` and therefore claimed both. Refusal was not
the harm: **taking** was. By the time the authorizer could object, the claim had
already bumped the generation, opened an attempt row and moved the item out of
`queued`, and none of that is undoable. The authorizer would then have refused
routine work as "the work names no agent" — so a routine trigger would have died
as `failed`, with a reason about agents it never had, and the pipeline engine
would never have seen it again.

The fix is in the atomic candidate selection, not after the claim: `ClaimOptions`
now takes `Kinds []work.Kind` — (source, domain kind) PAIRS — filtered in the
same SQL statement that selects candidates, inside the same transaction that
takes the item. Two independent lists would not have been enough either: they
match the cross product, so `{webhook, chat} × {agent_run, pipeline_run}` would
admit pairs nobody declared. An empty `DomainKind` is a value and not a wildcard.

Two more consequences, both deliberate:

- **A dispatcher that declares nothing refuses to start** (`Run` returns an
  error). There is no safe default: only the caller knows what its Runtime can
  run, and an undeclared filter claims everything.
- The regression test drives **both real acceptance routes** over HTTP into one
  database with the dispatcher running throughout, and asserts the routine work
  is untouched in four ways — state, generation, attempt count, and the absence
  of any attempt row. State alone would not do: a claim followed by a requeue
  lands back on `queued` having still spent an attempt and moved the fence.
  Mutation-checked: reverting to a source-only filter reproduces exactly the
  reported bug (`failed`, generation 1, one attempt burned).

### I8, stated precisely

The earlier version of this section rested on one test of a deleted agent and a
comment that claimed more than the code did — it mentioned a disabled agent and
an exhausted budget, and `WebhookAuthorizer` checked neither.

**Checked at dispatch, and tested by changing the answer between acceptance and
dispatch** (`TestVerticalServer_WhatIsRecheckedAtDispatchAndWhatIsNot`):

| Condition | Answer | Why that answer |
|---|---|---|
| the agent no longer exists | refuse | nothing brings it back |
| the agent was deleted while the work waited | refuse | same |
| the agent now belongs to a different workspace | refuse | the delivery's authorization belonged to the workspace it arrived in |
| the agent's crew was deleted | refuse | same |
| the agent is `PENDING_REVIEW` | **defer** | it is held until an operator approves, and approval is the expected outcome |
| any other `agents.status` (IDLE, RUNNING, ERROR) | allow | these are lifecycle states, not decisions — refusing on RUNNING would mean an agent could never be given a second task, and refusing on ERROR would let one failed run brick it |

**Not checked here, and where each one actually lives:**

| Concern | Enforced |
|---|---|
| concurrency admission (server, agent, class) | `work.Claim`, inside the transaction that takes the item — it has to be there, or two dispatchers both see a free slot |
| per-agent ingress rate and in-flight cap | acceptance (`agentRateLimit`, the `agentRuns` registry) — their job is to refuse a flood at the door, not to queue it |
| a workspace backup holding the write lock | inside the run (`refuseIfBackupInProgress`), where the lock is held for the duration rather than sampled |
| egress policy | when the run request is built — an externally triggered run is forced to restricted network mode regardless of the crew's setting |
| **spend budget** | **nowhere. There is none in 1.0.** §6's `work.CheckIngressTx` is a queue-depth and byte limit, not money, and nothing calls it on this path. When a spend gate exists it belongs in the authorizer, because it is exactly the kind of answer that changes while work waits. |

There is no agent "disabled" flag in this system; `PENDING_REVIEW` is the one
status that is a decision rather than a state, and `refuseHeldAgent` in
`assignments.go` owns that rule.

### "Not yet" had to become a third answer

A held agent cannot be refused and cannot be allowed. Failing it repeats a
mistake this repository already documents at length: the first version of
`refuseHeldAgent` returned an ordinary error, the mission engine recorded a
terminally FAILED task, and the operator's approval arrived at something that had
given up minutes earlier. Retrying it as an ordinary failure is no better —
`MaxAttempts` of capped backoff is about twenty minutes, and the answer changes
when a person acts, not on that timescale.

So `dispatch.Decision` has three values (`Allow`, `Refuse`, `NotYet`), and
`work.Store.Defer` returns a claimed attempt to the queue **with its budget
given back**, eligible again later. Giving the budget back is safe only under a
condition the store CHECKS rather than assumes: the attempt must still be in
runtime phase `planned`, so nothing can possibly have been created. What is not
given back is the fence: the generation is not rolled back and the deferred
attempt row is closed rather than deleted, so a straggler holding the deferred
run id is still refused.

That exposed a second defect. `work_attempts` is `UNIQUE(work_id, attempt)`, and
the attempt number and the attempt budget were the same column, so the claim
after a deferral reused a number that already had a row: the insert failed with a
constraint error the dispatcher could only log and retry, forever. They are two
quantities now — `work_items.attempts` is the budget, `work_attempts.attempt` is
a monotonic identity — and both are mutation-checked.

**A cancel that lands during the hold (review P1).** The dispatcher checks for
a cancel right after the claim; the authorizer runs after that; and a cancel
arriving in between is recorded on the attempt the deferral is about to close.
The first version of `Defer` closed it and requeued — so the request went to the
grave with the row, the next claim opened a fresh attempt with nothing on it,
and a user who was told "requested" watched the work run once the hold cleared.
The decision is now made **inside the deferral's transaction**: a cancel on a
`planned` attempt is a confirmed cancellation (nothing was created, so there is
nothing to ask to stop) and the work ends `cancelled` there. Any check outside
that transaction is a second race of the same shape. Guarded at three layers,
each mutation-checked: the store (`TestDefer_ACancelRequestedDuringTheHoldIsHonoured`),
the dispatcher with a paused authorizer
(`TestVertical_ACancelDuringAPausedAuthorizationSurvivesTheDeferral`), and the
real cancel route with the production authorizer held open mid-decision
(`TestVerticalServer_CancelOverHTTPSurvivesAHeldAgentsDeferral`). The HTTP test
originally left the ordering to timing and passed with the fix removed —
the cancel had landed on a queued row — so the real authorizer now has an
unexported `beforeDecide` hook the rig can block on, and the test asserts the
cancel outcome was `requested` (live attempt) before letting the deferral run.

What bounds a deferral loop is the item's own `deadline_at`, not an attempt
count. That is the right instrument: "how long may this wait for a human" is a
question about patience, not about retries. Work with no deadline waits
indefinitely, on purpose.

### What the parallel profile still rests on

`StartWebhookDispatcher` uses `work.SerialAgentLimits()` — one run per agent, of
either class — for **every** adapter. The parallel profile is off, and it stays
off until T06/T07 run against a real Claude runtime. A dispatcher that quietly
allowed two concurrent runs would be enabling that profile by omission.

**Proven end to end with a real CLI: nothing.** No T01–T14 is a PASS.

### Test evidence, and what each run actually covered

Runs are listed separately rather than combined, because combining them is how a
green claim gets made that nobody observed.

| Run | Tree | Result |
|---|---|---|
| `internal/api`, full, 1123 s | 163bd2ef **before** the background-guard fix | 20,216 pass, 6 skip, **1 fail** — `TestBackgroundWork_EverySpawnSiteIsAccountedFor`, tripped by the new dispatcher daemon |
| `internal/api -run TestBackgroundWork…\|TestWebhookAcceptance…\|TestWebhookRuntime…`, 0.7 s | with the fix | pass |
| `internal/work`, `internal/dispatch`, `internal/server`, and every other package except `cmd/crewship` | 163bd2ef | pass |
| `cmd/crewship`, `internal/server`, `internal/work`, `internal/dispatch`, `internal/orchestrator`, `internal/sidecar`, full | after the kind-filter, I8 and CLI-YAML work, on a disk with room | pass (383 s / 54 s / 15 s / 15 s / 39 s / 83 s) |
| `internal/api`, full, 1100 s | same tree, disk with room, `API_EXIT=0` | **20,224 pass, 0 fail, 6 skip**, zero `no space left` lines |
| `internal/work`, `internal/dispatch`, full; `internal/api -run 'TestVerticalServer_\|TestWebhook\|TestRunRecordAbsent\|TestBackgroundWork\|TestSecWebhook'` (53 s) | after the P1 cancel-survives-deferral fix | pass. The P1 fix touches `Store.Defer` only; the full `internal/api` run above predates it and was not repeated. |
| `internal/api`, full, 1101 s | same tree, same disk | **20,224 pass, 0 fail, 6 skip**, exit 0, zero `no space left` lines — the first full green run of this package on the branch |
| `cmd/crewship`, full | 163bd2ef | **1 fail** — `TestEmbeddedJSONInlineIsAlsoYAMLSafe`, and it had been red since the work CLI landed. Only `-run TestDaemon\|TestAcceptance` had been run on that package, so nothing had looked. |

The `cmd/crewship` failure is worth naming rather than filing away as
pre-existing: `workItemDetail` embedded an unexported `workItemRow`, so
`crewship work get -f yaml` **panicked** while `-f json` worked, and every
multi-word field printed a different key under the two formats. It is this
programme's own CLI, shipped by commit `6659886`, and the only reason it looked
green was that nobody had run the package. Fixed by exporting the type, marking
the embed inline for both encoders, adding the explicit `yaml:` tags, and adding
all four work types to the parity list so the next field cannot drift.

One more entry, because a void run is not a failing one and the difference is
invisible unless somebody writes it down: a full `internal/api` run taken with
the kind-filter and I8 work reported **35 failures** and is worthless. The disk
(`/` on crewship-dev, 290 GB shared with a dozen worktrees) reached 100 % with
130 MB free mid-run. The log carries 22 `no space left on device` lines and the
failures are the signature pattern — whole families failing in 0.03 s each,
which reads exactly like a regression in the change under test. `~/.cache/go-build`
was 59 GB with every byte touched inside 24 h, so age-based trimming frees
nothing; dropping half the content-addressed directories brought it to 30 GB
without the full-rebuild cost of `go clean -cache`. **Re-run anything that
overlapped such a window rather than reading it.**

**No full `internal/api` run was green at 163bd2ef.** The earlier summary said
the package was "green apart from that one, which is now green" — that was an
inference from the guard being a pure source-scan test that cannot affect
others, not something observed, and it should have been written as the two rows
above. The first full run on a tree with the fix is the one taken with the
kind-filter and I8 work, recorded below it.

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

The missing link is in the memory clients. The orchestrator already injects
an authenticated per-run token into each exec and MCP configuration. The
sidecar can resolve it through `actingRunIdentity`, but `hostMutationFencing`
still reads run_id/generation only from the memory request body. The normal
client does not supply those fields, so it cannot use the host ledger yet.
Reuse the authenticated per-exec identity when closing R6; a container-boot
identity would go stale because a container outlives many attempts.

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
- **Dispatch-time permission checks** (I8). The agent-webhook dispatcher rechecks
  agent/workspace/crew existence and defers PENDING_REVIEW agents (§4a).
  The remaining producers have not been moved onto that shared contract.
  No agent-run spend-budget gate exists in 1.0.
- **External operations with an unclear result** (§4, T09). The table exists and
  nothing writes to it: no intent before a call, no receipt after, no
  reconciliation path.
- **Scheduling recovery and retention.** The webhook dispatcher runs
  `RecoverExpiredLeases` at startup and on its recovery timer. This does not
  wire retention: `RetentionPolicy.Sweep` still needs a production lifecycle.
- **The remaining producers onto shared admission** (I7): agent webhooks use
  the ledger; chat, assignments, schedules, pipeline execution and direct
  IPC/peer-query entrypoints still need the common admission contract.
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
- **`scripts/docs-inventory -strict` matches a variable name anywhere under
  `docs/`, so a mention is not documentation.** Worth knowing because it has
  already produced a false green here: a previous version of this file listed
  four undocumented variables by name in a sentence complaining that they were
  undocumented, and that alone turned the gate green. A reviewer re-running it
  on the same commit saw a pass and reasonably read it as the gate being
  satisfied. The four are documented properly now — `CREWSHIP_MEMORY_REQUIRE_GUARANTEED`
  in the agent-memory guide, `CREWSHIP_SIDECAR_STATE_DIR` and
  `CREWSHIP_SIDECAR_RUN_AUTHORITY` in the orchestration guide, each with
  purpose, default, who sets it, scope and what a misconfiguration does. The
  fourth, `CREWSHIP_RUNEND_AUTH`, was never a variable at all: it was a shell
  heredoc delimiter that happened to wear the prefix the inventory scans for, so
  the only way to satisfy the gate would have been to write documentation for a
  setting nobody can set. It is renamed instead.
- The vertical pass adds ~35 s to `internal/api`, most of it one test that waits
  out the shipped 10 s cancel grace on purpose. If that package's runtime becomes
  the problem, that test is the first candidate for a build tag — but shortening
  the grace would make it test a value no deployment uses.
- Published Standard Webhooks test vectors trip gitleaks. `.gitleaks.toml` has a
  narrow entry for that file, and the "these are public vectors" claim is enforced
  by a test that verifies each published signature against its secret.


## Codex takeover — 2026-09-11

Ownership of #2488 / PR #2490 was explicitly transferred by the user. Synced
origin/main in a separate merge commit (44ca466e); the only conflict was in
CHANGELOG.md and both sides' entries were retained.

Independent CI of 4a31e1b9 (run 34604690097) initially exposed three integration failures:

- `lint-tsformat`: the ledger timestamp parser was near a SQL query. Added the
  guard's scoped exception explaining that it parses stored timestamps rather
  than formatting a SQL parameter. The exact CI command now passes locally.
- Go Shuffle: `TestCollectWorkMetrics_EmptyLedgerZeroFills` confused a fresh DB
  with a fresh process. Another test's global acceptance sample survived.
  Reproduced with seed 1789133893833417385; the zero-observation assertion now
  runs in a child process. The entire server package passed that seed (114.873s).
- macOS ARM64: the crash child announced its runtime but exited 97 instead of
  dying from SIGKILL. Both dispatch and work crash helpers now check Kill errors
  and, after a successful send, wait for signal delivery instead of racing an
  os.Exit fallback. Linux coverage passed; a fresh macOS CI run is still needed.
  Neither os.Exit nor SIGKILL executes Go defers.

The Cancel/Defer HTTP test now installs its authorization barrier before the
loop starts. It no longer relies on rewriting eligible_at after dispatch has
begun. Three repeated executions with -race passed (72.298s).

Validation is separated deliberately: work passed in full (7.552s); server
passed the original failing shuffle seed; dispatch's first shuffled run failed
while the host was under severe memory pressure (a nominal 10s wait lasted over
146s and its lease expired). The full dispatch rerun with the same seed passed
in 11.010s. go vet ./... passed on the merged code before the work crash-helper
follow-up. The final full Go verification is a separate run, not inferred from
these package results. R6, mailbox, global I7 and real T06/T07 remain unfinished;
the parallel profile remains disabled.


A further failure surfaced when the original full 4a31e1b9 run finally ended:
`internal/database` hit its 40-minute package timeout while multiple migration
tests were still progressing, and `TestVertical_IntentIsDurableBeforeTheRuntimeExists`
failed because the fake provider reported Alive for every locator, even before
Run created it. The complete run is therefore NOT green despite its API package
passing (1128.741s).

The fake now publishes existence after the creation hook and only reports
locators it actually created. The ordering test pauses creation on a barrier,
requires Alive=false for the prewritten locator, then releases creation and
waits for confirmation. Restoring the old Alive through a Go overlay makes it
fail deterministically before creation (2.174s). The first full post-merge run
was stopped after this follow-up changed the test tree; the final full run was
restarted with -count=1, -p 4, GOMAXPROCS=4 and a 60m package timeout. No completion
result is inferred from the earlier run. Frontend lint and static build passed.


The last job of that same original CI run also failed. Go Race reported real
races in memory_write_host_test.go: asynchronous audit requests appended to the
stub's request slice while assertions read it. The recorder and readers now
share a mutex, and assertions select the last mutation by route rather than
assuming the last HTTP request was the mutation. The dispatcher harness now
registers idempotent cleanup even for tests that explicitly stop later, so an
early fatal assertion cannot leave its loop polling a closed database. The
outcome-classification test freezes store time to inspect retry_wait before
eligibility advances; its assertion no longer races the backoff. State polling
has a 30s failure bound for instrumented CI, not a claimed runtime SLA.

After those changes, both COMPLETE packages passed with -race -count=1:
internal/sidecar 144.536s and internal/dispatch 82.193s, local log
/tmp/crewship-2-takeover-race-fixes.log. This covers the extra test fixes in the
isolated worktree; the full Go run on 9b3ee52b is still pending and is not
retroactively described as covering these later changes.


R6 prerequisite: reproduced both MCP writers ignoring the runtime's required
profile (memory.write wrote AGENT.md; append_daily wrote today's journal). Both
now return a recoverable tool error without a local write when guaranteed memory
is required. The regression was red before the change; the complete MCP test
family passed after it. The complete sidecar package then passed with -race -count=1 in 132.901s
(/tmp/crewship-2-r6-required-sidecar-full-race.log). This is a fail-closed guard,
NOT completion of R6: authenticated run binding, shared HTTP/MCP revision
namespace, read provenance, retry identity and the host dispatcher bridge remain
unimplemented. The default stays off.


Shutdown review found two production gaps, independently reproduced against
SQLite. A refused Stop left the detached run context alive, relying on a future
heartbeat to discover the ended attempt. A Stop that consumed its deadline then
passed that expired context to the ledger, so parking failed and work stayed
running. Drain now gives the outcome write a fresh bounded context and cancels
local supervision after recording it. An unconfirmed external stop is still
needs_reconciliation, never claimed as confirmed cancellation.

Both regressions failed before their respective fixes. The complete dispatcher
package with both fixes passed under -race -count=1 in 87.045s
(/tmp/crewship-2-shutdown-final-full-race.log). A separate real HTTP vertical
suite and a final full Go-tree run cover the assembled code; their completion
results must be recorded before claiming either passed.

The earlier disk-backed full run on 9b3ee52b completed internal/api successfully
in 1321.309s. That predates the shutdown and MCP production fixes. The first final-code run used a dedicated TMPDIR under /dev/shm and a
disk-backed GOTMPDIR, but local Go 1.27 testing.TempDir prefers GOTMPDIR, so
that did not move its database fixtures. The subsequent complete run sets BOTH
to a dedicated /dev/shm directory to reduce shared-disk fsync contention. It proves the test contracts under
process failure, not OS/power-failure durability (T14 remains unproven).


### Next bounded implementation: finish R6 before mailbox

- Reuse the existing per-exec authenticated run capability. Sidecar must derive
  the run from it, reject same-agent run substitution from the body, and have
  the host resolve and validate the current attempt generation. Do not enable
  global run-authority enforcement while non-ledger producers remain.
- Construct the memory dispatcher on the host with trusted actor roots, the
  ledger, run authorization under the file lock, and the existing injection
  screen/quarantine/cap policy. Preserve the sidecar's credential-literal check.
- Unify the canonical namespace: HTTP currently anchors agent:<slug>/<file>,
  while the generic dispatcher uses agent:<agent ID>/<file>. Alternating HTTP
  and MCP writes must share one revision history, not two counters for one file.
- Return revision, hash, operation identity and provenance to the MCP client;
  currently ToolResult.Metadata is dropped. A successful internal ledger write
  is insufficient if the model cannot read the revision needed for its CAS.
- Require stable caller operation IDs and stable effective date/content for
  append_daily retries. Distinguish missing expected_revision from a deliberate
  first-write precondition at revision zero; do not require a model to invent
  an empty append bootstrap. Concurrent first writes must have one winner.
- Acceptance must cross a real host + sidecar + filesystem/SQLite boundary:
  HTTP/MCP read/append/replace share revisions; lost response retries once with
  the same identity; stale and substituted run tokens fail; two attempts reuse
  one long-lived sidecar without inheriting the previous run identity.

This scope does not complete mailbox, global I7, real-adapter T06/T07 or T14.


### Completed takeover verification — code commit 205fe4fc

The final COMPLETE Go-tree run returned COMPLETE_FULL_EXIT=0: 145 packages
passed and 10 had no test files. Command: go test -p 4 ./... -count=1 -timeout
60m, GOMAXPROCS=4, GOGC=50, with both TMPDIR and GOTMPDIR set to the dedicated
/dev/shm/crewship-2-takeover-complete directory. Raw output including the shell's
exit marker is /tmp/crewship-2-takeover-complete-full.log, copied to
/srv/crewship/backups/crewship_2/oponentura-4a31e1b9/.

| Verification | Observed result |
|---|---|
| Complete Go tree | exit 0; CLI 172.561s, API 199.211s, database 413.751s, dispatch 9.095s, sidecar 5.032s |
| Complete dispatch, final shutdown fixes, -race | exit 0, 87.045s |
| Complete sidecar, final required-MCP guard, -race | exit 0, 132.901s |
| Real HTTP TestVerticalServer_ family, final shutdown fixes, -race | exit 0, 126.083s; FILTERED, not the full API race suite |
| go vet -p 2 ./... on final code | exit 0 |
| golangci-lint on final code | exit 0, 0 issues |
| Strict docs inventory, migration lint, timestamp lint, invariant guard | exit 0 |
| Skip budget | 143 calls, baseline 143 |
| Frontend lint and static build | exit 0 on the merged frontend; takeover follow-ups change only Go/tests/docs |

Only AFTER the final full run passed were the two older redundant runs stopped:
the disk-backed 9b3ee52b run (API passed 1321.309s) and the partial-RAM final-code
run (API passed 242.297s). They have no successful whole-tree result and are not
counted as passes. The successful full run above covers the final production
code; the subsequent handoff-only commit records these observed results.

A fresh remote CI run must still verify macOS and the other platform lanes.
CodeRabbit's old rate-limit status is not a review of this code. No merge or
release-readiness claim is made. R6, mailbox, non-webhook I7, real T06/T07 and
power-loss T14 remain outstanding.


### Latest-main integration — code commit 3cdf0d4d

After the first push, GitHub still reported a conflict because main had advanced
to 34b4d660 (routine fixes #2494 and #2503). Merged it separately; only
CHANGELOG.md conflicted and all entries were retained. Repeated the COMPLETE Go
run on the merged code with both temporary directories in dedicated RAM storage:
MAIN_SYNC_FULL_EXIT=0, 145 packages passed, 10 without tests; API 189.099s,
CLI 197.091s, database 322.832s. Raw log: /tmp/crewship-2-main-sync-full.log,
also archived beside the earlier review evidence. Repeated go vet, frontend
lint (0 errors, 31 warnings) and static build: all exit 0. The affected routine
presentation test file also passed all 24 tests. No production code changes
follow this result; this handoff-only update records it.


### Closing review — follow-up to remote CI on 52fb715e

Remote run 34613770592 completed with Go Race failing and CodeQL reporting two
path-injection flows. Ordinary Go, shuffle, full API race, CLI race, macOS ARM64,
Linux ARM64, lint and frontend checks passed. This is NOT a green PR.

The failing empty-ledger metrics subprocess spent its two-minute limit rebuilding
all migrations before its assertions. It now opens a fresh migrated database
prepared by the parent, while retaining separate-process sampler isolation.
The dispatcher harness no longer writes heartbeats every 20ms or polls cancel
every 10ms; production intervals and state assertions are unchanged. The periodic
recovery test additionally waits for authorization of a probe work item, proving
boot recovery completed before expiring the old lease. Its previous sleep could
not establish that ordering.

The HTTP memory mutation resolver now checks the absolute resolved path against
the configured host storage directory explicitly, in addition to the existing
narrower safepath checks. This is a lexical boundary check, not a claim of
symlink or power-loss containment. CodeQL must evaluate the new commit before
its two findings can be considered resolved; no alerts were dismissed.

Observed validation so far: complete final dispatcher package with -race passed
in 171.731s (FINAL_DISPATCH_EXIT=0). A Go overlay removing only the periodic
recovery action makes TestVertical_RecoveryRunsOnATimerNotOnlyAtBoot fail:
starting after 30s, want succeeded (RECOVERY_MUTATION_EXIT=1). The tracked
production dispatcher was never modified for this mutation. Logs are under
/tmp/crewship-2-close-*.log; final whole-tree results follow below.

Closing whole-tree run completed: CLOSE_FULL_EXIT=0, 145 packages passed and
10 had no test files. Command: go test -p 4 ./... -count=1 -timeout 60m, with
GOGC=50, GOMAXPROCS=4, and both TMPDIR/GOTMPDIR under the dedicated
/dev/shm/crewship-2-close-tests directory. It covers the final production change;
the deterministic boot-barrier TEST edit was made after this run started and
was separately covered by the complete final dispatcher race run above.
Complete dispatch + server race run also returned CLOSE_PACKAGES_RACE_EXIT=0
(dispatch 98.963s, server 443.874s). go vet -p 2 ./... and golangci-lint both
returned exit 0; lint reported 0 issues. Remote CI and CodeQL on the next pushed
commit are still pending; CodeRabbit remained throttled at this checkpoint.


### Closing security follow-up — symlink escape reproduced after 6bc2afc6

CodeQL on 6bc2afc6 passed. A separate real-HTTP probe nevertheless reproduced
an actual host filesystem escape: a daily-directory symlink pointed outside
configured storage, and an append created the outside file while returning
HTTP 200, profile guaranteed, revision 1, ledger_recorded true. Evidence:
/tmp/crewship-2-close-symlink.log, SYMLINK_PROBE_EXIT=1. Passing the lexical
scanner is therefore not evidence of filesystem confinement.

The mutation and canonical-read HTTP routes now supply StorageRoot. A common
canonical-file handle walks from that trusted root through directory descriptors,
refuses symlink components and checks that each opened directory is the same
object it inspected. It pins the final parent until the operation ends. Base
reads, the sentinel lock, atomic write and inline pending-intent recovery use
that handle; canonical absolute paths remain ledger identities, not fallback
I/O paths. Cap rejection uses the size of the base already read, avoiding a
second pathname lookup. Existing root-aware lock/durable-write primitives are
reused. Empty StorageRoot retains the trusted in-process/legacy contract;
the standalone RecoverPending helper is still a trusted, path-based API and
has no production callers. This change does not finish R6 or wire its sweeper.

Regression coverage: real HTTP reads/writes refuse a planted daily, .memory
or agent-directory symlink and preserve outside content without creating an
outside lock. Deterministic memory tests replace the parent during Authorize,
after root/lock acquisition but before recovery/base reads; both ordinary writes
and completion of a pending intent stay in the original opened directory.
The focused mutation/canonical suites passed (ROOT_FOCUSED_FINAL_EXIT=0).
Full-tree, race and mutation results for this follow-up are recorded below
only once observed. The previously green whole-tree run does not cover it.

Observed security-follow-up results: complete internal/memory with -race passed
in 134.484s (ROOT_MEMORY_RACE_EXIT=0); the FILTERED real-HTTP mutation/canonical
API family with -race passed in 66.014s (ROOT_API_RACE_EXIT=0). go vet -p 2 ./...
and golangci-lint returned exit 0, lint 0 issues. Windows amd64 memory test-binary
cross-compilation passed (ROOT_WINDOWS_BUILD_EXIT=0); it was NOT executed on
Windows. An overlay discarding StorageRoot makes BOTH parent-swap cases fail
and ALL three real-HTTP symlink scenarios return outside content / modify the
outside file (ROOT_MUTATION_EXIT=1). Production files remained untouched by
that mutation. Raw evidence uses the /tmp/crewship-2-close-root-*.log prefix.

The final security-follow-up COMPLETE Go run finished with ROOT_FULL_EXIT=0:
145 packages passed, 10 without test files. Command: go test -p 4 ./...
-count=1 -timeout 60m, GOGC=50, GOMAXPROCS=4, both TMPDIR/GOTMPDIR set to
/dev/shm/crewship-2-close-tests. All final Go and test edits preceded this run;
only changelog/handoff text changed afterwards. This remains RAM-backed test
evidence, not power-loss evidence. New-head remote CI/CodeQL and an actual
CodeRabbit review remain required; the earlier scanner pass covers 6bc2afc6.


## 2026-09-12 — response to the second Anthropic review (work in progress)

The negative merge verdict is accepted. Local merge `ab96578c` integrates
`origin/main` at `a0a5b3cb`; previous green CI is not evidence for this tree.
No merge or deployment has been performed. Issue #2488 is claimed for this work.

Current uncommitted repairs:

- Shutdown has one state writer, the attempt supervisor. Stopping for server
  shutdown is not a user cancellation. A classified result can settle; an
  ambiguous interrupted result remains `needs_reconciliation`.
- The webhook adapter retains immutable container/slug/run launch identity
  until settlement. Production StopRunAt/RunIsAliveAt no longer require the
  credential HOME registry entry to survive RunAgent's cleanup. This identity
  is process-local; restart lookup is still not a proven production capability.
- Workspace resolve API and `work resolve` require current generation, terminal
  outcome, authenticated manager, reason and runtime-stop attestation. This is
  an operator decision, not an automatic probe. Replay is a separate action.
- Routine delivery deduplication moves to `routine_webhook_receipts`, without a
  work item. Pipeline execution remains direct. No durable pipeline dispatch
  recovery is promised. Receipt keys currently persist without automatic expiry;
  earlier routine receipts are copied without losing dedup identity, and unclaimed
  phantom work is fenced into reconciliation for operator inspection. Pipeline
  work cancel/replay is refused in favor of the pipeline API.
- R8 throttled lookup checks payload hash and distinguishes database failure.
  Accepted work checks ingress item/byte limits in its acceptance transaction.
  Process-local execution occupancy no longer rejects acceptance.

Observed evidence so far (logs `/tmp/crewship-2-review2-*`):

| Check | Observed result | Scope |
| --- | --- | --- |
| shutdown red | failed with cancelled | successful-stop reproducer before fix |
| dispatch package | pass, 12.137 s | entire package before later cancellation-read error guard |
| R8 red | 202 instead of 409; 429 instead of 503 | actual failures, after fixing an invalid test URL |
| R8 green | work 1.808 s, API 2.627 s | filtered acceptance/capacity tests |
| resolve | work 1.509 s, API 2.130 s, CLI 7.546 s | filtered store/API/real CLI process |
| launch location / queue capacity | orchestrator 0.044 s, API 2.353 s | filtered provider-probe and HTTP tests |
| routine / production probe | API pass, 9.781 s | filtered routine tests and vertical test; agent execution and container transport substituted, production Stop/Alive retained |
| OpenAPI schema catalog | generator 5.544 s, inventory 0.031 s | both whole packages |
| full Go / vet | running, no final result yet | do not call green |

Remaining review work: R6 authenticated capability binding and agent-facing
mutation path; R7 proxy revocation; shared admission across chat and webhook;
real Claude T06/T07; T14; mailbox. Parallel profile remains off. This entry is
interim and does not approve merge. Final validation and migration/backward
compatibility review are still required.


Follow-up evidence during the same turn:

- Full Go run found missing resolve role manifest entry and missing backup table
  classification. Both corrected; this run is red, not a successful full suite.
- R6 token-A/body-B reproducer wrote memory and returned HTTP 200 with guaranteed
  profile before correction. The host now verifies the per-run MAC and matches
  workspace, agent and run, in addition to the ledger generation check. Sidecar
  forwards the caller's capability. Filtered internal memory / host memory /
  run-status tests passed: API 5.958 s, sidecar 0.324 s. This does not complete
  the MCP guaranteed-memory path or the whole R6 release requirement.
- Vet exit 0; golangci-lint reported 0 issues; frontend lint exit 0 with 31
  warnings; frontend static build exit 0. Vet/lint preceded the R6 binding and
  legacy receipt migration additions and must be repeated for the final tree.


### Final observed local validation for the 2026-09-12 follow-up

The last complete run is `crewship-2-review2-all-go-verified.log`, with
`ALL_GO_VERIFIED_EXIT=0`: **145 packages passed, 10 had no tests, zero failed
packages and zero disk-full lines**. Command:

```bash
TMPDIR=/dev/shm/crewship-2-review2-AJOSNi GOTMPDIR=/dev/shm/crewship-2-review2-AJOSNi GOGC=50 GOMAXPROCS=4 go test -p 4 ./... -count=1 -timeout=30m
```

This run includes all production and test changes in this follow-up, including
capability forwarding, the corrected registry fixture and the routine upgrade
migration. It used the real Next.js export staged by the repository embed
script (1341 files); RAM-backed test storage is not power-loss evidence.

| Check | Final observed result |
| --- | --- |
| entire API package | 189.032 s, pass |
| entire CLI package | 161.534 s, pass; acceptance tests execute CLI subprocesses |
| entire database package | 361.966 s, pass |
| entire sidecar package | 4.045 s, pass |
| entire work / dispatch, race | 85.375 s / 86.693 s, pass |
| API race, **filtered** | 62.664 s, pass: production location probe, capability mismatch, resolve |
| location mutation overlay | exit 1, expected: removing the retained-location Alive path leaves work starting; production tree was not edited |
| vet / golangci-lint | exit 0 / 0 issues (last subsequent code change was initialization in the test fixture; complete Go suite above covers it) |
| frontend lint / static build | exit 0 / exit 0; lint has 31 warnings |
| Go binary / linux sidecar build | exit 0 / exit 0; local review artifacts only, no install or deployment |
| strict docs inventory | clean, 664 API operations / 907 CLI commands |
| invariant / skip guards | pass; 144 skips at baseline 144 |

Earlier complete run `all-go.log` failed the route-role and backup-classification
gates. `all-go-final.log` failed a new test fixture's nil run registry. Both are
archived, neither is relabelled green. The corrected entire sidecar package
also passed independently (22.302 s) before the successful complete run.

A final fetch confirmed origin/main still at `a0a5b3cb`, with zero commits
missing from this branch. Raw logs and failed runs are archived under
`/srv/crewship/backups/crewship_2/review-2026-09-12/`. Review build artifacts are
`/tmp/crewship-2-review2-build/crewship` (with UI) and `crewship-sidecar` (Linux).

Remote CI and actual review on the new pushed head remain separate requirements.
The preceding CodeRabbit status was throttled, not reviewed. No merge or deploy
is authorized by this local evidence, and the release gaps listed above remain.
