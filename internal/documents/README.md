# Durable document storage

This package stores document state separately from the Page specification.
Pages keep their existing IDs, workspace boundaries and ownership. Documents
require a human-owned Page without existing panels or application source.

The storage foundation supports schemas 1 through 3. Public endpoints and the
editor are delivered separately; the runtime remains disabled by default.
The storage API also supports writer leases, atomic edits, receipt lookup,
pinned checkpoints and explicit restoration. Public endpoint wiring is
delivered separately.

Callers provide an authorization function. Every operation runs that function
inside a transaction after acquiring the document writer fence. Initialization
records the actor and a digest of the original input. An identical retry returns
the existing document only after current authorization succeeds; a different
actor, payload or fingerprint conflicts without changing the stored content.

Reading returns the persisted snapshot and the ordered updates after its
revision. Missing or discontinuous updates fail rather than presenting an
incomplete document as valid. The caller remains responsible for interpreting
and validating the document's CRDT payload; this storage foundation does not
claim that arbitrary binary data is a valid document.

An edit requires the current lease owner, writer identity, epoch and expiry,
plus a matching revision and any supplied generation or block-hash conditions.
Current authorization runs inside the fenced transaction before receipt lookup.
An identical retry returns its original receipt; a changed actor, payload or
precondition conflicts. The request digest excludes lease credentials so a
confirmed edit can be retried after boot fencing with a new lease. Requests
without a generation condition preserve their original digest representation.

A checkpoint pins the snapshot and projection at the confirmed revision.
Restoration creates a new head revision, advances its epoch and generation,
and clears the writer lease. It replaces the replay log while retaining pinned
checkpoints and retry receipts. Checkpoint and restore retries bind the original
generation when supplied; changing that condition conflicts, while requests
without it retain their previous digest representation. Authorization still
runs before retry lookup, including after a writer lease has rotated.

An explicit schema upgrade requires a human's confirmed revision and generation.
It preserves the snapshot, projection and character identities, adds an empty
CRDT update, advances the epoch and generation, and clears the writer lease.
An authorized exact retry returns the original upgrade receipt.

The Go server can enable a private Unix authority with `documents.enabled`,
`authority_socket`, `service_socket` and `service_token_file` configuration.
Paths must be distinct, clean absolute paths. The authority requires the service
credential and a current human session or CLI credential; Page permissions,
credential validity and CLI read/write scope are rechecked in the storage
transaction. These routes are absent from the public HTTP router.

The engine must acknowledge protocol 3 and schema capability 3 before recovery.
An incompatible engine cannot fence a live writer. Recovery of the same boot
identity is idempotent; a replacement engine fences old leases atomically.
Listener startup refuses an existing path before changing leases, and a
configured authority startup failure prevents the server from serving.

Logical storage statistics distinguish the current snapshot, replay updates,
pinned checkpoint bytes and receipt counts. They do not measure SQLite file or
WAL allocation. History limits reject new operations without deleting confirmed
checkpoints or receipts.

Document capacity is scoped to each workspace, with a default of 128 documents.
New commits, checkpoints and restores share a per-document admission window:
by default, 10,000 receipts within ten minutes of server time. A full window
returns `ErrRateLimit`, which also matches `ErrQuota`. Current authorization and
exact retry lookup run first, so an authorized confirmed retry still succeeds
when the window is full. Expiring a window never removes receipts; historical
retry evidence remains available after restart and encrypted backup restoration.

The database migration prevents attaching documents to existing panels or
application drafts, and prevents removing a document's human owner. Additional
content types must install reciprocal exclusion checks when their storage is
introduced. Stored receipts remain available for retry verification; the
foundation does not delete them or promise a retention policy.

Workspace backups include document state, updates, receipts and checkpoints in
dependency order. Binary content uses an explicit base64 representation to
preserve bytes through serialized bundles. Workspace forks remap document and
history anchors together. Workspace restoration and physical instance recovery
clear restored writer credentials and advance epochs and generations so a source writer cannot
resume with its old authority. The process-local writer fence is excluded from
workspace bundles.

Tests use the production database migrations and isolated temporary databases.
They cover current authorization, exact retries, conflicting inputs, workspace
scope, persistent reopening, type exclusivity and incomplete replay rejection.
Backup tests exercise encrypted workspace bundles, forks, physical instance
recovery and rollback after corrupt binary input.
Mutation tests cover concurrent revision checks, lease renewal and release,
boot fencing, immutable transaction guards, exact retries and revoked authority.
Checkpoint and restore tests also cover generation-bound retries, scoped reads,
pinned history, and transaction rollback that preserves the original writer.
