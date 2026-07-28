/**
 * Centralized color definitions.
 *
 * SOURCE OF TRUTH: app/globals.css defines every color that has a token,
 * per theme. This file NEVER re-types those values as hex — it exposes them
 * as `var(--token)` strings for the renderers that need a *value* rather than
 * a Tailwind class (SVG stroke/fill, React Flow node/edge colors, Recharts,
 * inline styles). All of these resolve CSS custom properties in the DOM.
 *
 * The only literal colors that remain here are CATEGORICAL / IDENTITY palettes
 * — crew colors, edge/label preset hues, provider brand tints, message-type
 * and issue-status-icon aesthetics. Those encode *which thing this is*, not a
 * semantic state, so tokenizing them would collapse distinctions users rely
 * on. See BRIEF §2: "if two of these were the same color, would the UI lose
 * information? Yes → data, keep it."
 */

// ── Brand palette ── (var() into --primary / --primary-hover / --info;
//    theme-aware, so an SVG stroke matches the button beside it in both modes)

export const BRAND = {
  /** Primary brand blue — resolves to the theme's --primary. */
  primary: "var(--primary)",
  /** Hover-state shift of brand blue. */
  primaryHover: "var(--primary-hover)",
  /** Kept for API compatibility; --primary already resolves the light variant. */
  primaryLight: "var(--primary)",
  /** Info / lighter sibling — journal entries, sparkbars, queued chips. */
  info: "var(--info)",
} as const

/** Brand blue at a given alpha — for shadow/glow strings.
 *  Usage: `box-shadow: 0 0 12px ${BRAND_RGBA(0.22)};`
 *  color-mix keeps it tracking --primary instead of a hand-copied triplet. */
export function BRAND_RGBA(alpha: number): string {
  return `color-mix(in oklch, var(--primary) ${alpha * 100}%, transparent)`
}

// ── Task/mission/agent status colors (semantic → tokens) ──
// IN_PROGRESS uses --primary: the app brands the "active/alive" state brand-blue
// (see globals.css agent-pulse). --info stays for non-status informational chips.

export const STATUS_COLORS: Record<string, string> = {
  COMPLETED: "var(--success)",
  IN_PROGRESS: "var(--primary)",
  FAILED: "var(--destructive)",
  BLOCKED: "var(--warn)",
  PENDING: "var(--muted-foreground)",
  PLANNING: "var(--purple)",
  REVIEW: "var(--purple)",
  CANCELLED: "var(--muted-foreground)",
  SKIPPED: "var(--muted-foreground)",
  AWAITING_APPROVAL: "var(--purple)",
}

// ── Issue status icon colors (Linear-style aesthetic) ──
// KEEP LITERAL: this is a deliberate Linear-mimic palette. #5E6AD2 (Linear's
// signature indigo) has no token equivalent, and mixing tokenized + literal
// entries would make the status-icon row visually incoherent. Categorical
// identity, not semantic state — see BRIEF §2/§4. Deferred by design.

export const ISSUE_ICON_COLORS: Record<string, string> = {
  BACKLOG: "#8C8C8C",
  TODO: "#8C8C8C",
  PLANNING: "#8C8C8C",
  IN_PROGRESS: "#F2C94C",
  REVIEW: "#F2994A",
  COMPLETED: "#5E6AD2",
  DONE: "#5E6AD2",
  FAILED: "#EF4444",
  CANCELLED: "#95959F",
  DUPLICATE: "#95959F",
}

// ── Issue status chart colors (progress bars, pie charts) — semantic → tokens ──

export const ISSUE_STATUS_COLORS: Record<string, string> = {
  BACKLOG: "var(--muted-foreground)",
  TODO: "var(--muted-foreground)",
  IN_PROGRESS: "var(--primary)",
  REVIEW: "var(--purple)",
  DONE: "var(--success)",
  COMPLETED: "var(--success)",
  CANCELLED: "var(--destructive)",
  FAILED: "var(--destructive)",
}

// ── Priority colors ──
// KEEP LITERAL: urgent/high (orange), medium (yellow), low (blue) encode an
// ordinal priority level through three distinct hues. Tokenizing orange→warn
// and yellow→warn would collapse urgent and medium to one color and lose the
// priority distinction. Categorical-ordinal data — see BRIEF §2. Deferred.

export const PRIORITY_COLORS: Record<string, string> = {
  urgent: "#FC7840",
  high: "#FC7840",
  medium: "#EAB308",
  low: "#3B82F6",
}

