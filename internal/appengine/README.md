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
restarts. Its managed container restart discards all private candidate scratch.

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
oracle (`check --tolerance 0`) in a fresh process: it recomputes every formula
from scratch and the candidate is rejected unless all formula cells are checked
and match exactly. Volatile functions such as `NOW()` cannot be confirmed, so a
workbook containing them rejects any edit that recalculates another cell. The
receipt's effects list only the mandated target. The host verifies the authored
diff itself but trusts the worker's report and the engine's formula semantics:
the oracle is a second, independent evaluation by the same pinned engine, not a
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
(default 64 MiB) and at most 1000 checkpoints. Reaching it prunes history no
rebase can use (before the newest checkpoint at least 256 revisions back); if
that is not enough, the new revision becomes a checkpoint and all earlier
commands are pruned. Pruned rows keep writer, receipt and result hash and are
marked as snapshot revisions, like rows recorded before command history, so no
mutation is rebased across them.
Receipts require FULL synchronous storage and report `durability: node`.
`Store.Commits` serves the confirmed revisions after a revision (the commits
feed) under the caller's authorization; a range reaching pruned or pre-history
revisions fails with `HistoryGoneError` (`snapshot_required`, with `MinSince`).
`Store.ClaimCommitted` claims undelivered outbox rows once, marking them `sent`;
the host then pushes revision numbers (never content) on the Page channel, and a
host sweeper claims what a crash left `pending`. Presence is not implemented.
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
