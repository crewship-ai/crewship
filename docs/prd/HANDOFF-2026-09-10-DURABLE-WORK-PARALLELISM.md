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

### `internal/work` — one owner for dispatch

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

### Durability

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
| **I1** no `202` before a durable commit | Mechanism exists (`work.AcceptTx` takes the caller's `*sql.Tx`); **not yet wired to any handler** |
| **I2** one delivery → at most one work item | Schema enforces it (`UNIQUE(workspace, endpoint, source_delivery_id)` + a partial unique index on the content key); **no handler writes those rows yet** |
| **I3** one active turn per session; atomic claim | **Demonstrated**, including that an unreconciled turn keeps the session |
| **I4** state/memory/sidecar changes verify the live run | Demonstrated for **state**. Memory and sidecar: not started |
| **I5** an old attempt cannot overwrite a newer one | **Demonstrated**, mutation-checked |
| **I6** memory writes are not lost under concurrency | Not started (diff primitive only) |
| **I7** no producer bypasses admission | **Not met.** Three producers still start runs with no shared lock at all |
| **I8** rights checked at acceptance and at dispatch | Columns exist; the dispatch-time re-check is not implemented |

## 3. T01–T14

Nothing is PASS. Saying otherwise would be the exact error the PRD warns about.

| | State |
|---|---|
| T01 signatures, rotation, timestamp edges | Signature half covered by unit tests; HTTP status mapping, oversized and chunked bodies **not** |
| T02 duplicates under concurrency | Ledger + spike scenario; **no handler**, so not the real path |
| T03 crash before/after commit | **No process-crash harness exists in the repo.** Must be written |
| T04 held write lock, checkpoint, FULL everywhere | Measured in the spike; not asserted as a repo test, and no budget guard exists yet |
| T05 all producers at once, mailbox | Mailbox table exists, unused. Not started |
| T06 real Claude, chat + background overlapping | **Not attempted.** Admission-level only |
| T07 start/cleanup/cancel B during A | Not started (E0 in progress) |
| T08 lost heartbeat, late completion, restart | Lease/fencing/recovery unit-tested; the restart half needs T03's harness |
| T09 external success without a receipt | Table exists, unused |
| T10 memory CAS, append retry, cap race | Diff primitive only |
| T11 memory crash recovery | Not started |
| T12 UI reconnect, two streams | Not started |
| T13 retention, disk full | Retention columns exist; no sweeper |
| T14 OS/VM crash | Not started, and by design cannot be a `kill -9` |

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

## 6. The next concrete step

In order. The first two are what the release commitment actually waits on.

1. **Wire acceptance.** Put the agent and routine webhook handlers onto
   `work.AcceptTx` + `internal/webhook/profiles`, inside one transaction, with the
   delivery ledger row. This retires W2, W3, W4 and gives T01–T03 something real
   to test. It requires the **acceptance budget guard** from §1 — a handle with a
   `busy_timeout` shorter than the budget — because a context will not bound it.
   Note that `internal/api/pipeline_webhooks_failed_forget_test.go` pins the W6
   behaviour that has to be inverted; deleting that test is the visible sign the
   contract moved, so do it deliberately.
2. **Write the process-crash harness.** T03, T08 and T11 all need it and none of
   them can be honest without it: spawn the real binary with a configurable kill
   point, `SIGKILL`, restart on the same data dir, and diff the ledger. A `kill -9`
   is still not OS-crash evidence — T14 is separate and needs a statement of the
   storage assumptions.
3. **Finish E0 and prove T06.** The mechanical per-run rename is in progress. The
   parts deliberately left out because they need a decision: splitting `HOME`
   (which is also the parent of `.memory`), splitting `/output/<slug>` (four read
   surfaces), splitting `/secrets/<slug>`, and per-run sidecar identity — which the
   PRD calls a release blocker if unmet and which is the single largest piece.
4. **The memory mutation contract**, on top of `memdiff`.
5. **Collapse the six pumps** onto `work.Claim`. Do this after 1–4: it is the
   change most likely to break live behaviour, and it wants the crash harness in
   place first.

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
- Published Standard Webhooks test vectors trip gitleaks. `.gitleaks.toml` has a
  narrow entry for that file, and the "these are public vectors" claim is enforced
  by a test that verifies each published signature against its secret.
