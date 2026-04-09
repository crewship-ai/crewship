"use client"

import type { ProjectStatus } from "@/lib/types/mission"

export function ProjectStatusIcon({ status, className }: { status: ProjectStatus; className?: string }) {
  switch (status) {
    case "backlog":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" strokeDasharray="3 3" opacity="0.5" />
        </svg>
      )
    case "planned":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" opacity="0.6" />
        </svg>
      )
    case "in_progress":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" opacity="0.3" />
          <path d="M8 2a6 6 0 0 1 6 6" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
        </svg>
      )
    case "paused":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" opacity="0.4" />
          <rect x="6" y="5" width="1.5" height="6" rx="0.5" fill="currentColor" opacity="0.6" />
          <rect x="8.5" y="5" width="1.5" height="6" rx="0.5" fill="currentColor" opacity="0.6" />
        </svg>
      )
    case "completed":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" fill="currentColor" opacity="0.15" stroke="currentColor" strokeWidth="1.5" />
          <path d="M5.5 8l2 2 3.5-3.5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      )
    case "cancelled":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" opacity="0.3" />
          <path d="M5.5 5.5l5 5M10.5 5.5l-5 5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
        </svg>
      )
    default:
      return null
  }
}

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
