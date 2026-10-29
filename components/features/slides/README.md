# Controlled native Slides editor

These isolated components render schema-1 text, shapes and tables. The wire model
in `lib/slides/model.ts` retains the existing schema-1/2 storage contract. The UI
gate rejects the entire schema-2 or unknown deck before rendering any source text;
image authorization/rendering is not integrated. Stored schema-2 data is unchanged.
This is the native candidate renderer, not a format-selection result.

`deck` is the caller-owned displayed candidate. `onChange(candidate)` emits a
validated unsaved draft. Optional `onSave(candidate)` only requests persistence;
it performs no request and cannot establish a successful save. `readOnly` or
`disabled` blocks mutations and save, while `saveDisabled` blocks only save.
`persistenceStatus` comes from the trusted caller and defaults to "Changes in this
preview are not saved." Without `onChange`, the body is read-only.

The parent must retain confirmed workspace/Page identity, revision, order version,
hash and saved deck separately from the candidate; authorize each real read/write;
bind save preconditions to that saved base; and verify acknowledgement and the
exact saved revision before replacing the candidate or reporting success. Scope
changes must remove old content. This cut adds no Page wrapper, API, CLI, hook,
feature activation, asset retrieval, comments, selected-agent request or export.

The defensive parsed-prop boundary rejects unknown fields/types, invalid scalars,
geometry/text limits and aggregate table-cell budgets. Duplicate JSON-key
rejection remains the Go parser's responsibility. This gate is no authorization.
`slide_order` controls display; storage order is irrelevant. New/duplicate slides
regenerate nested IDs; reorder preserves IDs. React text/SVG geometry and closed
styles avoid author HTML/JS interpretation. Frames clip with measured overflow
warnings. Present scopes navigation, avoids editing targets, traps focus and
restores the opener; its close button remains available.

Mounting: with `NEXT_PUBLIC_SLIDES_EDITOR=true`, `/pages/<slug>` renders
`SlidesWorkspace` (through `components/features/pages/page-slides.tsx`) for a Page
whose wire record carries `has_slides: true`. Off by default; the Page then renders
exactly as before and no Slides request is made.

Backend contract (decision F5-7, public PR #3837, behind `CREWSHIP_SLIDES_ENABLED`):
`lib/slides/http-client.ts` speaks `GET /api/v1/pages/@{id}/slides` and
`POST /api/v1/pages/@{id}/slides/initialize`, and nothing else. The read body is
exactly `{schema_version, revision, order_version, content_hash, deck}`; there is no
`page_id` and no `rev`. Identity is `deck.deck_id`, which must equal the requested
Page ID, and a mismatch is an error state. Initialize takes exactly
`{page_id, schema_version, base_rev: 0, mutation_id, deck}` and answers
`{mutation_id, revision, order_version, content_hash, duplicate}`. Both bodies must
have all of those keys (unknown extra keys are ignored, since the server's strict
decoder applies to requests only; the deck stays strict). The receipt must echo the
request's `mutation_id` and carry the hash of the deck that was sent, and a 2xx that is
not a verifiable receipt is never reported as saved. A missing route (a plain-text 404 when the
flag is off, because the routes are not installed; 405, 501) is shown as "not available on this server yet".

There is no PATCH, order or agent route yet, so editing is a local draft: the status
line says so, there is no Save button, and leaving with edits asks the browser to
confirm. The status words and the leave prompt come from the shared editor lifecycle
(`lib/editor-lifecycle`, also used by Document): a draft is "not stored in this browser
or on the server", and "Saved" appears only after a receipt echoing the sent `mutation_id`. `SlidesWorkspace` can drive the proposed save contract only when a caller
passes `saveEnabled` (tests, with the in-memory fixture); no environment flag
enables it, and `patch` on the HTTP client sends nothing. Browser flow:
`e2e/slides-editor.spec.ts` (flag-on tests need a UI started with
`NEXT_PUBLIC_SLIDES_EDITOR=true` and skip otherwise). `lib/slides/__tests__/real-server-capture.json`
holds bodies recorded from a server built from #3837.

Source provenance: existing editor/Present extraction #3206 commit
`3ce3173e4b26882151d68cac171f70e3c916b500` and unused-import fix `6c831db30`,
with schema-1/2 model from #3229 `f0a756d7151b2aa6431fb295416fa3056c1627f7`.
The image union has an explicit unreachable renderer rejection after the whole
deck gate, and authoring cannot create image elements. No model is redefined.

Focused verification:

```sh
crewship-heavy pnpm exec vitest run components/features/slides/__tests__/native-slides.test.tsx
```

The authored component tests use the canonical synthetic fixture. Browser,
saved-Page/API, actual-model and Office/PPTX acceptance are separate unexecuted
gates in this cut; no source-branch execution is claimed as current validation.

## Operation layer (`lib/slides/operations.ts`)

Pure client mirror of `internal/slides/operations.go`: `decodePatch`,
`decodeReorder`, `decodeInitialize`, `applyPatch` (atomic, no aliasing between
request, state and result), slide/element/deck hashes equal to the Go golden
values, and `diffToPatch`, which turns a local draft into a sequentially valid
operation batch. Changes the wire contract cannot express (element z-order,
slide reorder, theme/size) are reported as `unrepresentable` and never dropped;
the save button must stay disabled for them. `lib/slides/save-client.ts` defines
the client interface (the read and initialize it names are served; `patch` is a
proposed contract); `save-fixture.ts` is an in-memory stand-in used by tests. JSON integers cannot be
told from `24.0` in JavaScript, so that one Go rejection is not mirrored.

## Editing interactions

`SlidesEditor` selects with click or Enter/Space (Shift adds or removes), drags to
move, resizes with the corner handle (single selection), and supports: arrows
nudge by 1 (Shift 10, Alt resizes), Delete/Backspace remove, Escape clears,
Ctrl/Cmd+Z undo, Ctrl/Cmd+Shift+Z or Ctrl+Y redo (not while typing in a field).
Layer commands act on the selection as a block. Undo history is local only: a deck
the editor did not emit (reload after a conflict) clears it, so undo never
reverts someone else's confirmed change. Elements are focusable with a visible
dashed focus ring. Below 768 px the editor is a read-only preview (Present stays).

`SlidesWorkspace` owns the saved snapshot, the draft and the loading, not
available, error (retry), saving, conflict (load latest or keep editing locally,
never overwritten) and offline (draft kept, same mutation id on retry) states. It
saves through `diffToPatch` and a `SlidesSaveClient`; layer order, slide order and
theme edits cannot be expressed by the current operations, so save is blocked with
the reason while they remain in the draft. Save is off unless the caller passes
`saveEnabled`; the Pages route never does.
