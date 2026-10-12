"use client"

import * as React from "react"
import Link from "next/link"
import { KeyRound, Users } from "lucide-react"

import type { AgentSummary } from "@/app/(dashboard)/dashboard-types"
import { formatCost } from "@/app/(dashboard)/dashboard-helpers"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import { StatusPill } from "@/components/ui/status-pill"
import { entityHref } from "@/lib/entity-links"
import { formatStatus } from "@/lib/format-status"
import { cn } from "@/lib/utils"
import type { FleetHealthRow } from "./dashboard-overview"
import type { RunVolumeBucket } from "./run-volume-chart"
import { ListScrollControls, useListScroll } from "./list-scroll-controls"

export interface FleetCard {
  row: FleetHealthRow
  agents: AgentSummary[]
  /** Metered spend for the window, null when paymaster has no row for the crew. */
  spendUsd: number | null
  /** Runs per bucket for this crew, in bucket order — the card's sparkline. */
  runSeries: number[]
  runsTotal: number
}

/** Pure: one card's worth of facts per crew, from what the page already
 *  fetched. Agents are matched by crew id or slug (agents carry both). */
export function deriveFleetBoard({
  rows,
  agents,
  spendByCrew,
  buckets,
}: {
  rows: FleetHealthRow[]
  agents: AgentSummary[]
  spendByCrew: ReadonlyMap<string, number> | null
  buckets: RunVolumeBucket[]
}): FleetCard[] {
  return rows.map((row) => {
    const crewAgents = agents.filter((a) => a.crew_id === row.crew.id || a.crew?.slug === row.crew.slug)
    const series = buckets.map((b) => Number(b[row.crew.id] ?? 0))
    return {
      row,
      agents: crewAgents,
      spendUsd: spendByCrew?.has(row.crew.id) ? spendByCrew.get(row.crew.id)! : null,
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

/** Pure: the order the board shows crews in. Whatever needs a person comes
 *  first (errors, then tool gaps), then whatever is busy, then the idle
 *  majority — by activity, then by name so the order is stable between
 *  refreshes. On a fleet of six this changes nothing visible; on a fleet of
 *  a hundred it is the difference between a dashboard and a directory. */
export function prioritiseFleet(cards: FleetCard[]): FleetCard[] {
  return [...cards].sort((a, b) =>
    TONE_RANK[a.row.tone] - TONE_RANK[b.row.tone] ||
    b.runsTotal - a.runsTotal ||
    // A crew with people in it before a shell with none — a hundred empty
    // "Crew 0xx" rows must not push the three real crews off the cards.
    b.agents.length - a.agents.length ||
    a.row.crew.name.localeCompare(b.row.crew.name),
  )
}

const AGENT_DOT: Record<string, string> = {
  RUNNING: "bg-primary shadow-[0_0_0_3px_rgba(30,123,254,0.25)]",
  ERROR: "bg-destructive",
  IDLE: "bg-muted-foreground",
  ACTIVE: "bg-muted-foreground",
}

export function fleetAgentStatus(agents: Pick<AgentSummary, "status">[]): string {
  if (agents.length === 0) return "no agents yet"
  const running = agents.filter((agent) => agent.status === "RUNNING").length
  const ready = agents.filter((agent) => agent.status === "IDLE" || agent.status === "ACTIVE").length
  const errors = agents.filter((agent) => agent.status === "ERROR").length
  const other = agents.length - running - ready - errors
  return [running && `${running} running`, ready && `${ready} ready`, errors && `${errors} in error`, other && `${other} unavailable`].filter(Boolean).join(" · ")
}

export function FleetBoard({ cards: unordered }: { cards: FleetCard[] }) {
  const cards = React.useMemo(() => prioritiseFleet(unordered), [unordered])
  const listScroll = useListScroll()
  const toolGaps = toolGapCrews(cards)
  const foldToolGaps = toolGaps.length > 1
  if (cards.length === 0) return null
  // One row per crew instead of a card each: the same facts (state, agents,
  // runs) in a fifth of the height, so the board fits beside the results
  // instead of pushing everything below the fold (#2539).
  return (
    <section aria-label="Your crews" data-testid="dashboard-fleet-board" className="rounded-card border border-border bg-card p-4 xl:flex xl:min-h-0 xl:flex-1 xl:flex-col">
      <div className="mb-3 flex items-center justify-between">
        <h2 className="eyebrow inline-flex items-center gap-1.5">
          <Users className="h-3.5 w-3.5" /> Your crews
        </h2>
        <span className="flex items-center gap-2 font-mono text-[11px] text-muted-foreground">
          {cards.length} {cards.length === 1 ? "crew" : "crews"}
          <ListScrollControls label="crews" controller={listScroll} />
          <Link href="/crews" className="text-primary-hover hover:underline">Crews →</Link>
        </span>
      </div>
      {/* One missing credential used to light the same warning on every crew
          that shares it. Two or more fold into one line with one action; the
          rows keep the word, in a neutral tone. */}
      {toolGaps.length > 1 && (
        <Link
          href="/credentials"
          className="group mb-2 flex items-center gap-3 rounded-xl border border-chip-warn-fg/25 bg-chip-warn-bg px-3 py-2 text-label text-chip-warn-fg transition-colors hover:border-chip-warn-fg/50"
        >
          <KeyRound className="h-3.5 w-3.5 shrink-0" aria-hidden />
          <span className="min-w-0 flex-1 truncate">
            <span className="font-semibold">{toolGaps.length} crews need a tool</span>
            <span className="opacity-80"> · {toolGaps.join(", ")}</span>
          </span>
          <span className="shrink-0 font-semibold group-hover:underline">Connect →</span>
        </Link>
      )}
      <div ref={listScroll.listRef} className="flex max-h-[350px] flex-col divide-y divide-border/50 overflow-y-auto overscroll-contain pr-1 xl:min-h-0 xl:max-h-none xl:flex-1" tabIndex={0} aria-label="All crews">
        {cards.map((card) => {
          const { row } = card
          return (
            <div
              key={row.crew.id}
              className="group flex shrink-0 items-center gap-2.5 rounded-md px-1 py-1.5 transition-colors hover:bg-foreground/[0.025] coarse:min-h-12"
              data-testid="dashboard-fleet-card"
            >
              <CrewIcon icon={row.crew.icon || "users"} color={row.crew.color} size="sm" />
              <span className="min-w-0 flex-1">
                <Link href={entityHref({ kind: "crew", slug: row.crew.slug })} className="block truncate text-body font-medium text-foreground hover:underline">
                  {row.crew.name}
                </Link>
                <span className="block truncate text-label text-muted-foreground">
                  {fleetAgentStatus(card.agents)} · {card.spendUsd == null ? `${card.runsTotal} ${card.runsTotal === 1 ? "run" : "runs"}` : `${formatCost(card.spendUsd)} · ${card.runsTotal} ${card.runsTotal === 1 ? "run" : "runs"}`}
                  {row.services.checked && row.services.total > 0 ? ` · ${row.services.running}/${row.services.total} services` : ""}
                </span>
              </span>
              <span className="hidden items-center gap-1 sm:flex">
                {card.agents.slice(0, 5).map((agent) => (
                  <Link key={agent.id} href={entityHref({ kind: "chat", agentSlug: agent.slug })} title={`${agent.name} · ${formatStatus(agent.status).label}`} className="relative">
                    <AgentAvatar seed={agent.slug} alt={agent.name} className="h-6 w-6 rounded-md bg-muted ring-1 ring-border" />
                    <span className={cn("absolute -bottom-0.5 -right-0.5 h-2 w-2 rounded-full border-2 border-card", AGENT_DOT[agent.status] ?? "bg-muted-foreground")} aria-hidden />
                  </Link>
                ))}
                {card.agents.length > 5 && <span className="text-micro text-muted-foreground">+{card.agents.length - 5}</span>}
              </span>
              <StatusPill tone={row.tone === "success" || (foldToolGaps && row.status === "Needs tool") ? "muted" : row.tone} label={row.status} live={row.tone === "blue"} className="shrink-0" />
            </div>
          )
        })}
      </div>

    </section>
  )
}
