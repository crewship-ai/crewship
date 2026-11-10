# Document engine

This package provides the pinned Tiptap and Yjs implementation used to validate
Document content and preserve block identities across edits. It also contains an
optional private Unix-socket service. Adding the package does not enable public
Document endpoints or start that service.

Use Node.js 24 and the package manager declared in `package.json`:

```sh
pnpm --dir tools/docs-service install --frozen-lockfile
pnpm --dir tools/docs-service test
```

On the shared development host, run both commands through `crewship-heavy`.

The schema supports persisted content versions 1, 2 and 3. Version 3 adds links,
inline code and code blocks. Validation rejects unsupported nodes, attributes,
unsafe links and malformed content. The tests cover conversion between semantic
content and Yjs state, stable identifiers, selections and multiple text children.

`schema-core.mjs` accepts its caller's pinned Tiptap and Yjs types. Node callers
use `schema.mjs`; browser consumers must use their own matching types to avoid
mixing separate Yjs installations. Keep dependency versions and the lockfile
together when updating the engine.

## Position history compatibility

The browser and service pin `@tiptap/y-tiptap` 3.0.10 and Yjs 13.6.33 together.
Alongside `default`, validation admits only the upstream position-history map
`__tiptap_position_migrations:root:default`. Its version 1 records contain numeric
client/clock identities, causal vectors and position transfers; references must
resolve to retained document intervals (including payload-free GC intervals).
Unknown fields,
arbitrary roots, nested shared values and nonnumeric payloads remain rejected.
This metadata never supplies permissions or authorizes an agent request.

The same limits include deleted and overwritten retained records: 256 records,
4096 transfers, 128 causal pairs per record, 500000 total transferred positions,
and 64 KiB of JSON metadata per Document, within the existing 1 MiB binary limit.
A limit refuses the update without discarding confirmed content. Metadata is not
silently pruned: history and comment identities must survive an explicit future
compaction contract. Existing snapshots without this map need no rewrite. Deploy the compatible
service before the new browser build. Once new snapshots contain this map, an
older validator will refuse them: a rollback must retain this compatibility
reader even if the editor dependency is downgraded. Do not strip metadata or
rebuild CRDT identities as a rollback shortcut.

`position-migrations-node-test.mjs` covers retained malformed/oversized payloads
and previous-binding snapshots. The actual editor formatting test sends heading,
Enter, list, table, undo and redo updates through the production Unix service with a
controlled Go authority; it does not qualify Go authorization or model authoring.
The separate Dependabot `document-editor` group covers both manifests; its
packages are excluded from the broad dependency groups. Major and security
updates retain the existing separate-review policy.

