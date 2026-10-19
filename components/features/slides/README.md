# Controlled native Slides editor

These isolated components render schema-1 text, shapes and tables. The wire model
in `lib/slides/model.ts` retains the existing schema-1/2 storage contract. The UI
gate rejects the entire schema-2 or unknown deck before rendering any source text;
image authorization/rendering is not integrated. Stored schema-2 data is unchanged.
This is the native candidate renderer, not a format-selection result.

`PageSlides` supplies a separate read-only saved-content wrapper. Its
`usePageSlides` hook reads an immutable Page ID for the current human/workspace
and open epoch, validates the closed schema-1 response and canonical content hash,
and conceals old content on scope changes, deletion and reconnect. It passes only
verified saved content and its revision to the editor. This wrapper adds no save,
history, restore or agent operation; Page metadata and layout dispatch still need
separate integration. HTTP/realtime-callback tests do not prove a live browser flow.

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
changes must remove old content. The saved wrapper and hook above supply only
read-only admission; this cut adds no API, CLI, Page dispatch, feature activation,
asset retrieval, comments, selected-agent request or export.

The defensive parsed-prop boundary rejects unknown fields/types, invalid scalars,
geometry/text limits and aggregate table-cell budgets. Duplicate JSON-key
rejection remains the Go parser's responsibility. This gate is no authorization.
`slide_order` controls display; storage order is irrelevant. New/duplicate slides
regenerate nested IDs; reorder preserves IDs. React text/SVG geometry and closed
styles avoid author HTML/JS interpretation. Frames clip with measured overflow
warnings. Present scopes navigation, avoids editing targets, traps focus and
restores the opener; its close button remains available.

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
