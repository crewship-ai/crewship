# Native Slides components

These components consume the admitted schema-1 model in `lib/slides/model.ts`.
They are UI primitives, not a Pages integration, storage adapter or export.
`SlidesEditor` emits unsaved typed candidates through `onChange`; it never claims
a durable save. A future Pages adapter must retain the confirmed Page ID,
revision/order/hash preconditions captured when editing starts, and only report
success after a real durable acknowledgment. Images and comments remain pending.

The defensive boundary accepts parsed data, rejects unknown fields/types and
invalid scalar/geometry/text limits, and consumes shared aggregate table-cell
budgets. Duplicate JSON-key rejection belongs to the server parser. This check
does not substitute for Go admission, authorization or mutation preconditions.

`slide_order` controls display order; `slides[]` storage order is irrelevant.
New/duplicate slides regenerate every slide/element ID. Reorder preserves IDs.
The native React/SVG canvas maps pixel geometry through a responsive viewBox,
clips the frame, and reports measurable text/table overflow to the editor.
There is no author HTML, script, external asset or custom CSS interpreter.

Present reuses the native renderer, handles navigation only inside its modal,
avoids editing targets, traps focus and restores the opener. It is not evidence
of PDF/PPTX parity or production acceptance.

Run through the shared resource wrapper:

```sh
crewship-heavy pnpm exec vitest run components/features/slides/__tests__/native-slides.test.tsx
crewship-heavy pnpm exec playwright test --config=playwright.slides-native.config.ts
```

The browser fixture mounts the real components in an owned Vite server. It uses
synthetic typed data and no API, persistence, credentials, model or deployment.
Choose an owned private `--output` path for screenshots and traces.
