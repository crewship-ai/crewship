/**
 * Centralized color definitions — single source of truth.
 * All status, crew, edge, priority, and semantic colors live here.
 */

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
