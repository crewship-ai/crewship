# Template Crewship Design (mirror `nwpv`)

- **Original:** https://claude.ai/artifact/NwPV8rqSjHWDtpZPYUb2ko, a claude.ai
  **Design (canvas)** artifact, version `1794089214-dd97`, read on 2026-11-08.
  It is not a Claude Docs document: it is a canvas of 13 HTML artboards
  (`.dc.html` Design Components) on 5 canvas pages, with 4 page titles and
  4 sticky notes. It has **no comment threads**.
- **Seeded as:** slug `ref-design-template`, name `Template Crewship Design`,
  through `POST /api/v1/pages/artifacts` (the route `crewship page artifact create`
  uses), registered in `seeddata/mirror_nwpv.go`. The UI shows it as **App · Static**.

## Kind mapping

| Original | Crewship |
| --- | --- |
| Design canvas | App project (multi-file HTML, static: no `interactive.json`, no data) |
| 13 artboards `project/<Name>.dc.html` | 13 screens `<Name>.html`, the original source |
| canvas runtime `support.js` | `dc.js` (source `_dc.js`), a small stand-in |
| Google Fonts (IBM Plex Sans, JetBrains Mono) | `fonts.css` (source `_fonts.css`), the same fonts embedded as `data:` woff2 |
| canvas view: pages, frames, titles, sticky notes | `index.html`, the entry screen: a scaled map of each page plus a list of its artboards |

Why not Crewship's native Design canvas: it stores `frame`, `rect`, `text` and
`image` layers with a position, a size and one fill (`internal/design`). It has
no radius, borders, font family, size or weight, flex layout, SVG or
behaviour. Redrawing these artboards as such layers would change every one of
them. An App keeps the artboards' own HTML and CSS, so the text, layout and
colours stay the same and the clickable prototypes still work.

## Conversion (`_build.py` → `m-nwpv.json`)

`_original/project/` holds the 14 files exactly as claude.ai returned them
(sha256 pinned in `TestMirrorNwpv_OriginalsArePinned`). Each artboard changes
only in these ways, and `TestMirrorNwpv_ArtboardsAreTheOriginals` re-applies
the same steps and compares the bytes:

1. `<script src="./support.js"></script>` becomes
   `<link rel="stylesheet" href="fonts.css">` + `<script src="dc.js"></script>`.
2. The one `<link … fonts.googleapis.com …>` line in `<helmet>` is removed.
   The artifact frame allows fonts from `data:` only, so `fonts.css` carries
   the same families instead.
3. File names drop `.dc` (`Main.dc.html` → `Main.html`), and the two
   prototype links (`Main` ↔ `Detail`) are renamed to match. The screen menu
   names a screen by its file name, because the original `<title>`s are too
   long to be used as tab labels.

`dc.js` handles everything these artboards use: `<x-dc>`/`<helmet>`,
`{{ dotted.lookup }}` in text and attributes, `<sc-for>`, `<sc-if>`,
`onClick`/`onChange`, boolean and value attributes, and
`class Component extends DCLogic` with `data-props` defaults and `setState`.
It patches the DOM in place, so focus and caret survive typing. It runs the
component script as an inline script, not with `eval`.

Regenerate with `python3 -I _build.py` in this folder. Underscore files are
not embedded in the binary.

## Checked

`/mnt/dev-workspace/tmp/builds/mirror-nwpv/shots/` has screenshots of every
screen, taken in Chromium. Each screen was served the way the static App
frame serves it: project CSS/JS inlined, under the Studio UI
Content-Security-Policy. Results: 0 console errors, no unfilled `{{ }}`, both
fonts load, and the overview has no sideways page scroll at 390 px. The
prototype interactions also work: Main tab switch, Detail approve,
Onboarding steps, Form validation and submit, Web pricing toggle and FAQ, and
the Tablet crew picker.

## Gaps (not faked)

- **Canvas editing.** There is no infinite canvas, and frames cannot be moved,
  resized or edited on a canvas. The overview is a static scaled map, and its
  frames are labelled boxes, not live thumbnails: the frame blocks nested
  frames, and `index.html` cannot load the other screens.
- **Tweaks.** Crewship has no properties panel, so `data-props` controls
  (accent colour; `dark` on Desktop) render with their defaults only. The
  sticky notes that mention Tweaks describe a control this copy does not have.
- **Flow artboard.** In the original, boxes and arrows can be dragged and
  texts edited in place. Here the artboard is static.
- **Fluid pages** (`expand: "fill"`: Desktop, Web, Wireframe) fill the App
  frame's width, which matches the original's full-window mode. Fixed
  artboards render at their own pixel size.
- **Comments.** The original has none. Crewship App comments anchor to an
  element `#id`. The artboards have no ids, so element comments on these
  screens have no anchor to select.
- **External links** (`https://crewship.example` in Email) do not navigate,
  because the frame never leaves for another URL. Hash links work.
