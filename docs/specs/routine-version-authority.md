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
