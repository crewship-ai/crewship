import {
  Check,
  Clock,
  Loader2,
  PauseCircle,
  XCircle,
  type LucideIcon,
} from "lucide-react"

// Run-status visual helpers — single source of truth for icon + tint
// per pipeline_run status. Both the legacy RunsView (table) and the
// new RunTimelineRail (left rail in /activity) used to inline these
// as private helpers; same map, slightly different output shape, and
// they drifted on every status addition. Lifted here so future status
// values land in one place.

export function statusIcon(status: string): LucideIcon {
  switch (status) {
    case "running":
    case "queued":
      return Loader2
    case "paused":
      return PauseCircle
    case "completed":
      return Check
    case "failed":
    case "cancelled":
    case "interrupted":
      return XCircle
    default:
      return Clock
  }
}

export interface StatusTint {
  /** Tailwind class for the status badge background */
  bg: string
  /** Tailwind class for the icon — animation included when applicable */
  icon: string
  /** Tailwind class for status text. Optional — the rail variant
   * doesn't render a status label and skips this field. */
  text: string
}

export function statusTint(status: string): StatusTint {
  switch (status) {
    case "running":
    case "queued":
      return {
        bg: "bg-blue-500/15",
        icon: "animate-spin text-blue-400",
        text: "text-blue-300",
      }
    case "paused":
      return {
        bg: "bg-amber-500/15",
        icon: "text-amber-400 animate-pulse",
        text: "text-amber-300",
      }
    case "completed":
      return {
        bg: "bg-emerald-500/15",
        icon: "text-emerald-400",
        text: "text-emerald-300",
      }
    case "failed":
    case "cancelled":
    case "interrupted":
      return {
        bg: "bg-rose-500/15",
        icon: "text-rose-400",
        text: "text-rose-300",
      }
    default:
      return {
        bg: "bg-white/[0.06]",
        icon: "text-muted-foreground",
        text: "text-muted-foreground",
      }
  }
}