Upstream format: [3.0.10 release sources](https://registry.npmjs.org/@tiptap/y-tiptap/-/y-tiptap-3.0.10.tgz).
The npm registry `latest` tag was verified as 3.0.10 on 2026-10-21; the manifests
and lockfiles pin the release rather than following that moving tag.

## Canvas surface (not admitted yet)

`surface-core.mjs` defines the Docs Canvas data model of R71 rev 2 §0.4. A Page
block lives in `default`; the canvas only places it. Three extra Y.Map roots of
the same Y.Doc hold plain JSON values:

- `surface:placements`: key = a top-level Page block ID, value `{x,y,w,h,z}`. The
  key is the block ID, so a block has at most one placement (v1). Moving replaces
  geometry only; block content, IDs and hashes do not change.
- `surface:items`: canvas-only notes, shapes and connectors. They are never Page
  blocks. A connector has no geometry: it follows its endpoints.
- `surface:text`: the text of a note or shape, a separate value from its geometry
  so a concurrent move and text edit both survive.

The validator is closed, read-only and bounded (2000 live entries per root, 4 KiB
per value, and every value ever written, deleted ones included, within the 1 MiB
document limit). Under gc:false that is a lifetime budget: the UI writes once per
completed gesture, never per frame or keystroke, and compaction of surface
history is open work. A placement whose block was deleted concurrently is reported
as an orphan, an item whose ID a concurrent block also took as a conflict; neither
is rendered and neither fails the room. Deleting a block removes its placement in
the same transaction. Text left by an edit concurrent with a removal is reported
as a stray and never carries over to a new item with the same ID. Mutators refuse
to write into an invalid surface and refuse a write that would cross the history
cap; after one full walk they add each own write's cost to a cached total, so a
gesture does not re-walk the whole history.

Content schema 6 admits these roots; `validate()` keeps refusing them for
schemas 1-5, and a document becomes schema 6 only by an explicit initialize or
the human-only upgrade. The surface has its own share of the document: its
history count is an upper bound of its encoded bytes (whatever the values,
keys or number of writers) and may not exceed 512 KiB, half of the 1 MiB
document limit, so the Page always keeps the other half. Every commit validates
the merged state, so replicas that each stayed inside the share are refused
together once merged past it: the service answers 413 with code
`surface_quota` (also for the 2000-entry limit) and stores nothing.

Known debt: history is a lifetime budget (gc:false, no compaction yet), so once
the share is full every further Canvas write is refused; the editor treats that
413 like any refused save and keeps the draft for recovery. Undo/redo writes
bypass the client pre-check (the server validation still catches them). After a
remote change the next gesture re-walks the surface once.
`surface-node-test.mjs` covers the model, concurrency, comment anchoring and
undo; `schema-v6-node-test.mjs` the admission and the share.

## Private service boundary

`service.mjs` talks to a separate Go authority over a Unix socket. The service
socket, authority socket and token file must have distinct, clean absolute paths
of at most 100 bytes. Run both processes with the same UID and use private,
dedicated socket directories. The token must be a regular mode-0600 file of at
most 512 bytes; its trimmed content must contain 32–256 bytes.

Readiness requires protocol 3, the current latest schema and confirmation that the authority has
recovered its writer state. Draining rejects new work, finishes accepted queues
and releases held leases. The container packaging uses no network and mounts no
database; persistence and authorization remain with Go.

Admission is bounded to 32 active scheduler keys, 128 total operations and 32 per key.
An incoming key evicts the least recently used idle scheduler tail if necessary;
busy keys are never evicted. Idle reads therefore do not impose a lifetime
32-document capacity. Writer leases remain independently fenced.
Idle cleanup retains its private lease token until Go confirms release, and a
new write waits for an in-flight release. Refused or uncertain cleanup is
revalidated with the next request's current principal; no previous principal's
permissions are reused. An expired or released token can be acquired again
before committing, while authorization refusals still fail closed. The service
reacquires only after an explicit writer fence; other conflicts are not retried.
Tests cover revocation, concurrent release, expiry and lost release responses.
Idle keys unload after 30 seconds. Every operation reloads the Go snapshot and
update log into an isolated Y.Doc, validates it and destroys it afterward.
Bodies are capped at 2 MiB, authority replies at 4 MiB; each authority call has
a 10-second total deadline covering connection, headers and response body.
Continuous response bytes do not extend it; expiry aborts the owned request,
and completion or failure clears the timer. A timeout does not establish whether
a mutation committed: retry the original request unchanged so Go can verify its
retained receipt. This bounds blocked authority calls in the document scheduler
(T-OPS-01); it is not a live worker-replacement acceptance result (T-OPS-03).
No content, bearer or service secret is logged.

The Go listener checks socket inode custody during cleanup. Node removes its
configured socket path when it closes, so do not replace or unlink that path
while the Node service is alive. Dedicated private directories are part of this
service's operating contract.

A killed engine cannot remove its socket. On Linux, startup first takes a
lock: an abstract-namespace listener named from the socket's parent directory
identity (device and inode, so path aliases share one lock) and file name, which
an engine holds while it lives and the kernel releases when it exits. Only the lock
holder removes a socket file that no process accepts connections on, so a
supervised restart at the configured path succeeds and then recovers with a new
boot identity, and concurrent starters cannot remove each other's socket. A
live or paused engine keeps its lock and socket, and any other file at that path
is refused unchanged. The lock is per network namespace: one engine deployment
owns a socket directory. Elsewhere, any existing path is still refused.

Node and image smoke tests use a controlled authority fixture. They qualify the
service protocol and packaging; real Go persistence, managed deployment and
provider-backed document creation require their separate acceptance scenarios.

`cmd/crewship/acceptance_doc_durability_test.go` drives the CLI binary through
the Router and the real Go authority into this service, which it spawns, kills,
pauses and replaces. It covers lost acknowledgements, a crash with requests in
flight, rejection of a stale epoch, lease or generation from a resumed old
engine, and records document size, client count, update rate, engine RSS and
time to recover. It is opt-in and not part of default CI:

```sh
pnpm --dir tools/docs-service install --frozen-lockfile
TEST_DOCUMENT_DURABILITY=1 go test -tags docs_foundation ./cmd/crewship \
  -run '^TestAcceptance_DocumentDurabilityRealEngine$' -count=1
```

`TEST_DOCUMENT_DURABILITY_EVIDENCE` names the measurements JSON file. The test
needs Node.js 24 and records numbers only; it enforces no thresholds.

## Tabs (not admitted yet)

`tabs-core.mjs` is the model for Docs tabs, planned as content schema 7 after
the Canvas surface (schema 6). No content schema admits its roots yet, so every
validator still refuses them as unsupported Yjs roots.

- The first tab is the existing `default` fragment: every older document is a
  one-tab document without migration, and reading tabs writes nothing.
- Further tabs are XmlFragments named `tab:<id>`. Where a tab sits lives in
  the `tabs` map (`{ parent, order }`) and its name in `tab-names`, so a
  concurrent rename and move both survive, and renaming, reordering and
  nesting never move content or detach comment anchors.
- Order keys are fractional strings over `[0-9A-Za-z]` that never end in `0`.
  Two replicas that append at once get equal keys, and a key can grow long
  after many moves to the same place; in both cases the next insert there
  rebalances the siblings' keys in the same write, so an insert never fails.
- Nesting is at most 8 levels. A tab cannot move into itself or its
  descendants; a cycle formed by concurrent moves is broken at its smallest
  ID, and a tab that concurrent moves put below level 8 is shown under its
  ancestor at level 7. Both depend only on the stored state, so every replica
  shows the same tree.
- Deleting a tab removes its content and metadata and moves its children up;
  the first tab cannot be deleted. The empty root a deleted tab leaves behind
  is neither shown nor counted. Content written concurrently into a deleted
  tab stays visible as "Untitled tab" instead of being lost.
- Metadata is closed and bounded, retained values included: 400 tabs at most
  in any stored state, of which mutators create at most 200 (room for
  concurrent creates), 120-character names, and 256 KiB of metadata history
  counted as an upper bound of its encoded bytes. Mutators check every limit
  before writing and stop at three quarters of the history budget, leaving room
  for concurrent replicas; the full validation walk runs once per foreign change.
  A raw update can carry bytes its decoded values do not (an own `__proto__`
  key is dropped by the decoder); the service never stores raw request bytes,
  only the re-encoded document state and diff, so they never reach history.

Admission (schema 7) still needs: the document validator to accept `tab:`
fragments and the tab maps; block IDs unique across all tabs, including
duplicates created concurrently (a new tab refuses a block ID already used in
this replica only); comment anchors that carry the tab and a defined outcome
for anchors in deleted tabs; a Page projection in tree order for outlines,
Markdown and agents; the human-only upgrade; and compaction, because the
metadata history budget is for the document's lifetime.
