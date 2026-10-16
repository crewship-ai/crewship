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

The Go listener checks socket inode custody during cleanup. Node removes its
configured socket path when it closes, so do not replace or unlink that path
while the Node service is alive. Dedicated private directories are part of this
service's operating contract.

Node and image smoke tests use a controlled authority fixture. They qualify the
service protocol and packaging; real Go persistence, managed deployment and
provider-backed document creation require their separate acceptance scenarios.
