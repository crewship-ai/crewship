"use client"

// The STATUS rows — the Routines rail's words, icons and count pills (#2979).
// Shared by the rail and its routine focus (#2998), so the two cannot drift.

import * as React from "react"
import { Activity, CheckCircle2, CircleSlash, Layers, PauseCircle, XCircle } from "lucide-react"

import { SidebarRow } from "@/components/layout/sidebar-kit"
import type { RailScope, RailStatusRow } from "@/lib/activity-rail"
import { cn } from "@/lib/utils"

/** The Routines rail's glyph for each status row. */
const STATUS_ICON: Record<RailScope, React.ComponentType<{ className?: string }>> = {
  all: Layers,
  waiting: PauseCircle,
  active: Activity,
  done: CheckCircle2,
  stopped: CircleSlash,
  failed: XCircle,
}

export function StatusRows({
  rows,
  scope,
  onPick,
}: {
  rows: RailStatusRow[]
  scope: string
  onPick: (key: RailStatusRow["key"]) => void
}) {
  return (
    <div role="region" aria-label="Status">
      {rows.map((r) => {
        const Icon = STATUS_ICON[r.key]
        const isSelected = scope === r.key
        const empty = r.count === 0 && !isSelected
        return (
          <SidebarRow key={r.key} as="div" selected={isSelected} onSelect={() => onPick(r.key)}>
            <Icon className={cn("h-3.5 w-3.5 shrink-0", r.tone, empty && "opacity-40")} />
            <span className={cn("flex-1 truncate", empty ? "text-muted-foreground-soft" : "text-foreground/80")}>
              {r.label}
            </span>
            <span
              className={cn(
                "rounded-full px-1.5 py-px text-[10px] tabular-nums",
                r.count === 0
                  ? "text-muted-foreground-soft"
                  : isSelected
                    ? "bg-primary/15 text-primary-hover"
                    : "bg-foreground/[0.05] text-muted-foreground",
              )}
            >
              {r.count}
            </span>
          </SidebarRow>
        )
      })}
    </div>
  )
}
