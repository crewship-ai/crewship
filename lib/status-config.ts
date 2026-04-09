/**
 * Shared status badge styles used across the app.
 *
 * Each entry maps a semantic status name to the Tailwind className string
 * for badge-style rendering. Components compose their own STATUS_CONFIG by
 * picking from this palette and adding labels / icons specific to their domain.
 *
 * Two intensity variants are provided:
 *  - default (text-{color}-800) — used in most badge contexts
 *  - subtle  (text-{color}-700) — used in list/table views for less emphasis
 */

// ── Badge className palette (text-800 intensity) ────────────────────────────
export const STATUS_STYLES = {
  slate:   "bg-slate-100 text-slate-800 dark:bg-slate-900/40 dark:text-slate-300",
  blue:    "bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300",
  amber:   "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300",
  emerald: "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-300",
  red:     "bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300",
  gray:    "bg-gray-100 text-gray-800 dark:bg-gray-900/40 dark:text-gray-300",
  purple:  "bg-purple-100 text-purple-800 dark:bg-purple-900/40 dark:text-purple-300",
  orange:  "bg-orange-100 text-orange-800 dark:bg-orange-900/40 dark:text-orange-300",
  violet:  "bg-violet-100 text-violet-800 dark:bg-violet-900/40 dark:text-violet-300",
  green:   "bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300",
} as const

// ── Subtle badge className palette (text-700 intensity) ─────────────────────
export const STATUS_STYLES_SUBTLE = {
  slate:   "bg-slate-100 text-slate-700 dark:bg-slate-800/40 dark:text-slate-300",
  blue:    "bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300",
  amber:   "bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300",
  green:   "bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-300",
  red:     "bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300",
  gray:    "bg-gray-100 text-gray-700 dark:bg-gray-900/40 dark:text-gray-300",
} as const

// ── Type for components that build their own status config ──────────────────
export interface StatusConfigEntry {
  label: string
  className: string
}

export interface StatusConfigEntryWithIcon extends StatusConfigEntry {
  icon: React.ComponentType<{ className?: string }>
}
