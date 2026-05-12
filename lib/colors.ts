/**
 * Centralized color definitions — single source of truth.
 * All status, crew, edge, priority, and semantic colors live here.
 *
 * For brand colors prefer Tailwind utility classes (`bg-primary`,
 * `text-primary`, `border-primary`) — they read CSS variables
 * defined in app/globals.css and track theme changes automatically.
 *
 * The literal hex values in `BRAND` below exist ONLY for cases where
 * the renderer cannot consume CSS variables: rgba shadow strings,
 * dynamic SVG stroke/fill props, third-party canvas libraries, etc.
 * If you can render with a Tailwind class, do that instead — the
 * BRAND constants exist to avoid scattered hex literals, not as the
 * default styling path.
 */

// ── Brand palette ── (1:1 with --primary / --primary-hover / --info
// CSS vars in app/globals.css; matches marketing site crewship-web)

export const BRAND = {
  /** Primary brand blue — dark-mode --primary. Use `bg-primary` in TSX. */
  primary: "#1E7BFE",
  /** Hover-state shift of brand blue. */
  primaryHover: "#3D8FFE",
  /** Light-mode primary — deeper variant for white-bg legibility. */
  primaryLight: "#0E6BE8",
  /** Info / lighter sibling — for journal entries, sparkbars, queued chips. */
  info: "#5DA1FF",
} as const

/** Brand blue as `rgba()` — for shadow/glow strings that can't use CSS vars.
 *  Usage: `box-shadow: 0 0 12px ${BRAND_RGBA(0.22)};` */
export function BRAND_RGBA(alpha: number): string {
  return `rgba(30, 123, 254, ${alpha})`
}

// ── Task/mission/agent status colors ──

export const STATUS_COLORS: Record<string, string> = {
  COMPLETED: "#22c55e",
  IN_PROGRESS: "#3b82f6",
  FAILED: "#ef4444",
  BLOCKED: "#f59e0b",
  PENDING: "#64748b",
  PLANNING: "#8b5cf6",
  REVIEW: "#a855f7",
  CANCELLED: "#6b7280",
  SKIPPED: "#6b7280",
  AWAITING_APPROVAL: "#8b5cf6",
}

// ── Issue status icon colors (Linear-style, used in SVG status icons + project status) ──

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

// ── Issue status chart colors (progress bars, pie charts) ──

export const ISSUE_STATUS_COLORS: Record<string, string> = {
  BACKLOG: "#6b7280",
  TODO: "#a3a3a3",
  IN_PROGRESS: "#3b82f6",
  REVIEW: "#a855f7",
  DONE: "#22c55e",
  COMPLETED: "#22c55e",
  CANCELLED: "#ef4444",
  FAILED: "#ef4444",
}

// ── Priority colors ──

export const PRIORITY_COLORS: Record<string, string> = {
  urgent: "#FC7840",
  high: "#FC7840",
  medium: "#EAB308",
  low: "#3B82F6",
}

// ── Label preset colors ──

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

// ── Crew palette (maps crew color ID → hex) ──

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
 * Tailwind bg utility classes per crew palette ID.
 * Keep these in sync with CREW_COLORS above — they are used wherever inline
 * style backgrounds would otherwise be needed, so the Tailwind-only rule and
 * the palette-ID convention are both enforced at the render site.
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

// ── Edge color palette (graph connections) ──

export const EDGE_COLOR_PALETTE = [
  "#06b6d4", "#3b82f6", "#8b5cf6", "#22c55e",
  "#f59e0b", "#ec4899", "#14b8a6", "#6366f1",
] as const

// ── Direction colors (bidirectional vs unidirectional edges) ──

export const DIRECTION_COLORS = {
  bidirectional: "#06b6d4",   // cyan
  unidirectional: "#f59e0b",  // amber
} as const

// ── A2A message type colors ──

export const MESSAGE_TYPE_COLORS: Record<string, string> = {
  "@assign": "#3b82f6",
  "@ask": "#a855f7",
  "@broadcast": "#06b6d4",
  "@result": "#22c55e",
}

// ── Graph chrome (structural/decorative graph colors) ──

export const GRAPH_CHROME = {
  dimmedEdge: "#334155",
  minimapNode: "#1e2332",
  missionLabel: "#e2e8f0",
} as const

// ── Status badge classes (Tailwind, for Badge components) ──

export const STATUS_BADGE_CLASSES: Record<string, string> = {
  PENDING: "bg-muted text-muted-foreground",
  BLOCKED: "bg-amber-500/20 text-amber-400",
  IN_PROGRESS: "bg-cyan-500/20 text-cyan-400",
  COMPLETED: "bg-emerald-500/20 text-emerald-400",
  FAILED: "bg-red-500/20 text-red-400",
  SKIPPED: "bg-muted text-muted-foreground",
  AWAITING_APPROVAL: "bg-violet-500/20 text-violet-400",
}

// ── Complexity badge classes (Tailwind, for Badge components) ──

export const COMPLEXITY_BADGE_CLASSES: Record<string, string> = {
  SIMPLE: "bg-emerald-500/20 text-emerald-400",
  MEDIUM: "bg-amber-500/20 text-amber-400",
  COMPLEX: "bg-red-500/20 text-red-400",
}

// ── Graph background colors for status ──

export const STATUS_BG: Record<string, string> = {
  COMPLETED: "bg-[#0a1f0f]",
  IN_PROGRESS: "bg-[#0a1220]",
  FAILED: "bg-[#1f0a0a]",
  BLOCKED: "bg-[#1f1a0a]",
  PENDING: "bg-[#0f1115]",
  REVIEW: "bg-[#150a1f]",
  SKIPPED: "bg-[#0f1115]",
  AWAITING_APPROVAL: "bg-[#150a1f]",
}

// ── Light-theme aware status banner backgrounds ──
// Use for alert banners, info strips, escalation cards. Light in light mode,
// tinted-dark in dark mode. Pairs bg + text for one-shot consumption.
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

// ── Tailwind classes for StatusDot (solid fill, ≤ 2×2) ──
// Use inside <StatusDot status={...} /> or wherever an inline hex would leak.
export const STATUS_DOT_CLASSES: Record<string, string> = {
  COMPLETED: "bg-emerald-500",
  IN_PROGRESS: "bg-blue-500",
  FAILED: "bg-red-500",
  BLOCKED: "bg-amber-500",
  PENDING: "bg-slate-400",
  REVIEW: "bg-violet-500",
  AWAITING_APPROVAL: "bg-violet-500",
  CANCELLED: "bg-gray-500",
  SKIPPED: "bg-gray-500",
  PLANNING: "bg-violet-500",
}

// ── Provider icon colors (Anthropic/OpenAI/GitHub/etc.) ──
// Replaces hardcoded text-violet-600 / text-amber-600 / text-emerald-600 etc.
// in credentials and integrations pages. Values are tint-only — pair with
// lucide icons via <Icon className={PROVIDER_ICON_COLOR[provider]} />.
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
// Replaces hardcoded text-{color}-600 constants in TYPE_CONFIG maps
// scattered through credential/agent pages.
export const CREDENTIAL_TYPE_ICON_COLOR: Record<string, string> = {
  AI_CLI_TOKEN: "text-violet-500",
  API_KEY: "text-amber-500",
  CLI_TOKEN: "text-blue-500",
  SECRET: "text-muted-foreground",
  OAUTH2: "text-emerald-500",
}
