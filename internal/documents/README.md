# Durable document storage

This package stores document state separately from the Page specification.
Pages keep their existing IDs, workspace boundaries and ownership. Documents
require a human-owned Page without existing panels or application source.

The initial storage foundation supports initialization and reading of schema 1
state. It adds no public endpoint, editor, background service or enabled runtime.
The storage API also supports writer leases, atomic edits and receipt lookup.
Checkpoint operations and public endpoint wiring are delivered separately.

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
