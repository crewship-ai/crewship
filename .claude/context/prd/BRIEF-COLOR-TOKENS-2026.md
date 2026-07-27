# Brief: one source of truth for colour

**Status:** executed 2026-07-27 · **Owner:** Pavel Srba

---

## 0. The rule this whole brief exists to enforce

> **`app/globals.css` is the only place a colour value is written down.
> Everything else references it. Nothing copies it.**

That is the entire principle. Every task below is a consequence of it.

## 1. The target shape

Three layers, each with one job:

| Layer | Job | Contains |
|---|---|---|
| `app/globals.css` | **Defines** every colour, per theme | `--success`, `--primary`, `--border`, … |
| `lib/colors.ts` | **Derives** `var(--token)` strings for renderers that need a value | `STATUS_COLORS.COMPLETED = "var(--success)"` |
| Components | **Consume** — Tailwind classes, or an import from `lib/colors.ts` | `text-success`, `stroke={STATUS_COLORS.X}` |

- A component never writes a hex, `rgb()`, or a raw palette class (outside §2).
- A component never declares a local semantic colour map — it belongs in `lib/colors.ts`.
- `lib/colors.ts` never writes a hex for anything that exists as a token.

`var()` resolves in every consumer here (SVG stroke/fill, React Flow, Recharts,
inline styles); there is no canvas 2D context. `BRAND_RGBA(a)` uses
`color-mix(in oklch, var(--primary) …%, transparent)`.

## 2. What must NOT become a token

Colour that encodes **identity or category** is data, not style. Test: *if two
of these were the same colour, would the UI lose information? Yes → keep it.*

Kept literal (and allowlisted in `eslint.config.mjs`): per-crew/avatar/label
colours, `DOMAIN_COLORS`, provider/credential brand tints, edge/message-type
palettes, issue-status-icon (Linear) aesthetic, priority ordinal hues,
data-viz intensity scales (heatmap), JSON/YAML syntax tokens, file-type icon
maps, trace-lane maps, node-kind identity, indigo "AI-agent" markers (no token),
`STATUS_BG_LIGHT` (already theme-correct; a single token would regress
light-mode contrast), and 2 genuinely-ambiguous deferrals (runs-tab run-id
cell, files-tab folder icon).

## 3. The mapping

| From | To |
|---|---|
| `emerald`, `green` | `success` |
| `amber`, `yellow`, `orange` | `warn` |
| `red`, `rose` | `destructive` |
| `violet`, `purple`, `fuchsia` | `purple` |
| `cyan`, `teal` | `notice` |
| `blue`, `sky` | `primary` (interactive/brand chrome) **or** `info` (informational state) — §4b |
| grey text | `muted-foreground` / `foreground` |
| grey surfaces | `background` (inputs, `-950`), `muted` / `card` / `accent` |
| grey borders | `border` |

The token owns the shade; nuance is lost deliberately. Need lighter → opacity
(`text-success/70`), never a different palette step. Light tints (`-50/-100`)
become token opacities so a tinted banner stays tinted, not solid.

## 4. Two decisions not to guess at

**a) `--error` == `--destructive`** (same oklch, both themes). Standardised on
`destructive`; `--error`/`--color-error` deleted last.

**b) blue → `primary` or `info`.** primary = interactive/brand chrome (buttons,
links, focus/selection rings, active tabs, live/in-progress). info = an
informational *state* (info badge, queued/unread chip, journal marker,
sparkbar, hint callout). Genuinely ambiguous → defer + list.

## 5. Order (executed)

1. `lib/colors.ts` tokenised + `BRAND_RGBA`. 2. Semantic local maps folded.
3–9. Directory sweeps (colored families). 10. Greys. 11. Stray hex.
12. `--error` removal. 13. The guardrail.

## 6. The guardrail

`eslint.config.mjs` bans raw palette classes and hex in `components/**/*.tsx` +
`app/**/*.tsx` via `no-restricted-syntax`, allowlisting the §2 files. Verified
it fires on `text-emerald-400` and not on the allowlist.

## 7. Verification (per batch)

`npx tsc --noEmit` · `npx eslint <files>` · `npx vitest run` · `npx next build`
· visual check both themes on a dev slot.

## 8. Definition of done

- `globals.css` the only file defining a tokened colour.
- `lib/colors.ts` = `var(--token)` strings + categorical data palettes.
- Zero semantic local colour maps in components; zero raw palette classes
  outside the §2 allowlist (`eslint` proves it).
- `--error` gone; `destructive` everywhere.
- `tsc`, `eslint`, `vitest`, `next build` green; both themes checked.
- Lint rule in and demonstrably firing; deferred-ambiguous list recorded.
