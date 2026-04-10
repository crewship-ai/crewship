"use client"

import {
  CircleDashed,
  Circle,
  CircleDotDashed,
  CirclePause,
  CircleCheck,
  CircleX,
} from "lucide-react"
import { cn } from "@/lib/utils"
import type { ProjectStatus } from "@/lib/types/mission"

/**
 * Icon representing a project lifecycle status. Uses lucide-react icons so
 * the component obeys the repo-wide "only lucide icons in components/**"
 * rule; the opacity differences are expressed via Tailwind classes.
 */
export function ProjectStatusIcon({ status, className }: { status: ProjectStatus; className?: string }) {
  switch (status) {
    case "backlog":
      return <CircleDashed className={cn("opacity-50", className)} />
    case "planned":
      return <Circle className={cn("opacity-60", className)} />
    case "in_progress":
      return <CircleDotDashed className={className} />
    case "paused":
      return <CirclePause className={cn("opacity-70", className)} />
    case "completed":
      return <CircleCheck className={className} />
    case "cancelled":
      return <CircleX className={cn("opacity-60", className)} />
    default:
      return null
  }
}

/** Colored text badge displaying project health (at-risk, off-track, or no updates). */
export function HealthBadge({ health }: { health: string }) {
  switch (health) {
    case "at_risk":
      return <span className="text-[11px] text-amber-400">At risk</span>
    case "off_track":
      return <span className="text-[11px] text-red-400">Off track</span>
    default:
      return <span className="text-[11px] text-muted-foreground/40">No updates</span>
  }
}
