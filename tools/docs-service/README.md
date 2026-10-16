# Document engine

This package provides the pinned Tiptap and Yjs implementation used to validate
Document content and preserve block identities across edits. It is a library;
adding it does not enable Document endpoints or start a service.

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
