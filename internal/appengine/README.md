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
Portable workbook backup preserves snapshots and immutable receipts. Fork remaps
artifact anchors while retaining original receipt provenance; copied live run
intents are excluded. Recovery advances generation/token fences before any
new work. A copied source receipt cannot impersonate an operation on a new Page.

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

The engine computes outside the database transaction. The candidate binds the
artifact, base revision, generation, command hash, mandate, pinned engine and
result hash. Concrete cell effects must fit the mandate. The GridCraft adapter
also compares the entire reopened workbook: changes outside the target cell,
including formula recalculation elsewhere, are rejected conservatively. An
engine pin identifies trusted format semantics; isolation alone does not.

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

Public interface and examples: [`docs/cli/workbook.mdx`](../../docs/cli/workbook.mdx).

Tests use migrated real SQLite databases, synthetic fixtures, production Router
and a real CLI binary. Set `CREWSHIP_TEST_GRIDCRAFT_BINARY` to the pinned upstream
CLI to execute the opt-in real-engine contract. That test uses synthetic input
in a local process; it does not attest the deployed sandbox or a live model run.
