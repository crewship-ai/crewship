"use client"

import * as React from "react"
import Link from "next/link"

import type { AgentSummary } from "@/app/(dashboard)/dashboard-types"
import { CrewIcon } from "@/components/ui/crew-icon"
import { entityHref } from "@/lib/entity-links"
import { cn } from "@/lib/utils"
import type { FleetHealthRow } from "./dashboard-overview"
import type { RunVolumeBucket } from "./run-volume-chart"
import { DashCard, DashNote, TEXT_LINK } from "./dash-card"

export interface FleetCard {
  row: FleetHealthRow
  agents: AgentSummary[]
  /** Runs per bucket for this crew, in bucket order. */
  runSeries: number[]
  runsTotal: number
}

/** Pure: one entry per crew, from what the page already fetched. Agents are
 *  matched by crew id or slug (agents carry both). */
export function deriveFleetBoard({
  rows,
  agents,
  buckets,
}: {
  rows: FleetHealthRow[]
  agents: AgentSummary[]
  buckets: RunVolumeBucket[]
}): FleetCard[] {
  return rows.map((row) => {
    const crewAgents = agents.filter((a) => a.crew_id === row.crew.id || a.crew?.slug === row.crew.slug)
    const series = buckets.map((b) => Number(b[row.crew.id] ?? 0))
    return {
      row,
      agents: crewAgents,
      runSeries: series,
      runsTotal: series.reduce((sum, v) => sum + v, 0),
    }
  })
}

/** Pure: crews whose status is only a missing tool (credential gap). */
export function toolGapCrews(cards: FleetCard[]): string[] {
  return cards.filter((card) => card.row.status === "Needs tool").map((card) => card.row.crew.name)
}

const TONE_RANK: Record<FleetHealthRow["tone"], number> = { danger: 0, warn: 1, blue: 2, muted: 3, success: 4 }

/** Pure: the order the grid shows crews in. Whatever needs a person comes
 *  first (errors, then tool gaps), then whatever is busy, then the idle
 *  majority — by activity, then by name so the order is stable between
 *  refreshes. On a fleet of a hundred it is the difference between a
 *  dashboard and a directory. */
export function prioritiseFleet(cards: FleetCard[]): FleetCard[] {
  return [...cards].sort((a, b) =>
    TONE_RANK[a.row.tone] - TONE_RANK[b.row.tone] ||
    b.runsTotal - a.runsTotal ||
    // A crew with people in it before a shell with none.
    b.agents.length - a.agents.length ||
    a.row.crew.name.localeCompare(b.row.crew.name),
  )
}

/** Pure: a crew's state as the grid says it — a few words in one tone. */
export function crewChipStatus(row: FleetHealthRow): { text: string; tone: "ok" | "warn" | "bad" | "mut" } {
  if (row.tone === "danger") return { text: row.status, tone: "bad" }
  if (row.tone === "warn") return { text: row.status, tone: "warn" }
  if (row.runningAgents > 0) return { text: `${row.runningAgents} working`, tone: "ok" }
  if (row.agents === 0) return { text: "No agents", tone: "mut" }
  return { text: "Idle", tone: "mut" }
}

const CHIP_TONE = {
  ok: "text-chip-ok-fg",
  warn: "text-chip-warn-fg",
  bad: "text-chip-danger-fg",
  mut: "text-muted-foreground",
} as const

/** How many crews the grid shows (v67 lists six); the header link opens the rest. */
const GRID_LIMIT = 6

export function FleetBoard({ cards: unordered }: { cards: FleetCard[] }) {
  const cards = React.useMemo(() => prioritiseFleet(unordered), [unordered])
  const toolGaps = toolGapCrews(cards)
  // One missing credential used to light the same warning on every crew that
  // shares it. Two or more fold into one line with one action; the chips
  // keep the words, quietly.
  const foldToolGaps = toolGaps.length > 1
  if (cards.length === 0) return null
  const shown = cards.slice(0, GRID_LIMIT)
  return (
    <DashCard title="Your crews" concept="crews" link={{ label: "Crews", href: "/crews" }} data-testid="dashboard-fleet-board">
      <div className="grid grid-cols-2 gap-px border-t border-border bg-border">
        {shown.map((card) => {
          const { row } = card
          const status = crewChipStatus(row)
          const quiet = foldToolGaps && row.status === "Needs tool"
          return (
            <Link
              key={row.crew.id}
              href={entityHref({ kind: "crew", slug: row.crew.slug })}
              data-testid="dashboard-fleet-card"
              className="flex min-w-0 items-center gap-2.5 bg-card px-3 py-2.5 transition-colors hover:bg-muted focus-visible:outline-2 focus-visible:outline-primary coarse:min-h-[44px]"
            >
              <CrewIcon icon={row.crew.icon || "users"} color={row.crew.color} size="sm" />
              <span className="min-w-0">
                <b className="block truncate text-body font-medium text-foreground">{row.crew.name}</b>
                <small data-tone={quiet ? "mut" : status.tone} className={cn("block truncate text-label", CHIP_TONE[quiet ? "mut" : status.tone])}>
                  {status.text}
                </small>
              </span>
            </Link>
          )
        })}
        {/* An odd count leaves a hole in the hairline grid; fill it. */}
        {shown.length % 2 === 1 && <span className="bg-card" aria-hidden />}
      </div>
      {foldToolGaps && (
        <DashNote
          tone="muted"
          action={<Link href="/credentials" className={cn("font-medium", TEXT_LINK)}>Connect</Link>}
        >
          <span className="text-chip-warn-fg">{toolGaps.length} crews need a tool</span> · {toolGaps.join(", ")}
        </DashNote>
      )}
    </DashCard>
  )
}
