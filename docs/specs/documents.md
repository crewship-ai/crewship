# Docs persistence foundation

Status: implemented D1a foundation for review in #3161, disabled by default.
This is the Go/Node persistence contract for contributors and operators; it does
not ship a public editor, public document routes, CLI or authoring skill.
The [Pages contract](pages.md) continues to own Page permissions and identity.
[D0](../../tools/docs-d0/README.md) records the prototype and its limitations.

{/* docs-inventory: ignore-api-prefix /internal/docs/ reason=private-Unix-service-protocol-not-public-HTTP */}

## Activation and process boundary

`documents.enabled` defaults to false. Explicit configuration requires distinct
clean absolute `authority_socket`, `service_socket` and `service_token_file`
paths (at most 100 bytes). The token is a regular mode-0600 file, 32–256
characters. Go binds only the private authority Unix socket; it never mounts
these handlers on its public Router. Node is a separately managed process, with
no database mount, no public listener and no runtime package installation.
The [service package](../../tools/docs-service/README.md) supplies frozen
dependencies, Docker build source, health checks and an optional Compose profile.
Deployment on the shared host remains an Ansible change, not part of this PR.

Both processes need the same numeric UID for mode-0600 sockets and secrets.
The container defaults to UID 1001; the sample profile requires explicit UID/GID
alignment. Its image must be qualified and supplied explicitly. A failed bind
does not fence another live authority. Boot fencing runs after socket ownership
is established and before requests are accepted. A configured authority startup
failure prevents the server from starting.

## Write and identity contract

Node validates the visible **and retained** schema-1 Yjs structures, including
deleted content. Tiptap 3.32.0, Yjs 13.6.33 and `@tiptap/y-tiptap` 3.0.7 are pinned
together. Unknown metadata, nodes, marks and unresolved CRDT dependencies fail
closed. Binary state is authoritative; JSON is a projection.

Each operation is serialized per workspace/Page and uses a bounded queue. Node
calls the private Go authority with service authentication and the original
human or run bearer. Go takes the SQLite writer fence, rechecks exact session,
membership, Page ACL or existing live run delegation, then applies epoch/lease,
revision, target hash and idempotency checks. The stored actor comes from this
authorization, never the payload. Agent raw CRDT updates are rejected.

The durable update, projection and receipt/audit commit together. Node returns
success only after Go confirms; D1a has no speculative peer broadcast or editor
transport. Original operation fingerprints support retries after lost replies;
different payloads or actors cannot reuse a receipt. Init/checkpoint/restore are
also idempotent. Writer tokens expire after 30 seconds; acquisition advances
the fence epoch. The D1a service releases a lease after each operation, so its
acknowledgements can advance epochs without changing content. D1b must define
the longer-lived editor transport rather than assume a stable per-session epoch.

## History and limits

`documents` stores the baseline snapshot and current projection.
`document_updates` stores ordered incremental updates after its watermark.
`document_receipts` retains audit and retry identities; `document_checkpoints`
pins explicit complete states. These are separate from HTML revision tables.
Replay coverage must be contiguous. Every 32 updates or 64 KiB, one transaction
advances the baseline and removes only covered replay updates. This does not
delete receipts, checkpoints or their Yjs tombstones.

Default instance/document bounds are 128 documents, 1 MiB snapshot plus
projection, 256 KiB live replay log, 10,000 operation receipts per document and
16 MiB pinned checkpoint data per document. The Node process admits at most 32
document queues, 32 queued operations per document and 128 globally, and unloads
idle queue state after 30 seconds. Quota errors preserve confirmed data and
reject the affected operation; they do not silently prune promised history.
`gc:false` is retained. Physical GC, arbitrary historical keystroke replay,
assets, common revision UI and expanded retention remain separate work.

Documents attach only to empty human-owned Pages. Database constraints reject
coexistence with panels, HTML, Board or project drafts. Page ownership departure
requires an explicit eligible human successor; transfer keeps the same Page ID.

## Recovery and evidence

Workspace/fork and physical instance backups include all four document tables.
Binary columns use an explicit base64 representation, preserving invalid UTF-8.
Restore clears saved writer tokens and advances epochs; existing target rows
are also fenced. The runtime singleton is excluded from workspace backups.
Confirmed updates, checkpoints, projections and audit survive actual bundle
roundtrips. Old run credentials do not become newly authorized by restoring data.

Tests cover actual Node and Go SIGKILL/restart, durable replay, exact mutation
retries, six documents and 40 edits on 200 paragraphs across compaction, stale
writer fencing, schema mismatch, live delegation
and exact-session revocation, and SQL writer contention. The contention fixture
holds a real SQL writer for 50 ms; it is not a measurement on slower hardware.
Power loss, a qualified container image, multi-replica failover and production
capacity are not established by these tests. No dev environment is deployed or
reset. The [store contract](../../internal/documents/README.md) and service README
document reproduction, quotas and the still-unqualified boundaries.
