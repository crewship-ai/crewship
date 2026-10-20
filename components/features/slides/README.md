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
exactly as before and no Slides request is made. Loading and saving use
`lib/slides/http-client.ts` against the proposed `GET`/`PATCH
/api/v1/pages/@{id}/slides` routes, which the server does not have yet. A missing
route (non-JSON 404, 405, 501) is shown as "not available on this server yet" and a
failed save keeps the draft; nothing is reported as saved without an acknowledgement
that echoes the mutation id and a newer revision. Saving needs the separate
`NEXT_PUBLIC_SLIDES_EDITOR_SAVE=true`. Browser flow: `e2e/slides-editor.spec.ts`
(flag-on tests need a UI started with both flags and skip otherwise).

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
the proposed save interface behind `NEXT_PUBLIC_SLIDES_EDITOR_SAVE`;
`save-fixture.ts` is an in-memory stand-in used by tests. JSON integers cannot be
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
the reason while they remain in the draft. Save is off unless
`NEXT_PUBLIC_SLIDES_EDITOR_SAVE=true`.
