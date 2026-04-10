"use client"

import Link from "next/link"
import { cn } from "@/lib/utils"

export interface ProjectProgressEntry {
  id: string
  name: string
  color: string
  issueCount: number
  completedCount: number
  href?: string
}

interface ProjectProgressProps {
  projects: ProjectProgressEntry[]
  emptyLabel?: string
}

export function ProjectProgress({ projects, emptyLabel = "No active projects" }: ProjectProgressProps) {
  if (projects.length === 0) {
    return (
      <div className="flex items-center justify-center h-[140px] text-[11px] text-muted-foreground/50">
        {emptyLabel}
      </div>
    )
  }

  return (
    <div className="flex flex-col">
      {projects.map((p) => {
        const pct = p.issueCount > 0 ? Math.round((p.completedCount / p.issueCount) * 100) : 0
        const content = (
          <div className="py-2 border-b border-border/60 last:border-b-0">
            <div className="flex items-center justify-between mb-1 text-[11px]">
              <span className="inline-flex items-center gap-2 text-foreground/80 truncate min-w-0">
                <span className="w-2 h-2 rounded-sm shrink-0" style={{ background: p.color }} />
                <span className="truncate">{p.name}</span>
              </span>
              <span className="font-mono text-[10px] text-muted-foreground/70 tabular-nums shrink-0 ml-2">
                {p.completedCount}/{p.issueCount} · {pct}%
              </span>
            </div>
            <div className="h-1 rounded-full bg-white/[0.05] overflow-hidden">
              <div
                className="h-full rounded-full transition-all duration-500"
                style={{ width: `${pct}%`, background: p.color }}
              />
            </div>
          </div>
        )
        return p.href ? (
          <Link key={p.id} href={p.href} className={cn("block hover:bg-white/[0.02] -mx-2 px-2 rounded")}>
            {content}
          </Link>
        ) : (
          <div key={p.id}>{content}</div>
        )
      })}
    </div>
  )
}
