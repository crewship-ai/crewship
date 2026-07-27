o# Brief: one source of truth for colour

**Status:** ready to execute · **Owner:** unassigned · **Written:** 2026-07-27

---

## 0. The rule this whole brief exists to enforce

> **`app/globals.css` is the only place a colour value is written down.
> Everything else references it. Nothing copies it.**

That is the entire principle. Every task below is a consequence of it.

Today the codebase has **four** colour vocabularies, and they disagree:

1. **`app/globals.css`** — `--success --warn --error --info --notice --gold
   --purple` plus the surface tokens, redefined per theme. Exposed through
   `@theme inline`, so `text-success`, `bg-warn/10`, `border-purple/30` all
   work right now. This is the one that should win.
2. **`lib/colors.ts` → `BRAND`** — hex literals that *re-type* values from
   (1). `BRAND.primary = "#1E7BFE"` is `--primary` (dark). `BRAND.info =
   "#5DA1FF"` is `--info`. `BRAND.primaryLight = "#0E6BE8"` is `--primary`
   (light). Change one, forget the other, and an SVG stops matching the
   button beside it. Nothing catches it.
3. **`lib/colors.ts` → `STATUS_COLORS` / `ISSUE_*_COLORS` /
   `PRIORITY_COLORS`** — raw Tailwind hexes (`#22c55e`, `#3b82f6`,
   `#ef4444`, `#f59e0b`, `#8b5cf6`). These are *semantic* colours that
   should be tokens, and they are not even the same colours as (1):
   `--success` is `oklch(0.72 0.18 152)`, not `#22c55e`. They are also
   theme-blind.
4. **15 local colour maps inside components**, several shadowing the shared
   ones outright — `components/features/crews/empty-roster.tsx:45` declares
   its own `STATUS_COLORS`. Plus **191 bare hex literals** in `components/`
   and `app/`, and **~3 170 raw Tailwind palette classes** across ~400 files.

**Why it matters, concretely.** Tokens are redefined per theme; a hardcoded
`text-emerald-400` or `#22c55e` is not. Every one of them is a spot that
silently ignores light mode. This is a correctness fix wearing a tidiness
costume.

## 1. The target shape

Three layers, each with one job. Keep it this boring:

| Layer | Job | Contains |
|---|---|---|
| `app/globals.css` | **Defines** every colour, per theme | `--success`, `--primary`, `--border`, … |
| `lib/colors.ts` | **Derives** strings for renderers that need a value rather than a class | `STATUS_COLORS.COMPLETED = "var(--success)"` |
| Components | **Consume** — Tailwind classes, or an import from `lib/colors.ts` | `text-success`, `stroke={STATUS_COLORS.X}` |

Rules that follow:

- A component never writes a hex, an `rgb()`, or a raw palette class.
- A component never declares a local colour map. If it needs one, it belongs
  in `lib/colors.ts`.
- `lib/colors.ts` never writes a hex for anything that exists as a token. It
  writes `var(--token)`.

**`var()` works in every consumer we have.** Verified: the consumers are SVG
`stroke`/`fill` props, React Flow node styles, Recharts series and inline
`style` objects — all resolve CSS custom properties in the DOM. There is no
`canvas` 2D context anywhere in the repo (checked), which is the one case
that would have forced a runtime `getComputedStyle` fallback. So the
derivation layer is plain strings. No helper, no hook, no build step.

`BRAND_RGBA(alpha)` currently returns `rgba(30, 123, 254, ${alpha})` — a
hand-copied channel triplet. Replace with
`color-mix(in oklch, var(--primary) ${alpha * 100}%, transparent)`.

## 2. What must NOT become a token

Colour that encodes **identity or category** is data, not style. Tokenising
it collapses distinctions users depend on. Leave these alone — they keep
literal values, and that is correct:

| Keep literal | Why |
|---|---|
| `CREW_COLORS`, `CREW_BG_CLASSES`, `resolveCrewColor` | Per-crew identity the user picks. |
| `GRADIENT_PALETTES` (`lib/crew-icons.ts`) | Same. |
| `EDGE_COLOR_PALETTE`, `LABEL_PRESET_COLORS` | Categorical — arbitrary distinct hues by design. |
| Recharts / chart **series** colours | Categorical. |
| DiceBear / agent avatar palettes | Deterministic per-agent identity. |
| Tiptap `HIGHLIGHT_COLORS` / `TEXT_COLORS` | User-chosen document colours. |

The test: *if two of these were the same colour, would the UI lose
information?* Yes → it is data, keep it. No → it is style, tokenise it.

These still move **into `lib/colors.ts`** if they are currently declared
inside a component. One source of truth applies to data palettes too; it
just is not `globals.css` for them.

## 3. The mapping

Raw shades cluster on 300/400/500 with `/10 /15 /20 /30` opacity modifiers.
The modifier carries over unchanged: `bg-emerald-500/10` → `bg-success/10`.

