# WordCraft C-E acceptance (written before product integration)

This is an active, explicitly selected acceptance suite. It has no server launcher,
no localhost default, no mock engine, no retries and no skips. Missing C1 integration,
authentication, native state, renderer evidence, crop configuration or transport
produces failures. Nothing enables a product feature or modifies the legacy Yjs path.

The suite **edits its configured fresh disposable documents through normal UI**. Each feature starts with exactly one empty paragraph; the suite never resets native state through an injected hook.
Use a newly created, shared, plain WordCraft command document with default paragraph
formatting, owned by the test participants. Do not use a real working document.
Both existing participants need writer access. Two independent browser contexts use
two independently authenticated storage-state files; equal server principal IDs fail.
No credentials, storage-state contents or opaque view handles are written to results.

## Run

Install locked project dependencies and the Playwright Chromium browser. Configure:

| Variable | Meaning |
| --- | --- |
| `WORDCRAFT_CE_BASE_URL` | Explicit origin of the dedicated test server |
| `WORDCRAFT_CE_DOCUMENT_PATH` | Same-origin path of the disposable command document |
| `WORDCRAFT_CE_STATE_PATH` | Same-origin `/api/v1/pages/%40SLUG/mooring/command-session?workspace_id=ID`; the observer uses actual UI submit requests, and independent state reads use existing read-only open/read actions |
| `WORDCRAFT_CE_PRODUCT_SHA` | Exact 40-character deployed product SHA; version API must match before navigation/mutation |
| `WORDCRAFT_CE_DOCUMENTS` | Optional JSON feature ID → `{path,statePath}`; use separate fresh documents for all eleven features and `latency` when running the whole suite |
| `WORDCRAFT_CE_NATIVE_ACTIONS` | JSON feature ID → calibrated native UI action list: `{click:{x,y}}` in canvas coordinates, `{press:"Control+a"}` or `{type:"Arial"}`. Required for parameter/ribbon actions without a real shortcut; no engine or DOM hook fallback |
| `WORDCRAFT_CE_AUTH_A`, `WORDCRAFT_CE_AUTH_B` | Paths to storage-state files for different authenticated writers |
| `WORDCRAFT_CE_FEATURE_CROPS` | JSON feature → `{writer,peer}` rectangles, relative to each native canvas |
| `WORDCRAFT_CE_LATENCY_CROPS` | JSON `{writer,peer}` rectangles in page coordinates, for the blank/`W` glyph oracle |
| `WORDCRAFT_CE_LATENCY_SAMPLES` | Measurement count, default 30, range 1–1000 |
| `WORDCRAFT_CE_OUTPUT_DIR` | Results directory, default `test-results/wordcraft-ce` |

Each rectangle is `{x,y,width,height}` in CSS pixels. Choose document glyph regions
that exclude caret, selection, remote presence, ribbon, status text and animations.
There is deliberately no whole-canvas fallback. Both contexts use identical viewport,
scale and color scheme. Feature crop sizes must match, and every crop must lie inside
the actual native canvas. Reference stability checks catch some unstable pixels;
they do not automatically mask or prove the absence of a canvas caret. Set crops
from the actual rendered fixture and inspect their captures.

For example, export the variables above in your own runner, then run:

```sh
crewship-heavy env WORDCRAFT_CE_BASE_URL="$WORDCRAFT_CE_BASE_URL" \
  WORDCRAFT_CE_PRODUCT_SHA="$WORDCRAFT_CE_PRODUCT_SHA" \
  WORDCRAFT_CE_DOCUMENT_PATH="$WORDCRAFT_CE_DOCUMENT_PATH" \
  WORDCRAFT_CE_STATE_PATH="$WORDCRAFT_CE_STATE_PATH" \
  WORDCRAFT_CE_AUTH_A="$WORDCRAFT_CE_AUTH_A" WORDCRAFT_CE_AUTH_B="$WORDCRAFT_CE_AUTH_B" \
  WORDCRAFT_CE_FEATURE_CROPS="$WORDCRAFT_CE_FEATURE_CROPS" \
  WORDCRAFT_CE_LATENCY_CROPS="$WORDCRAFT_CE_LATENCY_CROPS" \
  WORDCRAFT_CE_OUTPUT_DIR="$WORDCRAFT_CE_OUTPUT_DIR" \
  pnpm exec playwright test --config playwright.wordcraft-ce.config.ts
```

Use the same environment prefix with `bash scripts/measure-wordcraft-ce.sh` to run
only the three latency measurements. Optional `PLAYWRIGHT_BROWSERS_PATH` must point
to the installed browser cache when the runner has a private cache. Storage states
are input files; this suite does not log in as a guessed account or create authority.

Run the offline assertion, coverage, percentile, HTTP correlation and passive-observer tests with:

