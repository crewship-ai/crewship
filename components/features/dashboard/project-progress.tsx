"use client"

import Link from "next/link"
import { Progress } from "@/components/ui/progress"

export interface ProjectProgressEntry {
  id: string
  name: string
  /** Hex color string picked during project creation (arbitrary user input). */
  color: string
  issueCount: number
  completedCount: number
  href?: string
}

interface ProjectProgressProps {
  projects: ProjectProgressEntry[]
  emptyLabel?: string
}

/**
 * Project colors come from user-picked hex values at project creation, so they
 * can't be mapped to a fixed Tailwind palette class. Instead we publish the
 * runtime color as a CSS custom property (`--project-color`) on each row and
 * let Tailwind's arbitrary custom-property syntax consume it via
 * `bg-(--project-color)`. This keeps all visual rules in Tailwind class-space
 * and confines the single inline-style escape hatch to this one prop.
 */
export function ProjectProgress({ projects, emptyLabel = "No active projects" }: ProjectProgressProps) {
  if (projects.length === 0) {
    return (
      <div className="flex items-center justify-center h-[140px] text-[11px] text-muted-foreground-soft">
        {emptyLabel}
      </div>
    )
  }

  return (
    <div className="flex flex-col">
      {projects.map((p) => {
        // Clamp to [0, 100] — guards against laggy aggregates where
        // completedCount can briefly exceed issueCount.
        const pct = p.issueCount > 0
          ? Math.max(0, Math.min(100, Math.round((p.completedCount / p.issueCount) * 100)))
          : 0
        const content = (
          <div
            className="py-2 border-b border-border/60 last:border-b-0"
            style={{ "--project-color": p.color } as React.CSSProperties}
          >
            <div className="flex items-center justify-between mb-1 text-[11px]">
              <span className="inline-flex items-center gap-2 text-foreground/80 truncate min-w-0">
                <span className="w-2 h-2 rounded-sm shrink-0 bg-(--project-color)" aria-hidden />
                <span className="truncate">{p.name}</span>
              </span>
              <span className="font-mono text-[10px] text-muted-foreground tabular-nums shrink-0 ml-2">
                {p.completedCount}/{p.issueCount} · {pct}%
              </span>
            </div>
            <Progress
              value={pct}
              className="h-1 bg-white/[0.05]"
              indicatorClassName="bg-(--project-color) transition-all duration-500"
            />
          </div>
        )
        return p.href ? (
          <Link key={p.id} href={p.href} className="block hover:bg-white/[0.02] -mx-2 px-2 rounded">
            {content}
          </Link>
        ) : (
          <div key={p.id}>{content}</div>
        )
      })}
    </div>
  )
}
