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

The preparatory `DesignScene` model validates declarative artboards, ordered
layers, group ancestry, text, geometry, styles, opaque image/font references
and internal prototype links. Every caller supplies positive capacity limits;
these are profile inputs, not measured production guarantees. IDs are unique
across the scene. An element's parent must be a group on the same artboard.
Missing prototype targets produce explicit diagnostics; activation fails and
keeps the current artboard. `DesignPrototype` captures an independent graph,
with navigation and reset held outside the editable scene.

This model has no wire format, persistence, import/export, API or editor, and
does not enable Design operations in the capability registry. Only text,
rectangle, ellipse, image and group elements are supported; unknown kinds fail
without rewriting source. Colors are literal hex values and asset references
are opaque IDs. The host must separately authorize asset/font reads, enforce
revision and audience boundaries, and discard prototype state on revocation
or revision replacement. Validation grants no access. Author scripts and
external navigation have no execution path here. A canonical versioned source
format and roundtrip qualification remain prerequisites for serialization;
these domain tests do not establish end-to-end Design delivery.
