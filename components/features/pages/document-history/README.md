# Read-only Document checkpoint history

This controlled component prepares a native Document history surface. It has
no network adapter, mutation, restore control, live editor reference or original
run authority. It is incomplete until real current-authority endpoints, CLI,
editor integration and browser acceptance are supplied.

The retained checkpoints list is a bounded set of confirmed pins, not every
revision. `internal/documents/checkpoint_list.go` returns `id`, `rev`, `schema`,
`actor` and `created_at`, ordered by revision and ID descending, limited to 1000.
`Store.CheckpointRead` reads the selected immutable pin. The private Documents
service decodes its Yjs snapshot into validated content; those transport bytes
and the original write lease must not reach this component.

Public wire proposal (coordinated separately; not implemented by this slice):

```ts
// GET current-human authorized list
{ checkpoints: [{ id, rev, schema, actor, created_at }] }
// GET current-human authorized immutable selected pin
{ page_id, id, rev, schema, content }
```

`types.ts` declares that projection and the controlled props. The caller must
reauthorize each request and scope responses to current human, workspace,
immutable Page ID and authority generation. Give `scopeKey` that identity,
change it immediately on identity/authority changes, and set denied/loading
before rendering stale protected responses. A snapshot must match the selected
listed checkpoint's ID, revision, schema and Page identity to render. Titles
are never guessed; actor is shown only as authorized checkpoint metadata.

Mount history beside the existing live editor. Keep that editor mounted with
its stable identity, content and pending draft. `onSelect` requests only a
checkpoint read; `onClose` hides history without rehydrating or replacing the
editor. Choosing a pin never implies restore, save, edit permission or renewed
agent authority. A future restore requires a separate real endpoint contract
and explicit draft/authority handling.

The read-only preview uses the existing closed Document schema 2/3 validator
and ProseMirror serializer. Unsupported schemas or invalid content show
unavailable; there is no fallback conversion to saveable text. Failed reads
remain distinct from a confirmed empty list. Access denial and mismatched
scope remove the preview rather than retaining stale content.

Focused component checks:

```sh
crewship-heavy pnpm exec vitest run components/features/pages/document-history/document-history.test.tsx
```

These tests use explicit props fixtures. They do not certify real endpoints,
CLI behavior, a live browser, complete history/compare/restore or access-audit
acceptance. Existing panel/application history controls have different mutation
semantics and are intentionally not reused as Document restore behavior.

The confirmed snapshot body reuses `../document-readonly/DocumentReadonly` after
the exact selected checkpoint and current scope checks. That shared reader rejects
non-Document roots and clears prior DOM if serialization fails. Browsing still
does not create a live editor or restore the original run authority.
