# Durable document storage

This package stores document state separately from the Page specification.
Pages keep their existing IDs, workspace boundaries and ownership. Documents
require a human-owned Page without existing panels or application source.

The initial storage foundation supports initialization and reading of schema 1
state. It adds no public endpoint, editor, background service or enabled runtime.
Leases, edits, checkpoints and backup integration are delivered separately.

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

The database migration prevents attaching documents to existing panels or
application drafts, and prevents removing a document's human owner. Additional
content types must install reciprocal exclusion checks when their storage is
introduced. Stored receipts remain available for retry verification; the
foundation does not delete them or promise a retention policy.

Tests use the production database migrations and isolated temporary databases.
They cover current authorization, exact retries, conflicting inputs, workspace
scope, persistent reopening, type exclusivity and incomplete replay rejection.
