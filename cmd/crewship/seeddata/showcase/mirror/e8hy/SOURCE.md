# Template Crewship Showcase (`ref-crewship-showcase`)

- **Original:** https://claude.ai/artifact/E8HYX1pSVKSAbLqgvTCZ8Z (version
  `1794069218-3a10`, read 2026-11-08). A plain HTML artifact: one page, no
  published supporting files, no runtime capabilities, not created from an
  Artifact type.
- **Content:** one page with five tabs (`#model-3d` three.js ship on waves,
  `#diagram` Mermaid flowchart, `#barvy` colour and type pairing with a WCAG
  contrast readout, `#filtry` task search and filter chips, `#email` weekly
  report e-mail draft with a copy button). Czech wording, kept verbatim.
- **Crewship kind:** App, as an interactive multi-file artifact project,
  created by the generic showcase step through `POST /api/v1/pages/artifacts`
  (`seeddata/mirror_e8hy.go` appends it to `ShowcaseArtifacts`; no new seed
  step).

## Files

| File | What it is |
| --- | --- |
| `index.html` | The page exactly as the artifact service returned it (29,401 bytes, sha256 `e7ebdcd2…0d37d5`), including the publish skeleton and the Mermaid runtime block the service stores. |
| `m-e8hy.json` | The manifest the route takes: `index.html` (with the three resources below made local), `fonts.css`, `interactive.json` and `dashboard.json`. About 1.9 MB. |
| `_build.py` | Regenerates `m-e8hy.json` from `index.html`: `python3 -I _build.py <cache dir>`. Downloads are pinned by sha256. Not embedded in the binary. |

`interactive.json` (`{"schema_version": 1}`) and `dashboard.json`
(`{"schema_version": 1, "collections": ["showcase"]}`) are Crewship markers,
not content. The server runs a project's own scripts only when the project
declares at least one data collection (`artifacts.ParseInteractiveArtifact`).
Without them the copy would be a static page whose tabs never switch. The
original keeps no data, so `showcase` stays empty and nothing reads it.

## What was changed, and why

The Crewship interactive runtime has no network: the bootstrap CSP allows
inline scripts only (`script-src 'nonce-…'` plus `script-src 'unsafe-inline'`),
`style-src 'unsafe-inline'`, `font-src data:` and `connect-src 'none'`, and
`installInteractiveRuntime` drops every `<script src>`. It adds the project's
`text/css` files to the page. So exactly three tags of the original are replaced;
every other byte is the original's, and the Go test proves it by reversing
the three substitutions and comparing sha256 with the original.

1. **three.js r128 (MIT).** `<script src="https://cdnjs.cloudflare.com/ajax/libs/three.js/r128/three.min.js">`
   becomes `<script data-mirror="three.js r128">` with the same file inlined
   (sha256 `9274bbce…`, licence header kept). The ship renders and animates.
2. **Mermaid 8.11.5 (MIT).** The original's `<script src="/_runtime/mermaid-11.16.1.min.js">`
   is a claude.ai host runtime, not part of the artifact. It becomes
   `<script data-mirror="mermaid 8.11.5">`, inlined with an MIT header, plus a
   one-line shim (`data-mirror="mermaid render shim"`) that turns 8.x's
   callback `render()` into the Promise the page's runtime block awaits.
   Mermaid 11 (2.5 MB) does not fit beside three.js in the runtime's 2 MiB
   source limit; 8.11.5 (0.9 MB, 2021) is the newest release that does.
   The page's own runtime block (theme palette, dark mode re-render) runs
   unchanged.
3. **Fonts (SIL OFL 1.1).** The Google Fonts `<link>` becomes the comment
   `<!--mirror: Google Fonts faces are in fonts.css-->`. `fonts.css` embeds
   Bricolage Grotesque (500–700), IBM Plex Sans (400–600) and JetBrains Mono
   (400–600), latin and latin-ext subsets (enough for Czech), as woff2 data
   URIs, one variable file per subset (227 KB of font data).

The runtime source is 1,905,696 bytes of its 2,097,152 limit.

## Known differences

- **Mermaid version.** claude.ai renders the diagram with Mermaid 11.16.1; the
  copy uses 8.11.5. Same nodes, edges, labels and palette; layout and text
  metrics differ slightly (edge labels sit a little tighter).
- **Copy e-mail button.** The opaque sandbox has no clipboard permission. The
  page's own fallback runs: it selects the textarea and says
  "Text je označený, zkopíruj ho Ctrl+C."
- Font subsets other than latin and latin-ext (Cyrillic, Greek, Vietnamese)
  are not embedded; the page has no such text.
