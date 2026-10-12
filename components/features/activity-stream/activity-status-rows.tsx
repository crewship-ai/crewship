"use client"

// The STATUS rows — the Routines rail's words, icons and count pills (#2979).
// Shared by the rail, its routine focus (#2998) and the ledgers' rail (#3017),
// so they cannot drift.

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

/** A status row of any rail: the Activity scopes, or a ledger's (#3017). */
export interface StatusRow<K extends string = string> {
  key: K
  label: string
  tone: string
  count: number
  /** Defaults to the Routines rail's glyph for an Activity scope. */
  icon?: React.ComponentType<{ className?: string }>
}

export function StatusRows<K extends string = RailStatusRow["key"]>({
  rows,
  scope,
  onPick,
  label = "Status",
}: {
  rows: StatusRow<K>[]
  scope: string
  onPick: (key: K) => void
  /** The region's name: "Status", or "Decision" on the deliveries rail. */
  label?: string
}) {
  return (
    <div role="region" aria-label={label}>
      {rows.map((r) => {
        const Icon = r.icon ?? STATUS_ICON[r.key as unknown as RailScope] ?? Layers
        const isSelected = scope === r.key
        const empty = r.count === 0 && !isSelected
        return (
          <SidebarRow key={r.key} as="div" selected={isSelected} onSelect={() => onPick(r.key)}>
            <Icon className={cn("h-3.5 w-3.5 shrink-0", r.tone, empty && "opacity-40")} />
            <span className={cn("flex-1 truncate", empty ? "text-muted-foreground-soft" : "text-foreground/80")}>
              {r.label}
            </span>
            <CountPill count={r.count} selected={isSelected} />
          </SidebarRow>
        )
      })}
    </div>
  )
}

/** The rail's count pill — also beside the ledgers' agent and event rows. */
export function CountPill({ count, selected }: { count: number; selected: boolean }) {
  return (
    <span
      className={cn(
        "self-center rounded-full px-1.5 py-px text-[10px] tabular-nums",
        count === 0
          ? "text-muted-foreground-soft"
          : selected
            ? "bg-primary/15 text-primary-hover"
            : "bg-foreground/[0.05] text-muted-foreground",
      )}
    >
      {count}
    </span>
  )
}
