# Routine version authority identity

Immutable routine version storage distinguishes content from executable
authority. `pipeline_versions` keeps its stable row ID and per-routine integer
version. `(pipeline_id, definition_hash, authority_fingerprint)` is unique, so
equal content can have distinct authority identities without accepting a
duplicate content/authority tuple. Integer version uniqueness remains unchanged.

The append-only `pipeline_version_authority_identity` migration preserves every
historical version field, parent chain, integer pin, timestamp and chat origin.
Existing rows receive `legacy-trusted:v1`, also the default for existing writers.
Other fingerprints must be lowercase 64-character hexadecimal digests; NULL,
empty and malformed values are rejected. A row's fingerprint cannot be rewritten.
Fingerprints must use SQLite's text storage type. Digest length is checked in
characters and bytes, rejecting BLOB values and embedded NUL suffixes.

This is storage preparation for
[#3270](https://github.com/crewship-ai/crewship/issues/3270). It adds no publication
endpoint, grant, compiler or runtime activation. A digest proves neither publisher
authority nor execution permission. New bounded writers require integrated
provenance and consumer enforcement before exposure.

The pipeline cascade, chat-origin foreign key and both version indexes remain.
Upgrade tests check the actual pre-migration schema has no incoming foreign key
to the rebuilt child table. Copy failure rolls back the table replacement and
preserves all history and integer pins. Foreign keys remain enabled.

Backup export and ID-remapping preserve discriminator and origin metadata.
Genuine legacy bundles retain normal and forked roundtrip; omitted discriminator
uses the legacy default. Metadata export/remapping tests do not qualify bounded
restore, provider execution or authorization. Qualified bounded ingress remains
a separate prerequisite before a bounded producer is enabled.

Chat origins remain independent, as described by
[routine authority bindings](routine-origin-binding.md). This migration neither
clears nor infers them.

A later append-only `legacy_pipeline_current_heads` cutover archives the known
current physical definition of an existing positive HEAD only when that
routine has no archive rows at all and its stored hash matches SHA256 of the
exact bytes. The archive keeps the existing HEAD integer and chat origin;
HEAD, publication revision, drafts, run/schedule pins and NULL run versions
remain unchanged. No earlier definitions or historical authoring timestamps
are reconstructed: capture uses system upgrade attribution and capture time.
Archive-bearing missing HEADs, inconsistent hashes and head=0 rows are left
untouched. Explicit head=0 compatibility and narrowly proven old-backup
restoration require separate prerequisites before strict HEAD readers ship.
This cutover is never registered as a global restore backfill; an old source
bundle cannot prove unrelated target rows have legacy authority.

Capture also requires native TEXT id/definition/hash, positive INTEGER HEAD and
NULL-or-TEXT origin before scanning. Unsupported BLOB storage remains uncaptured
and source rows, archive absence and integer pins are never normalized or repaired.

The cutover requires exactly one inserted archive and an exact native persisted
row acknowledgement after triggers. Ignored, deleted or changed archive writes
roll back all captures and leave the migration unrecorded for a clean retry.


## Canonical run-admission envelope

The version-one `crewship.routine-run-admission` envelope records exact byte
values, native NULL/TEXT/BLOB distinctions, an immutable selection identity and
the executed-definition projection. Canonical decoding rejects unknown or
duplicate fields, omitted values, alternate encodings, trailing content and
invalid structural bindings. The outer receipt/workspace/routine/run tuple must
match the encoded identity. The caller supplies a positive buffer bound.

This codec validates storage shape only. It performs no SQL or source
authentication, grants no execution authority and creates no admission,
historical receipt or runtime continuation. Runtime writers and readers must
separately establish current authority, exact source state and transaction
ownership before using such data.

### Raw run-admission input boundary

Raw dump restore refuses receipt payloads, unknown reserved admission namespaces,
nonzero or malformed markers, and non-NULL admission references before acquiring
a write connection or invoking callbacks. Existing UNKNOWN0 transport remains
permitted. This input check creates no storage or runtime permission; bundle and
target-history validation require a separate boundary before storage activation.