| From | To |
|---|---|
| `emerald-*`, `green-*` | `success` |
| `amber-*`, `yellow-*`, `orange-*` | `warn` |
| `red-*`, `rose-*` | `destructive` (**not** `error` — see §4a) |
| `violet-*`, `purple-*`, `fuchsia-*` | `purple` |
| `cyan-*`, `teal-*` | `notice` |
| `blue-*`, `sky-*` | `primary` **or** `info` — see §4b |
| grey text (`zinc/slate/gray/neutral`) | `muted-foreground` / `foreground` |
| grey surfaces | `card`, `muted`, `accent`, `surface-subtle`, `surface-raised` |
| grey borders | `border` |

Shade nuance is lost deliberately — `text-emerald-300` and `text-emerald-400`
both become `text-success`. The token owns the shade. If a spot genuinely
needs lighter, use opacity (`text-success/70`), never a different palette
step.

## 4. Two decisions you must NOT guess at

**a) `--destructive` and `--error` are the same colour.** Both
`oklch(0.70 0.19 25)`, in both themes. `destructive` has 219 uses and is the
shadcn convention the component library already follows; `error` has 3.

→ Standardise on `destructive`. Delete `--error` / `--color-error` as the
**final** commit, so a half-finished sweep never leaves a dangling token.

**b) blue → `primary` or `info`?** Different colours (`#1E7BFE` vs
`#5DA1FF`), both legitimate:

- **`primary`** — interactive or brand chrome: selected nav row, focus ring,
  link, primary button, active tab underline.
- **`info`** — an informational *state*: queued chip, info badge, journal
  entry marker, sparkbar.

Genuinely ambiguous? **Leave it and list it in your report.** Blue is the
largest family (782 occurrences); a wrong guess is a visible brand
inconsistency and a few deferrals cost nothing.

## 5. Order of work

Do NOT attempt this in one pass. One commit per batch, each independently
reviewable and revertable.

**Foundation first — these change the shape, the rest is then mechanical:**

1. `lib/colors.ts` — convert `BRAND` and the semantic maps to `var(--token)`;
   fix `BRAND_RGBA`. Nothing else may proceed until this lands, or later
   batches will convert components to point at hexes that are about to move.
2. **Fold the 15 local colour maps into `lib/colors.ts`**, deleting the local
   copies. Start with `empty-roster.tsx`'s shadowing `STATUS_COLORS`.

**Then the mechanical sweep, by directory:**

3. `components/features/settings/**` — smallest, partly clean, good calibration
4. `components/ui/**` — highest leverage
5. `components/layout/**`
6. `components/features/orchestration/**`
7. `components/features/crews/**` + `agents/**` — *most data-driven colour; read §2 twice*
8. `components/features/**` — remainder
9. `app/**`
10. Greys, everything — separate pass, different judgement
11. The 191 stray hex literals
12. `--error` removal (§4a)
13. The guardrail (§6)

Batches 3–11 are independent and can run in parallel **only if each agent
owns a disjoint file set** — a shared working tree otherwise means agents
overwrite each other.

## 6. The guardrail — without this it all comes back

Final commit, once the sweep is green. Otherwise the next feature
reintroduces `text-emerald-400` and this decays within a month.

Add an ESLint rule banning raw palette classes and bare hex in
`className`/style strings, allowlisting the §2 files. `eslint.config.mjs`
already exists. Sketch:

```js
// Raw palette colours and hex literals are fixed across themes — exactly
// the bug the semantic tokens exist to fix.
{
  files: ["components/**/*.tsx", "app/**/*.tsx"],
  ignores: ["lib/colors.ts", "lib/crew-icons.ts", /* §2 list */],
  rules: {
    "no-restricted-syntax": ["error", {
      selector: "Literal[value=/\\b(bg|text|border|ring|fill|stroke|from|to|via)-(emerald|green|red|rose|amber|yellow|orange|blue|sky|cyan|teal|violet|purple|fuchsia|zinc|slate|gray|neutral|stone|pink|lime|indigo)-[0-9]{2,3}\\b/]",
      message: "Use a semantic token (success/warn/destructive/info/notice/purple) or a surface token. Raw palette colours don't follow the theme. See .claude/context/prd/BRIEF-COLOR-TOKENS-2026.md",
    }],
  },
}
```

Verify it fires on a deliberately-bad line before trusting it, and that it
does **not** fire on the §2 allowlist.

## 7. Verification, per batch

Non-negotiable, in order:

```
npx tsc --noEmit
npx eslint <files you touched>
npx vitest run          # full suite — class-name assertions are scattered
npx next build
```

Then **look at it in both themes**, which is the whole point: deploy the
batch to a dev slot and load the affected pages in light and dark. A
conversion can type-check, pass tests, and still be the wrong token.

Report per batch: files touched, occurrences converted per family, every
usage deliberately deferred (file:line + why), and any bug found and left
alone. Do not fix unrelated bugs mid-sweep; write them down.

## 8. Definition of done

- `globals.css` is the only file defining a colour that has a token.
- `lib/colors.ts` contains `var(--token)` strings plus genuinely categorical
  data palettes — no re-typed brand hex.
- Zero local colour maps in components; zero bare hex outside the §2 list.
- Zero raw palette classes outside the §2 allowlist (`grep` proves it).
- `--error` gone; `destructive` everywhere.
- `tsc`, `eslint`, full `vitest`, `next build` all green.
- Both themes visually checked on a dev slot.
- The lint rule is in and demonstrably fires.
- A written list of deferred ambiguous usages — a known finite remainder,
  not an unknown one.
