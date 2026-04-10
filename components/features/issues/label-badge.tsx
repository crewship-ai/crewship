"use client"

import type { IssueLabel } from "@/lib/types/mission"

interface LabelBadgeProps {
  label: IssueLabel
}

/**
 * Map common color names to Tailwind-compatible HSL or hex values.
 * Falls back to the raw color string if it looks like a hex/rgb value.
 */
const COLOR_MAP: Record<string, { bg: string; text: string }> = {
  red: { bg: "bg-red-500/15", text: "text-red-600 dark:text-red-400" },
  orange: { bg: "bg-orange-500/15", text: "text-orange-600 dark:text-orange-400" },
  yellow: { bg: "bg-yellow-500/15", text: "text-yellow-600 dark:text-yellow-400" },
  green: { bg: "bg-green-500/15", text: "text-green-600 dark:text-green-400" },
  blue: { bg: "bg-blue-500/15", text: "text-blue-600 dark:text-blue-400" },
  purple: { bg: "bg-purple-500/15", text: "text-purple-600 dark:text-purple-400" },
  pink: { bg: "bg-pink-500/15", text: "text-pink-600 dark:text-pink-400" },
  gray: { bg: "bg-gray-500/15", text: "text-gray-600 dark:text-gray-400" },
  slate: { bg: "bg-slate-500/15", text: "text-slate-600 dark:text-slate-400" },
  cyan: { bg: "bg-cyan-500/15", text: "text-cyan-600 dark:text-cyan-400" },
  teal: { bg: "bg-teal-500/15", text: "text-teal-600 dark:text-teal-400" },
  indigo: { bg: "bg-indigo-500/15", text: "text-indigo-600 dark:text-indigo-400" },
  violet: { bg: "bg-violet-500/15", text: "text-violet-600 dark:text-violet-400" },
  amber: { bg: "bg-amber-500/15", text: "text-amber-600 dark:text-amber-400" },
  emerald: { bg: "bg-emerald-500/15", text: "text-emerald-600 dark:text-emerald-400" },
  rose: { bg: "bg-rose-500/15", text: "text-rose-600 dark:text-rose-400" },
  lime: { bg: "bg-lime-500/15", text: "text-lime-600 dark:text-lime-400" },
  fuchsia: { bg: "bg-fuchsia-500/15", text: "text-fuchsia-600 dark:text-fuchsia-400" },
}

export function LabelBadge({ label }: LabelBadgeProps) {
  const colorKey = label.color.toLowerCase().replace(/^#.*/, "")
  const mapped = COLOR_MAP[colorKey]

  if (mapped) {
    return (
      <span
        className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium ${mapped.bg} ${mapped.text}`}
      >
        {label.name}
      </span>
    )
  }

  // Fallback for hex/rgb values — use inline styles
  return (
    <span
      className="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium"
      style={{
        backgroundColor: `${label.color}20`,
        color: label.color,
      }}
    >
      {label.name}
    </span>
  )
}