// ── Label preset colors ── (categorical, user-chosen — KEEP, BRIEF §2)

export const LABEL_PRESET_COLORS = [
  { name: "Red", value: "#EF4444" },
  { name: "Orange", value: "#F97316" },
  { name: "Yellow", value: "#EAB308" },
  { name: "Green", value: "#22C55E" },
  { name: "Blue", value: "#3B82F6" },
  { name: "Purple", value: "#A855F7" },
  { name: "Pink", value: "#EC4899" },
  { name: "Gray", value: "#6B7280" },
] as const

// ── Crew palette (per-crew identity the user picks — KEEP, BRIEF §2) ──

export const CREW_COLORS: Record<string, string> = {
  blue: "#3b82f6",
  emerald: "#10b981",
  violet: "#8b5cf6",
  amber: "#f59e0b",
  rose: "#f43f5e",
  cyan: "#06b6d4",
  lime: "#84cc16",
  fuchsia: "#d946ef",
}

export const CREW_COLOR_DEFAULT = "#64748b"

/** Resolves a crew palette ID to its hex color, falling back to slate gray. */
export function resolveCrewColor(color: string | null | undefined): string {
  return (color && CREW_COLORS[color]) || CREW_COLOR_DEFAULT
}

/**
 * Tailwind bg utility classes per crew palette ID. Categorical identity, so
 * these stay as palette classes (this file is on the lint allowlist).
 */
export const CREW_BG_CLASSES: Record<string, string> = {
  blue: "bg-blue-500",
  emerald: "bg-emerald-500",
  violet: "bg-violet-500",
  amber: "bg-amber-500",
  rose: "bg-rose-500",
  cyan: "bg-cyan-500",
  lime: "bg-lime-500",
  fuchsia: "bg-fuchsia-500",
}

/** Default bg class used when a crew color is missing or not in the palette. */
export const CREW_BG_DEFAULT = "bg-slate-500"

/**
 * Resolves a crew palette ID to a Tailwind bg class. Prefer this over
 * `style={{ backgroundColor: resolveCrewColor(...) }}` so components stay
 * Tailwind-only and raw hex values never leak to consumers.
 */
export function getCrewBgClass(color: string | null | undefined): string {
  return (color && CREW_BG_CLASSES[color]) || CREW_BG_DEFAULT
}

// ── Edge color palette (graph connections — categorical, KEEP, BRIEF §2) ──

export const EDGE_COLOR_PALETTE = [
  "#06b6d4", "#3b82f6", "#8b5cf6", "#22c55e",
  "#f59e0b", "#ec4899", "#14b8a6", "#6366f1",
] as const

// ── Direction colors (bidirectional vs unidirectional edges) ──
// KEEP LITERAL: binary categorical encoding of edge direction; collapsing the
// two hues would lose the distinction. BRIEF §2. Deferred.

export const DIRECTION_COLORS = {
  bidirectional: "#06b6d4",   // cyan
  unidirectional: "#f59e0b",  // amber
} as const

// ── A2A message type colors ──
// KEEP LITERAL: @assign/@ask/@broadcast/@result are message-type identity;
// each hue answers "which kind of message", not a semantic state. BRIEF §2.

export const MESSAGE_TYPE_COLORS: Record<string, string> = {
  "@assign": "#3b82f6",
  "@ask": "#a855f7",
  "@broadcast": "#06b6d4",
  "@result": "#22c55e",
}

// ── Graph chrome (structural/decorative graph canvas colors) ──
// KEEP LITERAL: dark-first decorative values tuned for the React Flow canvas
// (minimap node fill, dimmed-edge, mission label). The graph renders dark-only;
// these are not theme-adaptive semantic states. Deferred — see report.

export const GRAPH_CHROME = {
  dimmedEdge: "#334155",
  minimapNode: "#1e2332",
  missionLabel: "#e2e8f0",
} as const

// ── Status badge classes (Tailwind, for Badge components) — semantic → tokens ──

export const STATUS_BADGE_CLASSES: Record<string, string> = {
  PENDING: "bg-muted text-muted-foreground",
  BLOCKED: "bg-warn/20 text-warn",
  IN_PROGRESS: "bg-primary/15 text-primary",
  COMPLETED: "bg-success/20 text-success",
  FAILED: "bg-destructive/20 text-destructive",
  SKIPPED: "bg-muted text-muted-foreground",
  AWAITING_APPROVAL: "bg-purple/20 text-purple",
}

