# Experimental offline workbook operations

This module prepares GridCraft workbook candidates and commits them through Go.
The server runtime and agent tools are disabled by default. It is a narrow pilot,
not a collaborative editor or a dynamically installed application platform.

The compiled Artifact format is `gridcraft-workbook`, schema 1, with execution
profile `headless-worker/v1`. Native JSON imports, `cell.set` and the atomic
multi-cell `cells.set` on the active sheet are implemented. Embedded UI, arbitrary engine commands, client sync,
shared-folder direct edits, proposals, export and restore are unavailable.

An operator must install and qualify a persistent immutable worker image. Configure
`gridcraft.enabled`, `gridcraft.service_socket`, `gridcraft.service_token_file`,
`max_workers` (default 4) and `max_workspace_workers` (default 2). Environment
overrides are `CREWSHIP_GRIDCRAFT_ENABLED`, `CREWSHIP_GRIDCRAFT_SERVICE_SOCKET`
and `CREWSHIP_GRIDCRAFT_SERVICE_TOKEN_FILE`. The application connects to an
authenticated Unix socket; it never launches containers or supplies run tokens.
The service runs one candidate at a time, independently of application restarts.
A service token authorizes only candidate computation, never database persistence.
Portable workbook backup, fork and recovery are deferred. A workspace dump
containing workbooks is refused rather than silently dropping their content.

Each mutation is bound to artifact, authority and mutation ID. The canonical
request fingerprint includes original preconditions and mandate. Current
credentials/ACL/run authority are checked before receipt lookup, and again in
the fenced commit transaction. A confirmed exact retry returns its original
immutable receipt before checking the current head. Reused IDs with changed
payload conflict. Unfinished reservations return `in_progress`; lease takeover
increments a fencing token, so a displaced candidate cannot commit. The service
also holds physical worker slots until computation and cleanup return; an expired
lease alone does not free global, workspace or artifact capacity. The persistent worker also enforces physical capacity across application
restarts; it runs several candidates concurrently only for different artifacts,
because this admission keeps at most one candidate per artifact. Its managed container restart discards all private candidate scratch.

Concurrency is per written cell. A cell mutation whose `base_rev` is behind the
head is computed on the head and committed when the generation matches, no
import (or pruned history) lies in between, it lags at most 256 revisions and no
other writer changed one of its target cells since its base. A writer is the
authority, plus the client `session_id` for a human, so two windows of one
person are distinct writers. A rejection carries the head revision, the
conflicting cells with their input in the head, and `SnapshotRequired` whenever
retrying the same request cannot succeed (generation, base ahead of head, stale
import, gap or window). If the head moves while a candidate is computed, the
commit fails with `precondition_failed` without either, and the same request may
be retried. An agent mandate keeps its original base revision and targets; it
can still commit after a human changed other cells. Imports require the exact
head.

The read set is not protected: a literal derived from another cell seen at
`base_rev` commits even if that cell changed since, and a target write changes
every dependent formula, including ones created after a mandate was admitted.
Formulas themselves are always recalculated on the head. This is accepted for
the pilot and documented in `docs/cli/workbook.mdx`.

The rebase window is read and parsed in a separate, already authorized read
transaction before the writer transaction; inside it only revisions committed
since then are scanned, so a stale edit over large commands does not hold the
database-wide write lock while parsing them.

The engine computes outside the database transaction. The candidate binds the
artifact, base revision, generation, command hash, mandate, pinned engine and
result hash. Concrete cell effects must fit the mandate. The GridCraft adapter
also compares the entire reopened workbook, separating authored data (input
value, formula, style and every other field) from derived data (the computed
value of a formula cell). Outside the target cell, authored data must be
identical and no cell may be added or removed. A formula cell's value may change
only when its formula is unchanged and both values are plain numbers, text,
booleans or errors. The worker then reruns the pinned engine's cached-value
oracle (`check --tolerance 0`) over exactly the bytes it returns, copied into
a new private directory, and reports their SHA-256. The check is a confined
engine execution like every other (Landlock write scope on that directory
only, seccomp, resource limits, its own session swept afterwards), so no
process of an earlier execution survives into it or can write its files; the
report goes to an unlinked file the worker holds. The host
requires the digest to match the result it received, counts formula cells
itself and requires the report's count to match, and accepts derived changes
only when every formula cell was checked and matched exactly. A formula in the
target cell also requires a report with no mismatches and non-iterative
calculation. Volatile functions such
as `NOW()` cannot be confirmed, and with iterative calculation enabled the
oracle accepts any fixed point of a cycle, so such workbooks reject any edit
that recalculates another cell. The receipt's effects list only the mandated
target; the audit record adds the result digest, the recalculated cell count
(and up to 100 addresses) and the report. The host verifies the authored diff
itself but trusts the worker's report and the engine's formula semantics: the
oracle is a second, independent evaluation by the same pinned engine, not a
different implementation. An engine pin identifies trusted format semantics;
isolation alone does not.

