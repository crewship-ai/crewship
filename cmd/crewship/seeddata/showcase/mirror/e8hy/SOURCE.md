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
| `index.html` | The page exactly as the artifact service returned it (29,401 bytes, sha256 `e7ebdcd2…0d37d5`), including the publish skeleton, the pinned `three.js/r128` script tag and the Mermaid runtime block the service stores. |
| `m-e8hy.json` | The manifest the route takes: `index.html` (same bytes), `interactive.json` and `dashboard.json`. |

`interactive.json` (`{"schema_version": 1}`) and `dashboard.json`
(`{"schema_version": 1, "collections": ["showcase"]}`) are Crewship markers,
not content. The server runs a project's own scripts only when the project
declares at least one data collection (`artifacts.ParseInteractiveArtifact`).
Without them the copy would be a static page whose tabs never switch. The
original keeps no data, so `showcase` stays empty and nothing reads it.

Regenerate the manifest from `index.html` with a JSON encoder that keeps
non-ASCII text as is (`json.dumps(..., ensure_ascii=False)`). The test checks
the sha256 of both copies.

## Known gaps (Crewship capability, not faked)

The Crewship interactive runtime has no network: the bootstrap CSP is
`script-src 'nonce-…'` plus a second `script-src 'unsafe-inline'` policy,
`style-src 'unsafe-inline'`, `font-src data:` and `connect-src 'none'`, and
`installInteractiveRuntime` drops every `<script src>`. Effects on this page:

1. **3D object (`#model-3d`)**: `https://cdnjs.cloudflare.com/ajax/libs/three.js/r128/three.min.js`
   is not loaded, so `window.THREE` is undefined and the canvas stays empty
   (the hint line and the right-hand text show). The tag keeps its pinned
   version so the copy can run once Crewship allows pinned CDN scripts.
2. **Diagram (`#diagram`)**: the Mermaid runtime is `/_runtime/mermaid-11.16.1.min.js`,
   a claude.ai host path that Crewship does not serve and could not load.
   The diagram shows as its Mermaid source text.
3. **Fonts**: the Google Fonts stylesheet (Bricolage Grotesque, IBM Plex Sans,
   JetBrains Mono) is blocked, so the stacks fall back to Arial and Courier New.
4. **Copy e-mail button**: the opaque sandbox has no clipboard permission. The
   page falls back to its own path: it selects the textarea and asks for Ctrl+C.

Tabs, the colour and font picker with the contrast readout, the task filters
and the e-mail preview run as in the original.