```sh
crewship-heavy pnpm exec vitest run --config tools/wordcraft-ce/vitest.config.ts
```

## Actual product observations

The adapter follows the existing C1 HTTP command-session product path. It adds no
server route or product test hook. Engine SHA comes from independent confirmed
server reads, and an exact product commit check precedes any UI navigation.

- The real accessible host is `section[aria-label$=" command editor"]`, with its
  `iframe[title="WordCraft command editor"]` and native `#wordcraft_canvas`.
  The actual role status says `Saved` after a committed operation, and `No unsaved
  changes` after startup/reload. Labels alone never qualify a save.
- Passive Page request/response listeners observe existing `action:"submit"`
  HTTP requests. The exact original raw string is SHA-256 bound to the response,
  along with actor, generation, base+1 revision, native selection and a
  `state:"host_committed"` receipt. Another request, generic error, optimistic
  state or different original cannot pass. Opaque view handles stay private.
- Existing authenticated `open(write:false)` and `read(view)` actions provide the
  independent confirmed SQL projection. They never submit, create, retry outcome
  or replay commands. The two authenticated principal IDs must differ.
- A test-only MessageChannel constructor wrapper adds only a passive message
  listener to parent-owned ports. It never posts, starts or replays a message,
  calls WASM or changes product state. Real `wordcraft-command:applied` messages
  prove native queue consumption **only**; they are not GPU paint acknowledgments.
  Stable glyph crops remain the separate actual rendering oracle. Ambiguous
  channels, overflowing observations or missing queue messages fail closed.
- Native egui parameter controls are canvas UI, not synthetic DOM textboxes.
  Calibrate the explicit click/key input map against the actual native ribbon.
  The journey independently requires the exact resulting command target, base,
  parameters and persisted native effect, so clicks alone do not qualify a stage.
  Text, Enter, bold and italic use actual keyboard input. Seed and peer text wait
  for each real committed operation to avoid input loss under serialized C1.

The original WebSocket parser remains covered as a separate proposed transport
parser; active product journeys and latency use the actual HTTP observer. Nothing
maps missing APIs, authentication, disabled undo, startup failure or unqualified
commands to success. The existing native registry alias `edit.undo` maps to the
actual wire command `participant.undo`; that exact wire command is observed after
the real Control+z input. A release without participant undo is expected to fail the
undo stage (and the latency oracle reset), even when earlier stages succeed.

## What the scenarios prove when executed successfully

Eleven scenarios cover text, Enter, font family, font size, foreground and highlight
colors, bold, italic, paragraph alignment, bullets and numbering. Each selects a
known ASCII range and checks actual command target, base revision and parameters.
Native runs must cover the selected bytes; character formatting must preserve every
byte's properties outside the selection. Lists must reference the correct persisted
list and level definition, not merely contain an unrelated list somewhere.

Each scenario then checks durable Saved for that operation, native queue consumption and
identical committed state at B, plus stable document glyph pixels changed from B's
baseline and equal to A. Reload preserves native state and the same raster result.
B appends an independent paragraph; A's undo after reload must restore exactly A's
original body plus B's entire unchanged contribution, including every referenced
list definition. Both editors must paint the undo revision. This is a C-E acceptance
boundary, not a declaration of general collaborative conflict/rebase correctness.

`coverage.json` and `coverage.md` map every expected feature into UI → engine →
Saved → other clients → reload → undo. Missing/filtered scenarios remain in the
full denominator as `not-executed`/`not-reached`; `expected`, `executed` and
`coverageComplete` distinguish partial runs. A completed early stage does not mean
later stages passed. Browser launch failure cannot create passing stage evidence.

## Three distinct latency distributions

The measurement runner first creates a confirmed UI `W` reference at A and B.
Both crops must visibly differ from confirmed blank and remain stable across a
second capture. Each sample types a real `W`, observes matching pixels separately
at A and B, correlates the exact sent command with its durable receipt, and restores
blank through confirmed UI participant undo. Setup and undo are outside timing.

`latency.json` reports separate `keyToWriterPixels`, `sentToDurableSaved` and
`changeToPeerPixels` distributions: unit ms, nearest-rank p50/p95/max, count and raw
samples, plus original operation/revision and observer timestamps. One runner's
`performance.now()` is the common clock. Key origin is UI keyboard dispatch;
image completion and HTTP response observation are **observation upper bounds**, including
input dispatch, screenshot/observer overhead and polling resolution. They are not
exact compositor or storage commit instants. Network/API state is never counted
as visibility. Oracle hashes and crop coordinates make the raster evidence auditable.

An unsuccessful measurement writes `measured:false`, an error and `metrics:null`;
it never fabricates latency numbers. No live latency or C-E completion is implied
by passing offline percentile/parser tests. Record server/build SHA, browser,
document, machine, network, participant count and duration alongside live results.
