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
- `CREWSHIP_GRIDCRAFT_TEST_BARRIER_SLOT` is `dev1`..`dev4`.

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
which fails if any built Go binary carries the tag. Tagged tests:
`go test -tags crewship_testbarrier ./internal/appengine/ ./internal/server/`.
