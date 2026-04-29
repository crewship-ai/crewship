// Pure helpers + palettes shared by the dashboard page.

export const CREW_PALETTE: Record<string, string> = {
  blue: "rgb(96, 165, 250)",
  emerald: "rgb(52, 211, 153)",
  violet: "rgb(167, 139, 250)",
  amber: "rgb(251, 191, 36)",
  rose: "rgb(251, 113, 133)",
  cyan: "rgb(34, 211, 238)",
  lime: "rgb(163, 230, 53)",
  fuchsia: "rgb(232, 121, 249)",
}
export function crewColor(paletteId: string | null | undefined): string {
  return CREW_PALETTE[paletteId ?? ""] ?? "rgb(148, 163, 184)"
}

export const STATUS_PALETTE = {
  BACKLOG: "rgb(96, 165, 250)",
  TODO: "rgb(34, 211, 238)",
  IN_PROGRESS: "rgb(167, 139, 250)",
  REVIEW: "rgb(251, 191, 36)",
  COMPLETED: "rgb(52, 211, 153)",
  FAILED: "rgb(248, 113, 113)",
  CANCELLED: "rgb(148, 163, 184)",
} as const

export function formatCost(cost: number): string {
  if (cost === 0) return "$0.00"
  if (cost < 0.01) return "<$0.01"
  return `$${cost.toFixed(2)}`
}

export function formatRelativeShort(iso: string | null | undefined): string {
  if (!iso) return ""
  const ts = new Date(iso).getTime()
  if (isNaN(ts)) return ""
  const diffSec = Math.floor((Date.now() - ts) / 1000)
  if (diffSec < 60) return `${diffSec}s`
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m`
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h`
  return `${Math.floor(diffSec / 86400)}d`
}

