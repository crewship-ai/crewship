# Slides S0 format comparison

Opt-in reproduction harness for [#3179](https://github.com/crewship-ai/crewship/issues/3179).
This compares two content formats without selecting a production Slides model.
It adds no application dependency, API, Page type, persistence, deployment or
change to the existing HTML, Board or Docs release gates. It is independent of
Docs D1b. Public source and fixtures do not require private context.

## Run

Use Node 24 and pnpm 10.11.0, with Playwright Chromium installed in the usual
browser cache. The nested `pnpm-workspace.yaml` isolates this tool's pinned graph
from the application. On a shared host run all tests through `crewship-heavy`.

```sh
cd tools/slides-s0
crewship-heavy pnpm install --frozen-lockfile
crewship-heavy pnpm test
crewship-heavy env PLAYWRIGHT_BROWSERS_PATH="$HOME/.cache/ms-playwright" \
  pnpm qualify /absolute/path/to/your/own/evidence
```

Default output is ignored `output/`. The harness starts an ephemeral loopback
HTTP editor serving synthetic data, drives actual Chromium controls, creates
two real PPTX files, then closes its browser/server. No shared Crewship server,
Docker, model, paid provider, account or credential is used. Do not point this
harness at an existing server. Generated evidence belongs in private context or
your own temporary path, never tracked public run reports.

For interactive inspection, import `fixture`, `Session`, both adapters and
`startEditor` in a local Node entrypoint; close the returned server afterward.
The editor deliberately has only a first-slide title/text and position form.
The headless semantic path also changes width. This tests direct manipulation
semantics with identical controls, not a polished canvas UI or an AI model.

## Equal candidates

| Concern | A: restricted HTML | B: typed element tree |
| --- | --- | --- |
| Authority | Metadata (`deck` identity/version/theme/assets/notes/order) plus one HTML section per slide | One JSON deck with typed elements |
| Native edit | Parse HTML DOM; replace text and canonical CSS geometry | Replace text/geometry fields in native JSON |
| Normalization | Parse, reject unknown content, canonical serialize | Exact field/type validation; JSON roundtrip |
| Identity | `data-id` on section and each element | Explicit slide/element `id` fields |
| Shared costs | Session preconditions, schema/PNG validation, editor, derived renderer, PPTX mapping | Same |

`Session` stores only the native representation. Projection is transient and
read-only; it is never a second writable document. A projection shared by the
renderer/exporter prevents either format receiving a better export path.
The HTML normalizer deliberately canonicalizes admitted HTML through the same
semantic schema; HTML-specific preservation of incidental CSS order/whitespace
is not promised. The HTML patch path mutates DOM/CSS directly. Both require
whole-deck revision and canonical target hash; stale revisions, even on other
slides, are rejected. Concurrent disjoint-slide merging is **not** qualified.
Reorder has its own order hash and preserves slide/element IDs. Comment anchors
are slide+element IDs, retained after text edits/reorder; they are not text-range
anchors, comment persistence or a product discussion implementation.

The five-slide corpus in `model.mjs` is the same input to each adapter: Czech
text/notes, intentional long text overflow, a two-column table, local PNG plus
rectangle/ellipse, and a deliberately unavailable font. Qualification applies
the same manual edit, targeted agent-like patch and reversal of slide order.
Tests prove stale version/hash rejection without state changes and invalid
geometry rejection. A targeted patch is a semantic test client, not a real
model run or Crewship delegation/authorization proof.

## Exact admitted profile

Dimensions: 960×540 CSS pixels. Deck ≤2 MiB serialized JSON; 1–20 slides;
≤50 elements per slide; 10,000 UTF-16 code units per text/cell/notes; unique IDs
matching `[a-z][a-z0-9-]{0,63}` across deck/slides/elements. Integer CSS-pixel geometry,
positive width/height, nonnegative position, all boxes contained by the slide;
coordinates ≤1920. Text can intentionally overflow its contained box.

HTML source ≤256 KiB per slide; parser tree ≤2000 nodes, depth ≤6 (including
fragment and text). Parse errors, comments, namespaces, unknown fields and
unknown content are rejected before normalization. One `section[data-id]`;
direct `p` plain text, empty `div` shape, `img` asset reference, or `table` with
one `tbody`, rows `tr`, plain-text cells `td`. No nested text markup.

Attributes are exactly `data-id,style` for text/table;
`data-id,style,data-shape` for shapes; `data-id,style,data-asset` for images;
only `data-id` for the section; none for table rows/cells/body. CSS is exactly
`left,top,width,height` with nonnegative decimal `px`, plus
`font-family,font-size,color` for text/table or `background-color` for shapes.
No duplicate CSS declarations. Numeric CSS resolves to integer pixel values; fractional geometry is rejected equally by both adapters. Colors are uppercase six-digit hex. Fonts are
`DejaVu Sans` and the deliberate `S0 Missing Font` fixture, integer size 8–72 CSS px. Text uses LF line endings; CR and other control characters apart from tab/LF are rejected.
Shapes are `rect` or `ellipse`; tables 1–10 equal-length rows and 1–6 columns.
There are no author scripts, handlers, iframe/embed, SVG, URLs, font loads,
CSS imports, classes, arbitrary styles, links, HTML plugins or runtime code.
This deliberately narrow whitelist is an experiment, not general HTML import.

Assets: ≤10 deck-local PNGs, each ≤1 MiB encoded bytes and ≤2,000,000 pixels.
The validator checks PNG signature and bounded dimensions before decoding,
then CRC/decoding and SHA-256. Source only references an asset ID; no URL or
filesystem path is admitted. Images are stretched to the stated box in both
renderers. No JPEG/SVG/animated-image or image-orientation contract is supplied.

The host editor has trusted JavaScript; slide content is in empty-sandbox
iframes with CSP `default-src 'none'`, data PNG only and inline tool-generated
styles. Author content cannot supply scripts or iframe markup. Browser evidence
records attempted external HTTP requests and fails if any are observed; this
is not a general claim of zero DNS/TCP speculation outside the fixture.

## Export contract and unqualified gates

[PptxGenJS text](https://gitbrent.github.io/PptxGenJS/docs/api-text/),
[table](https://gitbrent.github.io/PptxGenJS/docs/api-tables/) and
[image](https://gitbrent.github.io/PptxGenJS/docs/api-images/) APIs are writers;
our `export.mjs` maps geometry, text, tables, PNG, shapes and speaker notes.
CSS px map to inches at 96 dpi and text to points at 72/96. Text and shapes
become native OOXML objects, tables native tables, PNG a native picture. Shape
names retain element IDs and notes carry slide IDs; these are provenance,
**not** a qualified arbitrary PPTX import/ID-roundtrip contract. PptxGenJS may
rewrite application object IDs. The missing font is explicitly substituted
with DejaVu Sans in both browser and export and reported per element. Fonts
are not embedded. Font availability and metrics on other machines can differ.

Every export report lists native text/shapes/tables, source raster images,
font substitutions, browser-measured overflow and rasterized elements. No
supported element is rasterized; the existing PNG is source raster content.
Unsupported elements fail validation rather than being silently screenshot.
Overflow is intentionally preserved (`fit:none`, no automatic pagination).
Browser clipping and Office rendering can differ; screenshot parity between
A/B is not evidence of PPTX visual fidelity.

Qualification writes per-slide browser screenshots and static OOXML structure
checks. **Neither proves opening, layout fidelity or editing in Office.** If
LibreOffice is on PATH (or provided as `S0_LIBREOFFICE`), the harness opens each
PPTX through headless PDF conversion using its own profile. This is an
application parsing/opening check, still not manual editability or PDF fidelity.
PowerPoint opening/editability always remains an explicit external gate.

Before choosing a canonical model, open both exports in LibreOffice and
PowerPoint, edit Czech text/native shapes/table cells, save and reopen, compare
all five slides with browser references, and record overflow/font differences
and any normalization. PDF page geometry, Present, arbitrary PPTX import,
production authorization/storage, collaboration and model-agent acceptance
remain outside this harness. A decision table must distinguish passed, failed
and unperformed gates; no winner is preselected from code size alone.

`report.json` and `decision.md` record native state bytes, source hashes, code bytes, physical lines by module and one
observed manual/semantic operation duration. Shared code costs count equally.
These are reproducibility observations, not performance/capacity measurements.
HTML-specific work includes parser/whitelist/CSS/metadata reconciliation;
typed-specific work includes explicit schema handling. Five-slide results
cannot establish general format or export superiority.

Primary implementation references: [parse5 fragment parser](https://parse5.js.org/functions/parse5.parseFragment.html), [Playwright frame locators](https://playwright.dev/docs/api/class-framelocator), [pngjs decoder](https://github.com/pngjs/pngjs). Pin upgrades require rerunning all profile and browser tests.
