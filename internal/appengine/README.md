# Experimental offline workbook operations

This module prepares GridCraft workbook candidates and commits them through Go.
The server runtime and agent tools are disabled by default. It is a narrow pilot,
not a collaborative editor or a dynamically installed application platform.

The compiled Artifact format is `gridcraft-workbook`, schema 1, with execution
profile `headless-worker/v1`. Native JSON imports and `cell.set` on the active
sheet are implemented. Embedded UI, arbitrary engine commands, client sync,
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
target cell also requires a report with no mismatches. Volatile functions such
as `NOW()` cannot be confirmed, and with iterative calculation enabled the
oracle accepts any fixed point of a cycle, so such workbooks reject any edit
that recalculates another cell. The receipt's effects list only the mandated
target; the audit record adds the result digest, the recalculated cell count
(and up to 100 addresses) and the report. The host verifies the authored diff
itself but trusts the worker's report and the engine's formula semantics: the
oracle is a second, independent evaluation by the same pinned engine, not a
different implementation. An engine pin identifies trusted format semantics;
isolation alone does not.

Go atomically stores snapshot, revision, immutable receipt, audit and outbox.
Receipts require FULL synchronous storage and report `durability: node`.
Delivery remains `pending`: no live broadcast/presence transport is installed.
No receipt detail or replay fence is deleted by this module. A fresh candidate
always starts from the host's confirmed snapshot; no engine promotion is needed.

`gridcraft_intents` retains one explicit human instruction for a particular live
run, Page revision/generation and target. It is not an ACL. The existing signed
run capability, human chat principal, membership epochs, crew policy, ownership,
Page version and private audience are checked transactionally on every use.
The private chat is sealed before agent access. A revoked intent cannot be
renewed, and a new run does not inherit it. Human edits are imports only.

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
