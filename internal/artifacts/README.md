# Typed Artifact operations

`internal/pages/content_types.json` remains the compiled Go/TypeScript format
registry. This package adds operation availability over that registry; it does
not introduce another format registry or storage model. Stable Page and
workspace identifiers remain the object identity.

`Capabilities(typeID, schema, enabledTypes)` declares create, read, apply,
versions, restore, snapshot and export separately. Every operation has an
explicit supported/available result. Missing type enablement is false. Unknown
types, unknown schemas, unknown operations and formats without an implemented
adapter fail closed through `Check`. Enablement comes from server configuration,
never content or caller-supplied permission flags. Capability availability is
not an ACL grant and must not be used as a substitute for authorization.

The first adapter is `DocumentAdapter`, constructed with an existing
`documents.Store` and the server's Document enablement. It retains the typed
Document create and commit payloads, generation/revision conditions, writer
lease, immutable mutation guards, idempotency fingerprints and receipts.
Snapshots are pinned checkpoints; versions lists those checkpoints, not every
CRDT update. Restore creates a new head through the existing store protocol.
Reads reject unknown stored schemas without returning their content.

Every supported method requires `documents.Authorize`. The store calls it
inside its fenced Go transaction, including exact retries and version reads.
The API still supplies live session/run, workspace, ownership and access checks;
the Docs engine validates CRDT payloads without gaining database or ACL authority.
Engine recovery, lease control, receipt lookup and schema upgrades retain their
existing Document protocol and are not generic Artifact mutations.

The registry's export format metadata describes a format, not an available
export execution path. Generic Document export returns `ErrUnsupported` from
`Check`; there is no export success stub or new export service. Other compiled
content formats keep their existing entrypoints and are explicitly unsupported
by this operation adapter until their own implementations are added.

Tests exercise declarations against every compiled format and a migrated real
SQLite database: create, commit, checkpoint, version listing, restore, exact
retry, revoked authorization on every operation and unsupported stored schemas.
