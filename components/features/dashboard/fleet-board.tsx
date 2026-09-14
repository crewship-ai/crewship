"use client"

import * as React from "react"
import Link from "next/link"
import { Users, ChevronRight } from "lucide-react"

import type { AgentSummary } from "@/app/(dashboard)/dashboard-types"
import { crewColor, formatCost } from "@/app/(dashboard)/dashboard-helpers"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import { StatusPill } from "@/components/ui/status-pill"
import { entityHref } from "@/lib/entity-links"
import { formatStatus } from "@/lib/format-status"
import { cn } from "@/lib/utils"
import type { FleetHealthRow } from "./dashboard-overview"
import type { RunVolumeBucket } from "./run-volume-chart"

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

/** How many crews get a full card. Past this the board switches to a dense
 *  list, because 100 cards is a wall nobody reads. */
export const FLEET_CARD_LIMIT = 6

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
  IDLE: "bg-success",
  ACTIVE: "bg-success",
}

export function fleetAgentStatus(agents: Pick<AgentSummary, "status">[]): string {
  if (agents.length === 0) return "no agents yet"
  const running = agents.filter((agent) => agent.status === "RUNNING").length
  const ready = agents.filter((agent) => agent.status === "IDLE" || agent.status === "ACTIVE").length
  const errors = agents.filter((agent) => agent.status === "ERROR").length
  const other = agents.length - running - ready - errors
  return [running && `${running} running`, ready && `${ready} ready`, errors && `${errors} in error`, other && `${other} unavailable`].filter(Boolean).join(" · ")
}

export function FleetBoard({ cards: unordered, workspaceId }: { cards: FleetCard[]; workspaceId: string | null }) {
  const [showAll, setShowAll] = React.useState(false)
  const cards = React.useMemo(() => prioritiseFleet(unordered), [unordered])
  if (cards.length === 0) return null
  const featured = cards.slice(0, FLEET_CARD_LIMIT)
  const rest = cards.slice(FLEET_CARD_LIMIT)
  const restNeedingAttention = rest.filter((c) => c.row.tone === "danger" || c.row.tone === "warn").length
  // One row per crew instead of a card each: the same facts (state, agents,
  // runs) in a fifth of the height, so the board fits beside the results
  // instead of pushing everything below the fold (#2539).
  return (
    <section aria-label="Your crews" data-testid="dashboard-fleet-board" className="rounded-xl border border-border/60 bg-card p-3">
      <div className="mb-2.5 flex items-center justify-between">
        <h2 className="inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-foreground/70">
          <Users className="h-3.5 w-3.5 text-muted-foreground-soft" /> Your crews
        </h2>
        <span className="flex items-center gap-2 font-mono text-[10px] text-muted-foreground">
          {cards.length} {cards.length === 1 ? "crew" : "crews"}
          <Link href="/crews" className="text-primary-hover hover:underline">Crews →</Link>
        </span>
      </div>
      <div className="flex flex-col divide-y divide-border/50">
        {featured.map((card) => {
          const { row } = card
          return (
            <div
              key={row.crew.id}
              className="group flex items-center gap-2.5 rounded-md px-1 py-1.5 transition-colors hover:bg-foreground/[0.025]"
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
                    <AgentAvatar seed={agent.slug} agentId={agent.id} workspaceId={workspaceId} alt={agent.name} className="h-6 w-6 rounded-md bg-muted ring-1 ring-border" />
                    <span className={cn("absolute -bottom-0.5 -right-0.5 h-2 w-2 rounded-full border-2 border-card", AGENT_DOT[agent.status] ?? "bg-muted-foreground")} aria-hidden />
                  </Link>
                ))}
                {card.agents.length > 5 && <span className="text-micro text-muted-foreground">+{card.agents.length - 5}</span>}
              </span>
              <StatusPill tone={row.tone} label={row.status} live={row.tone === "blue"} className="shrink-0" />
            </div>
          )
        })}
      </div>

      {rest.length > 0 && (
        <div className="mt-2 rounded-lg border border-border/60" data-testid="dashboard-fleet-rest">
          <button
            type="button"
            onClick={() => setShowAll((v) => !v)}
            aria-expanded={showAll}
            className="flex w-full items-center justify-between gap-3 px-3 py-2 text-left text-label"
          >
            <span className="text-muted-foreground">
              <span className="font-medium text-foreground/90">{rest.length} more {rest.length === 1 ? "crew" : "crews"}</span>
              {restNeedingAttention > 0
                ? <> · <span className="text-warn">{restNeedingAttention} need attention</span></>
                : " · all healthy"}
            </span>
            <span className="inline-flex items-center gap-1 text-primary-hover">
              {showAll ? "Hide" : "Show all"} <ChevronRight className={cn("h-3.5 w-3.5 transition-transform", showAll && "rotate-90")} />
            </span>
          </button>
          {showAll && (
            <div className="flex flex-col border-t border-border/50 px-1 py-1">
              {rest.map((card) => (
                <Link
                  key={card.row.crew.id}
                  href={entityHref({ kind: "crew", slug: card.row.crew.slug })}
                  className="flex items-center gap-2.5 rounded-md px-2 py-1.5 text-label transition-colors hover:bg-foreground/[0.03]"
                >
                  <span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: crewColor(card.row.crew.color) }} aria-hidden />
                  <span className="min-w-0 flex-1 truncate text-foreground/90">{card.row.crew.name}</span>
                  <span className="shrink-0 font-mono text-micro tabular-nums text-muted-foreground">{card.agents.length}a · {card.runsTotal}r</span>
                  <StatusPill tone={card.row.tone} label={card.row.status} />
                </Link>
              ))}
            </div>
          )}
        </div>
      )}
    </section>
  )
}
