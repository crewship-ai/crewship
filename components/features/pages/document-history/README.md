# Read-only Document checkpoint history

This controlled component displays immutable Document history. The separate
human restore control requires explicit confirmation and current write access;
it never restores original run authority. Production route registration and
real browser acceptance must qualify the composed cut before delivery.

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
agent authority. Restore uses a separate original Page/head/epoch/checkpoint
packet, requires confirmation again if that base changes, and never runs while
a draft is pending or awaiting recovery. An uncertain response locks the editor
and retains the exact packet for retry. Only a validated receipt allows a clean
editor remount; intervening local changes instead require explicit recovery.

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

## Compare, and revert (flagged)

With the `review` prop the history offers **Compare with saved version** (block
diff against the last confirmed head, `lib/documents/block-diff.ts`). Compare is
not flagged: it reads only the checkpoint endpoints on main. It is disabled with
"Loading current document…" until the head content exists, and with unsent
edits it says they are not included. When `review.actions` is supplied (writers
only, and only behind `NEXT_PUBLIC_DOCUMENT_REVIEW_ACTIONS`), **Revert this
change** is offered, a separate action from restore. Revert is disabled while
unsent edits exist, sends the head revision the user reviewed and one request ID
per decision, and reports network and 5xx failures as an unknown outcome. The
HTTP client (`lib/documents/history-actions.ts`) targets a proposed endpoint.
Revert tests use fixtures.
