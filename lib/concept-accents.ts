/**
 * A colour per concept, on top of the icon per concept.
 *
 * `concept-icons.ts` fixed WHICH glyph a concept wears. This fixes what colour
 * it is. Same argument, one step further: a rail of fourteen identical grey
 * glyphs is a wall of grey glyphs — you read the label every time, because the
 * shape is doing all the work and shapes at 16px are nearly the same shape.
 * Colour is the thing the eye lands on before it reads.
 *
 * ── Where the colours come from ──────────────────────────────────────────
 *
 * Mostly the SEMANTIC tokens already in globals.css — `--primary`, `--info`,
 * `--notice`, `--success`, `--warn`, `--gold`, `--purple`, `--destructive` —
 * plus five rail hues (`--indigo`, `--rose`, `--lime`, `--orange`, `--azure`)
 * added so every destination in the rail has its own colour; each is defined
 * per theme and its text contrast is tested like the semantic ones. Those are declared identically in
 * light and dark ("brand-consistent across modes") and their contrast is
 * already measured; inventing a parallel accent palette would be a second
 * source of truth for the same job, and the first one to drift.
 *
 * `--chart-1..5` are deliberately NOT used. globals.css records that they are
 * currently identical between `:root` and `.dark` — a known bug (#1940) — and
 * they are a data-series scale, not a UI palette.
 *
 * ── Why literal class strings ────────────────────────────────────────────
 *
 * Tailwind's scanner reads source text, so a class assembled at runtime
 * (`` `text-${accent}` ``) is never generated. Every class here is written out.
 * That is also why this is a map and not a function.
 *
 * ── Reading the three slots ──────────────────────────────────────────────
 *
 *   fg     the glyph itself, and any label that has to match it
 *   chip   the tinted square a glyph sits in (fill + hairline, one string)
 *   soft   the same tint without the border — for rows and hover states
 *
 * `fg` uses the `-hover` variant wherever the base token is not text-safe on
 * a tint. `--primary-hover` and `--purple-hover` exist for exactly this and
 * carry the measurements in their own comments; the remaining tokens sit at
 * lightness ≥ 0.72, which clears 4.5:1 on both card surfaces.
 */

export type AccentName =
  | "blue"
  | "sky"
  | "teal"
  | "green"
  | "amber"
  | "gold"
  | "purple"
  | "red"
  | "slate"
  // Rail hues added for one-colour-per-function (tokens in globals.css,
  // contrast pinned in lib/__tests__/theme-contrast.test.ts).
  | "indigo"
  | "rose"
  | "lime"
  | "orange"
  | "azure"

export interface Accent {
  /** Glyph / matching label colour. */
  fg: string
  /** Tinted chip: fill + hairline border. */
  chip: string
  /** Fill only, for rows and hovers. */
  soft: string
  /** The raw colour, as a CSS value, for the Harbor icon tile (`--ic`). */
  tint: string
}

export const ACCENT: Record<AccentName, Accent> = {
  blue: { fg: "text-primary-hover", chip: "bg-primary/12 border-primary/25", soft: "bg-primary/10", tint: "var(--primary)" },
  sky: { fg: "text-info", chip: "bg-info/12 border-info/25", soft: "bg-info/10", tint: "var(--info)" },
  teal: { fg: "text-notice", chip: "bg-notice/12 border-notice/25", soft: "bg-notice/10", tint: "var(--notice)" },
  green: { fg: "text-success", chip: "bg-success/12 border-success/25", soft: "bg-success/10", tint: "var(--success)" },
  amber: { fg: "text-warn", chip: "bg-warn/12 border-warn/25", soft: "bg-warn/10", tint: "var(--warn)" },
  gold: { fg: "text-gold", chip: "bg-gold/12 border-gold/25", soft: "bg-gold/10", tint: "var(--gold)" },
  purple: { fg: "text-purple-hover", chip: "bg-purple/12 border-purple/25", soft: "bg-purple/10", tint: "var(--purple)" },
  red: { fg: "text-destructive", chip: "bg-destructive/12 border-destructive/25", soft: "bg-destructive/10", tint: "var(--destructive)" },
  indigo: { fg: "text-indigo", chip: "bg-indigo/12 border-indigo/25", soft: "bg-indigo/10", tint: "var(--indigo)" },
  rose: { fg: "text-rose", chip: "bg-rose/12 border-rose/25", soft: "bg-rose/10", tint: "var(--rose)" },
  lime: { fg: "text-lime", chip: "bg-lime/12 border-lime/25", soft: "bg-lime/10", tint: "var(--lime)" },
  orange: { fg: "text-orange", chip: "bg-orange/12 border-orange/25", soft: "bg-orange/10", tint: "var(--orange)" },
  azure: { fg: "text-azure", chip: "bg-azure/12 border-azure/25", soft: "bg-azure/10", tint: "var(--azure)" },
  slate: {
    fg: "text-muted-foreground",
    chip: "bg-foreground/[0.05] border-border/60",
    soft: "bg-foreground/[0.04]",
    tint: "var(--muted-foreground)",
  },
}

/**
 * Concept → accent. Keys match `CONCEPT_ICON` exactly; the test in
 * `lib/__tests__/concept-accents.test.ts` fails if one grows a key the other
 * does not, which is the drift this file exists to prevent a second time.
 *
 * The assignment is not decorative. Concepts that appear together in one rail
 * group get different hues (that is what makes the group scannable), and
 * concepts that mean the same thing on two screens get the same hue even when
 * they are far apart — `runs` is the journal's colour because it opens the
 * journal, exactly as it is already the journal's icon.
 */
export const CONCEPT_ACCENT = {
  // ── Plan ──
  dashboard: "blue",
  inbox: "sky",
  sessions: "teal",
  issues: "indigo",
  // A mission is issue work in flight, and the two appear side by side in the
  // work cards — same family, same accent. concept-icons.ts gained the icon in
  // f85d1d41 without this entry, which is what the icon/accent parity test in
  // lib/__tests__/concept-accents.test.ts exists to catch.
  missions: "indigo",
  routines: "purple",
  pages: "green",

  // ── Run ──
  activity: "amber",
  journal: "gold",
  // Accepted work waiting for, or holding, an execution slot. It sits beside
  // activity and the journal because all three answer "what is this server
  // doing right now", and it shares activity's amber for the same reason.
  work: "amber",

  // ── Build ──
  crews: "rose",
  skills: "lime",
  credentials: "orange",
  integrations: "azure",

  // ── System ──
  marketplace: "slate",
  settings: "slate",
  admin: "red",
  workspace: "slate",

  // ── Concepts inside a detail screen ──
  triggers: "green",
  runs: "gold",
  peers: "sky",
  channels: "teal",
  tools: "azure",
  memory: "purple",
} as const satisfies Record<string, AccentName>

export type ConceptKey = keyof typeof CONCEPT_ACCENT

/** The accent for a concept, falling back to the neutral one. */
export function accentFor(concept: string | undefined): Accent {
  if (!concept) return ACCENT.slate
  const name = (CONCEPT_ACCENT as Record<string, AccentName>)[concept]
  return name ? ACCENT[name] : ACCENT.slate
}
