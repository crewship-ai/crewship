# ADR: River over SQLite for the 1.0 durable work queue — **reject**

Date: 2026-09-10 · Status: **decided (reject)** · Issue: [#2488](https://github.com/crewship-ai/crewship/issues/2488)

Closes the technical evaluation ordered by [SPIKE-RIVER-SQLITE-1-0.md](SPIKE-RIVER-SQLITE-1-0.md).
The default path of the [implementation contract](WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md)
§2 stands: extend the existing SQLite queue primitives under one dispatch owner.
Redis, Postgres and Temporal remain out of 1.0 and were not re-evaluated here.

## What was measured

Harness: [`tools/spike-river/`](../../tools/spike-river/) — an isolated Go module, so River
is **not** in the main `go.mod`. Every scenario opens a fresh SQLite file under a temp
directory with the DSN copied verbatim from `internal/database/database.go:113-126`. The
worker is a mock: no credentials, no external effects.

```
go run ./tools/spike-river -scenario all -pool 5 -sync FULL -ops 200 -workers 32 -hold 5s \
  -managed-wal=true -json docs/prd/reports/spike-river-sqlite-results.json
```

**Re-measured 2026-09-10 under managed WAL.** The first run of this spike used
SQLite's inline autocheckpoint, which the daemon never runs — it opens
`WithManagedWAL()` (`cmd_start.go:197`), i.e. `wal_autocheckpoint(0)` plus a 2 s
checkpointer goroutine. The harness now mirrors that, and it changes the FULL
numbers by roughly 3x. Every figure below is from the production shape; the
superseded ones are noted where they were quoted.

Raw output: [`reports/spike-river-sqlite-results.json`](reports/spike-river-sqlite-results.json).

| | |
|---|---|
| River / riverdriver / riversqlite | `v0.47.0` (all three, pinned) |
| SQLite driver | `modernc.org/sqlite v1.58.0` → SQLite library `3.53.4` |
| Go | `go1.27.1` |
| Host | crewship-dev, 12 vCPU, 46 GiB RAM, Linux `6.8.0-137-generic` |
| Filesystem | ext4 on `/dev/sda1`, non-rotational (`ROTA=0`) |
| DSN | `busy_timeout(30000)`, `journal_mode(WAL)`, `synchronous(<NORMAL\|FULL>)`, `foreign_keys(ON)`, `cache_size(-65536)`, `temp_store(MEMORY)`, `mmap_size(268435456)`, `_txlock=immediate` |
| River migrations applied | 7 |

The harness reproduces the server's `WithManagedWAL()` + dedicated checkpointer
(`cmd/crewship/cmd_start.go:197,210`): autocheckpoint off, a 2 s tick running
`PRAGMA wal_checkpoint`, escalating to TRUNCATE past the production threshold.
Setting the pragma without running the goroutine would measure a configuration
nobody ships either, so the harness does both.

## Results against the spike's own success criteria

| Criterion (SPIKE §"Úspěch a rozhodovací pravidlo") | Result | Evidence |
|---|---|---|
| No orphan delivery/job on rollback | **PASS** | `tx`: after rollback, 0 deliveries and 0 river_job rows. Commit produces exactly 1 of each and the worker runs it once. |
| Worker must not get the work before commit | **PASS** | `tx`: `jobs_visible_before_commit = 0`, `worked_before_commit = 0` with the client already started. |
| No second logical job from one delivery | **PASS** | `dup`: 32 concurrent identical deliveries → 1 accepted, 31 reported duplicate, 1 delivery row, 1 river_job, 1 distinct receipt, 0 errors. |
| Old-attempt completion cannot overwrite the newer result | **FAIL** | `fencing` — see below. |
| Config must not globally flip the live API to a single connection without measured impact | **FAIL** | `pool` — see below. |
| Uncontended acceptance p95 ≤ 500 ms, p99 ≤ 2 s | **PASS** | `contention` at pool 5 / FULL / managed WAL: p50 22 ms, p95 52 ms, p99 65 ms. An order of magnitude of headroom, not two. |
| Under a held lock: commit inside the 2 s budget, or a retryable error — never a false `202` | **SPLIT** | No false ack (`cancelled_op_left_row = 0`), but the budget is **not** enforceable by context — see below. |
| Does driver cancellation interrupt the 30 s busy wait? | **NO** | `contention`: a 500 ms context, against a lock held 5 s, returned after **5044 ms**. Unchanged by the WAL configuration. |

### The two hard failures

**1. River does not fence by attempt.** Its completion statement is, verbatim from
`riverdriver/riversqlite/internal/dbsqlc/river_job.sql:595-623` (`JobSetStateIfRunning`):

```sql
UPDATE river_job SET … WHERE id = @id AND state = 'running'
```

The predicate is `(id, state)`. There is no attempt or generation term. The `fencing`
scenario walks one job through claim(attempt 1) → lease loss → rescue → claim(attempt 2),
then applies that exact predicate on behalf of the stale attempt-1 worker:

- `river_predicate_rows_affected: 1` — **accepted**
- `state_after_stale_completion: completed`, `attempt_after_stale_completion: 2`
- the same write with `AND attempt = 1` added: `0` rows — rejected

So a worker that lost its lease can mark the *newer* attempt's job completed. That is
exactly what invariant **I5** forbids. Adopting River would not discharge I5; Crewship
would have to add a generation term on its own rows anyway.

**2. River's recommended pool size is incompatible with this codebase.** The driver's own
package doc recommends `dbPool.SetMaxOpenConns(1)`. Crewship runs `SetMaxOpenConns(5)`
(`internal/database/database.go:145`), and that is load-bearing for read latency. The
`pool` scenario opens a `*sql.Tx` and then asks the same pool for a second connection:

| pool | second connection inside an open transaction |
|---|---|
| 1 | **starved** — `context deadline exceeded` after 3001 ms |
| 2 | ok, 0 ms |
| 5 | ok, 0 ms |

Any acceptance handler that enqueues inside a transaction and then reads anything back
through the pool deadlocks at pool 1. Running River at pool 5 instead means running a
driver its authors describe as *"currently in early testing … minimal real world use as of
yet"* outside its own recommended configuration.

### Findings that bind the default path too

These are properties of SQLite and the modernc driver, not of River. They survive the
reject and become stage-B requirements.

**FULL is affordable.** Cost of `synchronous=FULL` on this host, 200 ops each:

| | delivery row only | full acceptance transaction |
|---|---|---|
| NORMAL p50 / p95 | 20 µs / 32 µs | 261 µs / 459 µs |
| FULL p50 / p95 / p99 | 6371 µs / 12381 µs | 7901 µs / 13807 µs / 21334 µs |

FULL costs roughly 6.4 ms of fsync per commit — a ~300× multiple on the bare write, and
~30× on the acceptance transaction. In absolute terms p95 13.8 ms against a 500 ms budget,
so §3's "FULL on every authoritative connection" is affordable at the design load. It is
**not** free at high write rates: 6.4 ms of serialised fsync puts a ceiling near 150
commits/s on a single writer, which the capacity report must state rather than assume.

**A context deadline does not bound an acceptance request.** With a 500 ms context and a
write lock held elsewhere for 5 s, the acceptance path returned after 5044 ms with
`context deadline exceeded` — it waited for the lock, then noticed. This matches the driver
source: `modernc.org/sqlite@v1.58.0/tx.go:36,50,58` passes `context.Background()` into
`Commit` and `Rollback`, so a busy commit is uncancellable for the full
`busy_timeout(30000)`. §5's "odpověď vzniká nejpozději 2 s po úplném načtení těla" therefore
cannot be implemented by wrapping the handler in a context. Stage B needs an explicit
admission guard in front of the transaction (a bounded acceptance semaphore, or a shorter
`busy_timeout` on the acceptance connection), and T04 must assert it.

The one piece of good news: the budget overrun did not produce a false `202`. The
cancelled operation left no delivery row, and the path recovered cleanly once the lock was
released (`recovered_after_release: true`, 199/200 contended ops committed, 0 `SQLITE_BUSY`).

This is now enforced rather than merely observed: `internal/work`'s acceptor owns a
handle whose `busy_timeout` sits under its budget, and measured against a lock held
6 s with a 600 ms budget it refuses after **452 ms**.

## Decision

**Reject River for 1.0.** Four reasons, in order of weight:

1. It does not discharge the invariant it would have been adopted for (I5); Crewship-side
   fencing is required either way.
2. Adopting it means running an explicitly early-testing driver outside its own
   recommended pool configuration, on the database that holds every domain row.
3. It buys little that is missing. Per the stage-A audit, `assignments` already carries
   `lease_owner`/`lease_expires_at` with a 20 s heartbeat, a 15 s owner-aware lease
   sweeper, a stuck-RUNNING sweeper and a boot recovery pass
   (`internal/api/assignments_lease.go`, `assignments_running_recovery.go`). The real gaps
   there are the absence of `attempts`/`eligible_at` and a recovery pass that *fails*
   orphans instead of re-queueing them — neither of which River would fix for us.
4. The measured constraints are SQLite's, not the queue library's: ~21 ms of fsync
   per durable acceptance commit and an uncancellable busy wait. Both apply
   identically to either choice.

This is a **reject**, not an *unresolved*. The two failing criteria are measured and
reproducible, not inconclusive.

**Not claimed:** the spike did not run a real process-crash restart (kill a worker
mid-claim, restart, deliver the old attempt's completion). The fencing question was
settled at the level of the driver's own completion statement and its observable effect on
the row, which is sufficient to decide adoption but is *not* the T08 evidence. T08 still
owes a genuine crash harness, and per §11 a `kill -9` is not evidence of OS-crash
resilience either. Nor were River Pro's feature boundaries mapped; the reject makes that
moot for 1.0.

## Consequences

- The default path proceeds: one dispatch owner over extended SQLite primitives. No new
  production dependency, no second scheduler.
- `tools/spike-river/` stays in the tree as an isolated module with its own `go.mod`. It is
  not built by `go build ./...`, not shipped in the image, and carries no credentials. It
  is kept so a future re-evaluation starts from a running harness rather than from this
  document, and because four of its scenarios (`tx`, `dup`, `fencing`, `pool`) are the
  correctness bar the in-house implementation must clear too.
- Stage B inherits three requirements from the measurements above, all now
  implemented: FULL on authoritative connections with its ~42 commits/s ceiling
  stated in the capacity report; an explicit acceptance budget guard that does not
  rely on context cancellation; and a generation/fencing term on the work rows.
- Revisit if a measured single-writer limit appears, or if the driver leaves early testing
  *and* grows attempt-level fencing. Neither is a 1.0 concern.