// ── Complexity badge classes (Tailwind, for Badge components) — semantic → tokens ──

export const COMPLEXITY_BADGE_CLASSES: Record<string, string> = {
  SIMPLE: "bg-success/20 text-success",
  MEDIUM: "bg-warn/20 text-warn",
  COMPLEX: "bg-destructive/20 text-destructive",
}

// ── Graph background tints for status (semantic → token opacities) ──

export const STATUS_BG: Record<string, string> = {
  COMPLETED: "bg-success/10",
  IN_PROGRESS: "bg-primary/10",
  FAILED: "bg-destructive/10",
  BLOCKED: "bg-warn/10",
  PENDING: "bg-muted",
  REVIEW: "bg-purple/10",
  SKIPPED: "bg-muted",
  AWAITING_APPROVAL: "bg-purple/10",
}

// ── Light-theme aware status banner backgrounds ──
// KEEP LITERAL: this map already does the theme split correctly (distinct
// light/dark shades). The semantic tokens are single-value across both themes
// (--success is the same oklch in light and dark, tuned for dark), so folding
// this into `bg-success/10 text-success` would regress light-mode contrast —
// text-success (0.72 L) on a light tint fails WCAG AA. This is the one map
// that's already right; leaving it. See BRIEF §0 (theme-awareness is the goal)
// and report. Palette classes are allowed here (lint allowlist).
export const STATUS_BG_LIGHT: Record<string, string> = {
  COMPLETED: "bg-emerald-50 dark:bg-emerald-950/30 text-emerald-700 dark:text-emerald-400",
  IN_PROGRESS: "bg-blue-50 dark:bg-blue-950/30 text-blue-700 dark:text-blue-400",
  FAILED: "bg-red-50 dark:bg-red-950/30 text-red-700 dark:text-red-400",
  BLOCKED: "bg-amber-50 dark:bg-amber-950/30 text-amber-700 dark:text-amber-400",
  PENDING: "bg-muted text-muted-foreground",
  REVIEW: "bg-violet-50 dark:bg-violet-950/30 text-violet-700 dark:text-violet-400",
  AWAITING_APPROVAL: "bg-violet-50 dark:bg-violet-950/30 text-violet-700 dark:text-violet-400",
  CANCELLED: "bg-muted text-muted-foreground",
  SKIPPED: "bg-muted text-muted-foreground",
}

// ── Tailwind classes for StatusDot (solid fill, ≤ 2×2) — semantic → tokens ──

export const STATUS_DOT_CLASSES: Record<string, string> = {
  COMPLETED: "bg-success",
  IN_PROGRESS: "bg-primary",
  FAILED: "bg-destructive",
  BLOCKED: "bg-warn",
  PENDING: "bg-muted-foreground",
  REVIEW: "bg-purple",
  AWAITING_APPROVAL: "bg-purple",
  CANCELLED: "bg-muted-foreground",
  SKIPPED: "bg-muted-foreground",
  PLANNING: "bg-purple",
}

// ── Provider icon colors (Anthropic/OpenAI/GitHub/etc.) ──
// KEEP LITERAL: per-provider brand identity tints (if OpenAI and Google were
// the same color you'd lose which provider it is). Categorical — BRIEF §2.
// Palette classes are allowed here (lint allowlist).
export const PROVIDER_ICON_COLOR: Record<string, string> = {
  ANTHROPIC: "text-violet-500",
  OPENAI: "text-emerald-500",
  GOOGLE: "text-blue-500",
  CURSOR: "text-cyan-500",
  FACTORY: "text-rose-500",
  GITHUB: "text-foreground",
  GITLAB: "text-orange-500",
  VERCEL: "text-foreground",
  AWS: "text-amber-500",
  CUSTOM_CLI: "text-muted-foreground",
  NONE: "text-muted-foreground",
}

// ── Credential type icon colors (AI_CLI_TOKEN, API_KEY, etc.) ──
// KEEP LITERAL: per-credential-type identity tints. Categorical — BRIEF §2.
export const CREDENTIAL_TYPE_ICON_COLOR: Record<string, string> = {
  AI_CLI_TOKEN: "text-violet-500",
  API_KEY: "text-amber-500",
  CLI_TOKEN: "text-blue-500",
  SECRET: "text-muted-foreground",
  OAUTH2: "text-emerald-500",
}