Go atomically stores head snapshot, immutable receipt, audit and outbox. Each
audit row records the revision kind (`snapshot` for an import, `cells` for cell
edits), its writer, the canonical command (JSON without HTML escaping, at most
1 MiB) and the hash of the resulting snapshot. History keeps a full snapshot only
for imports and every 50th revision (`gridcraft_revisions` checkpoints); the
head is always a full snapshot. Any revision can be rebuilt from the nearest
checkpoint at or below it by replaying the recorded cells with the pinned engine,
and each step is verified against the recorded result hash (tested with the real
engine). The history quota counts checkpoint snapshots and recorded commands
(default 64 MiB) and at most 1000 checkpoints. Reaching either limit rejects
new writes with `quota_exceeded` without changing the head or deleting history.
Retention must be an explicit operation; a commit never prunes earlier revisions.
Receipts require FULL synchronous storage and report `durability: node`.
`Store.Commits` serves the confirmed revisions after a revision (the commits
feed) under the caller's authorization; a range reaching pruned or pre-history
revisions, or below the principal's `HistoryFloor` (an agent mandate's base
revision), fails with `HistoryGoneError` (`snapshot_required`, with `MinSince`).
`Store.ClaimCommitted` claims undelivered outbox rows once, marking them `sent`
(it checks for pending rows with a plain read before taking the writer lock);
the host then pushes revision numbers (never content) on the Page channel.
Delivery is at most once: a push lost after the claim is not repeated, and
clients recover from the commits feed. Rows still `pending` when the host
stopped are claimed by its sweeper after the next start. Presence is not
implemented.
No receipt detail or replay fence is deleted by this module. A fresh candidate
always starts from the host's confirmed snapshot; no engine promotion is needed.

`gridcraft_intents` retains one explicit human instruction for a particular live
run, Page revision/generation and target. It is not an ACL. The existing signed
run capability, human chat principal, membership epochs, crew policy, ownership,
Page version and private audience are checked transactionally on every use.
The private chat is sealed before agent access. A revoked intent cannot be
renewed, and a new run does not inherit it. The Page owner edits cells through
`POST …/workbook/edits` (`cells.set`, up to 64 cells on one sheet, one engine
run, mandate `human-edit`); other humans cannot edit. Mandate IDs with the
`human-` prefix are reserved and refused for agents. The adapter still applies
the recalculation effect check to human edits, so a human edit that
recalculates other cells of a workbook with volatile formulas, or whose
recalculation cannot be verified, is refused with `effect_out_of_mandate`.

Deploy the application host before the worker image: the host decodes worker
responses strictly, so an older host rejects the newer worker's recalculation
report and every `cell.set` fails until the host is updated. An older worker
without the report fails closed on the newer host for recalculated cells only.

Public interface and examples: [`docs/cli/workbook.mdx`](../../docs/cli/workbook.mdx).

Tests use migrated real SQLite databases, synthetic fixtures, production Router
and a real CLI binary. Set `CREWSHIP_TEST_GRIDCRAFT_BINARY` to the pinned upstream
CLI to execute the opt-in real-engine contract. That test uses synthetic input
in a local process; it does not attest the deployed sandbox or a live model run.

The qualified worker transport requires Linux. Before sending the service
secret, the client checks a real private socket directory (0700 or 0750), a
0600 socket, ownership by the application service UID and the connected peer's
kernel UID. It fails closed on other platforms. This does not distinguish two
processes running under the same UID; deployment isolation remains necessary.

Docs and Slides human initialization reject an attached workbook in their
existing authority transactions, even when workbook routes are disabled.
Workbook transactions reject all other installed content formats, including
boards and HTML artifacts. Existing corrupt mixed-format pages fail closed;
this pilot does not include a repair or deletion workflow for such pages.

## Test barrier (development builds only)

`Execute` has two hooks, `pre-commit` (after the candidate, before the commit
transaction) and `post-commit` (after a durable commit, before the receipt is
returned). In normal builds they are no-ops (`barrier_off.go`). Only a binary
built with `-tags crewship_testbarrier` can park operations, and only when:

- `CREWSHIP_GRIDCRAFT_TEST_BARRIER_DIR` is a clean absolute path to a real
  directory with mode `0700` owned by the service user, and
- `CREWSHIP_GRIDCRAFT_TEST_BARRIER_SLOT` is `dev1`..`dev4`, and
- the operator marker `/etc/crewship/test-barrier-<slot>.enabled` exists for
  exactly that slot: a regular file (not a symlink) owned by root, not
  writable by group or others, whose only content is
  `slot=<slot> port=<server.port>` naming this instance's configured listen
  port, while the service itself does not run as root.

The environment is self-asserted; the marker is not, and it binds the
barrier to one instance rather than to the host: several instances (stage
included) share a host and `/etc/crewship`, and an instance listening on a
different port never matches. Provisioning writes the marker only for the
development instance that should have it (for example an Ansible `copy` with
`content: "slot=dev2 port=8082\n"`, `owner: root`, `mode: "0644"`) and asserts
that no marker names a stage or production port and that stage and
production set neither environment variable.

Any other slot (including `stage` and `prod`) or an unsafe directory stops the
server at startup. While active, startup logs a warning and `/healthz` carries
`"test_barrier": true`.

Only mutation ids starting with `barrier-` park. At each point the host
creates `<sha256(artifact|authority|mutation)>.<point>.parked` (`O_EXCL`,
`0600`) and waits for the matching `.release` file, the end of the request
context, or a hard 45 s cap, then removes both files and continues. A release
written before the park is ignored; stale files are removed at startup. The
barrier only adds delay: the commit still checks token, lease, base,
candidate binding, authority and effects, and the barrier never returns
success in place of a commit.

Release and nightly packaging run `scripts/check-no-test-barrier.sh dist`,
and the image's backend build stage runs it on the binaries it builds; both
fail if a Go binary carries the tag. Tagged tests:
`go test -tags crewship_testbarrier ./internal/appengine/ ./internal/server/`.

Harness notes: takeover scenarios must use a different `mutation_id`, since
park files are keyed by artifact, authority and mutation id. Calls through
the agent IPC path have a 30 s write timeout, so park those for less than
that or expect a dropped response instead of a status code.
