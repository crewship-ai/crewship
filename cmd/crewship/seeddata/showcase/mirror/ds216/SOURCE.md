# Template Crewship Design System (mirror)

- **Original:** https://claude.ai/artifact/216yVddS9pUMcVvznaxLcU, a claude.ai artifact of
  type "Design System", read at version `1794069109-eb06`. Its content is under `project/`:
  `design-system.json` (the index), `README.md`, `tokens.json` and
  `components/Cover/preview.html`. Its asset store is empty and its `assetGroups` is `{}`.
- **Seeded as:** slug `ref-design-system`, name "Template Crewship Design System"
  (`seeddata/mirror_ds216.go`), created through `POST /api/v1/pages/artifacts` like the other
  showcase projects.
- **Crewship kind:** Design in Library mode. An artifact project whose manifest holds
  `design-system.json` opens as a Library (`kinds.ts` `kindOf`, `kinds/design/design-library.tsx`).
  Storage, versions and restore are the artifact VersionStore. No separate design-system backend exists.

## Files

`files/` holds the project's files as they are stored. `manifest.json` is the request body the
route accepts, generated from `files/` (see "Regenerating" below). The test
`TestMirrorDS216_ManifestIsTheFilesFolder` fails if the two differ.

| Project file | From the original | How |
| --- | --- | --- |
| `design-system.json` | `project/tokens.json` | Converted to the Library's schema 2 (`components/features/artifacts/design-system/manifest.ts`) |
| `README.md` | `project/README.md` | Verbatim; the Library shows it under Overview |
| `tokens.json` | `project/tokens.json` | Verbatim: themes, usage notes and type groups |
| `components/Cover/preview.html` | `project/components/Cover/preview.html` | Verbatim; the Library previews it under Components |
| `source/design-system.json` | `project/design-system.json` | Verbatim original index (title, namespace, last change) |
| `index.html` | the Cover preview | The entry point a project needs: the Cover with the token variables for light and dark (`prefers-color-scheme`), which is how the original type renders a card |

The version message is the original's `lastChange.note`.

## Token mapping

- **Themes:** the original's two themes, `light` "Světlé" and `dark` "Tmavé", in its order
  (the first is the default).
- **Colors:** the 12 tokens, each once under its original name with both theme values and its
  original `usage` note: `{"value": {"light": …, "dark": …}, "usage": …}`. The Library shows
  the chosen theme, every theme's value, the usage note and the WCAG contrast of white or black
  text on the swatch; the Cover preview gets the chosen theme's values.
- **Typography:** 7 styles (`display`, `title`, `heading`, `body`, `caption`, `code`,
  `eyebrow`). Each keeps its size, weight, family (sans or mono, spelled as in the original
  `families`) and its original group (Nadpisy, Text, Mono) in `group`. The line height is stored as a ratio (line px / size px, 4 decimals) that rounds
  back to the original pixels.
- **Spacing:** `space-2/3/4/6` = 8/12/16/24 px. **Radius:** `radius-sm/md/lg` = 10/14/18 px.
- **Components:** `Cover`.

## Known gaps (the Library format cannot hold these; nothing is faked)

- **Spacing and radius usage notes:** the Library schema has a usage note only for colours.
  The original has none for spacing or radius, so nothing is dropped.
- **Line height** is a ratio, not pixels. Its rendered value can differ from the original by
  less than 0.01 px.
- **Fonts:** the Cover preview links IBM Plex Sans from Google Fonts. The Library's preview
  frame allows no network, so the preview falls back to Arial. The original's `fonts` list is
  empty, and the Library says so under Typography ("carries no font files").
- **Component preview width:** the card is laid out at 960 px and scaled down to the column.
- **The Library view is behind the web build switch** `NEXT_PUBLIC_ARTIFACT_DESIGN_SYSTEM=true`.
  Without it, the project opens as an App · Static and shows `index.html`, the Cover.
- **Not in the original, not mirrored:** the claude.ai type's own files (`SKILL.md`,
  `index.html` app shell, `artifact-type/*`). They belong to the Artifact type, not to this
  design system.

## Regenerating

After editing a file in `files/`, rebuild `manifest.json` in this order: `index.html`,
`design-system.json`, `README.md`, `tokens.json`, `components/Cover/preview.html`,
`source/design-system.json`. Each file becomes `{path, media_type, content}`, with media types
`text/html`, `application/json` or `text/markdown`. Set `entrypoint` to `index.html` and
`message` to `lastChange.note`. Then run
`crewship-heavy go test ./cmd/crewship/ -run 'TestMirrorDS216|TestShowcaseCatalogue|TestSeedShowcase'`.
