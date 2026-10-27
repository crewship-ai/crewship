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

Node and image smoke tests use a controlled authority fixture. They qualify the
service protocol and packaging; real Go persistence, managed deployment and
provider-backed document creation require their separate acceptance scenarios.
