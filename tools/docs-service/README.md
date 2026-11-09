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

## Private service boundary

`service.mjs` talks to a separate Go authority over a Unix socket. The service
socket, authority socket and token file must have distinct, clean absolute paths
of at most 100 bytes. Run both processes with the same UID and use private,
dedicated socket directories. The token must be a regular mode-0600 file of at
most 512 bytes; its trimmed content must contain 32–256 bytes.

Readiness requires protocol 3, schema 3 and confirmation that the authority has
recovered its writer state. Draining rejects new work, finishes accepted queues
and releases held leases. The container packaging uses no network and mounts no
database; persistence and authorization remain with Go.

Admission is bounded to 32 active scheduler keys, 128 total operations and 32 per key.
An incoming key evicts the least recently used idle scheduler tail if necessary;
busy keys are never evicted. Idle reads therefore do not impose a lifetime
32-document capacity. Writer leases remain independently fenced.
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
- Further tabs are XmlFragments named `tab:<id>`. Names, nesting and order
  live in one `tabs` map (`{ name, parent, order }`), so renaming, reordering
  and nesting never move content and never detach comment anchors.
- Order keys are fractional strings over `[0-9A-Za-z]` that never end in `0`,
  so a key between any two neighbours always exists. Nesting is at most 8 deep;
  a tab cannot move into itself or its descendants, and a cycle formed by
  concurrent moves is broken the same way on every replica.
- Deleting a tab removes its content and metadata and moves its children up;
  the first tab cannot be deleted. Content written concurrently into a deleted
  tab stays visible as "Untitled tab" instead of being lost.
- Metadata is closed and bounded (200 tabs, 120-character names, 256 KiB of
  retained metadata history), and retained values are validated like live ones.

Admission (schema 7) still needs: the document validator to accept `tab:`
fragments and the `tabs` map, a per-tab Page projection for outlines, Markdown
and agents, block IDs unique across tabs, and the human-only upgrade.
