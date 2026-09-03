"use client"

import * as React from "react"
import Link from "next/link"
import { motion, useReducedMotion } from "motion/react"
import { Bot, ChevronRight } from "lucide-react"

import type { AgentSummary } from "@/app/(dashboard)/dashboard-types"
import { crewColor, formatCost } from "@/app/(dashboard)/dashboard-helpers"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import { StatusPill } from "@/components/ui/status-pill"
import { entityHref } from "@/lib/entity-links"
import { formatStatus } from "@/lib/format-status"
import { cn } from "@/lib/utils"
import type { FleetHealthRow } from "./dashboard-overview"
import { Sparkline } from "@/components/ui/sparkline"
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

export function FleetBoard({ cards: unordered, workspaceId }: { cards: FleetCard[]; workspaceId: string | null }) {
  const reduce = useReducedMotion()
  const [showAll, setShowAll] = React.useState(false)
  const cards = React.useMemo(() => prioritiseFleet(unordered), [unordered])
  if (cards.length === 0) return null
  const featured = cards.slice(0, FLEET_CARD_LIMIT)
  const rest = cards.slice(FLEET_CARD_LIMIT)
  const restNeedingAttention = rest.filter((c) => c.row.tone === "danger" || c.row.tone === "warn").length
  return (
    <section aria-label="Fleet" data-testid="dashboard-fleet-board" className="flex flex-col gap-2.5">
      <div className="flex items-center justify-between px-0.5">
        <h2 className="inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-foreground/70">
          <Bot className="h-3.5 w-3.5 text-muted-foreground-soft" /> Fleet · {cards.length} {cards.length === 1 ? "crew" : "crews"}
        </h2>
        <Link href="/crews" className="font-mono text-[10px] text-primary-hover hover:underline">Crews →</Link>
      </div>
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
        {featured.map((card, index) => {
          const { row } = card
          const color = crewColor(row.crew.color)
          return (
            <motion.div
              key={row.crew.id}
              initial={reduce ? false : { opacity: 0, y: 10 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.34, ease: [0.22, 1, 0.36, 1], delay: reduce ? 0 : index * 0.05 }}
              whileHover={reduce ? undefined : { y: -2 }}
              className="group flex flex-col gap-3 rounded-xl border border-border/60 bg-card p-4 transition-colors hover:border-border"
              data-testid="dashboard-fleet-card"
            >
              <div className="flex items-center gap-3">
                <CrewIcon icon={row.crew.icon || "users"} color={row.crew.color} size="md" />
                <span className="min-w-0 flex-1">
                  <Link href={entityHref({ kind: "crew", slug: row.crew.slug })} className="block truncate text-body font-semibold tracking-tight text-foreground hover:underline">
                    {row.crew.name}
                  </Link>
                  <span className="block truncate text-label text-muted-foreground">
                    {card.agents.length} {card.agents.length === 1 ? "agent" : "agents"} · {row.detail}
                  </span>
                </span>
                <StatusPill tone={row.tone} label={row.status} live={row.tone === "blue"} />
              </div>

              <div className="flex items-center gap-2">
                <span className="flex items-center gap-1.5">
                  {card.agents.slice(0, 6).map((agent) => (
                    <Link key={agent.id} href={entityHref({ kind: "chat", agentSlug: agent.slug })} title={`${agent.name} · ${formatStatus(agent.status).label}`} className="relative">
                      <AgentAvatar seed={agent.slug} agentId={agent.id} workspaceId={workspaceId} alt={agent.name} className="h-7 w-7 rounded-lg bg-muted ring-1 ring-border" />
                      <span className={cn("absolute -bottom-0.5 -right-0.5 h-2.5 w-2.5 rounded-full border-2 border-card", AGENT_DOT[agent.status] ?? "bg-muted-foreground")} aria-hidden />
                    </Link>
                  ))}
                  {card.agents.length > 6 && <span className="text-micro text-muted-foreground">+{card.agents.length - 6}</span>}
                </span>
                <span className="ml-1 truncate text-label text-muted-foreground">
                  {row.runningAgents > 0 ? `${row.runningAgents} running · ${Math.max(0, card.agents.length - row.runningAgents)} idle` : card.agents.length > 0 ? "all idle" : "no agents yet"}
                </span>
              </div>

              <div className="flex items-center gap-3 border-t border-border/50 pt-3">
                <Sparkline values={card.runSeries} color={color} width={110} height={26} />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-body font-medium tabular-nums">
                    {card.spendUsd == null ? `${card.runsTotal} ${card.runsTotal === 1 ? "run" : "runs"}` : `${formatCost(card.spendUsd)} · ${card.runsTotal} ${card.runsTotal === 1 ? "run" : "runs"}`}
                  </span>
                  <span className="block truncate text-label text-muted-foreground">
                    {row.services.checked ? `${row.services.running}/${row.services.total} services` : "services unchecked"}
                  </span>
                </span>
                <Link href={entityHref({ kind: "crew", slug: row.crew.slug })} className="inline-flex items-center gap-1 text-label font-medium text-primary-hover">
                  Open <ChevronRight className="h-3.5 w-3.5" />
                </Link>
              </div>
            </motion.div>
          )
        })}
      </div>

      {rest.length > 0 && (
        <div className="rounded-xl border border-border/60 bg-card" data-testid="dashboard-fleet-rest">
          <button
            type="button"
            onClick={() => setShowAll((v) => !v)}
            aria-expanded={showAll}
            className="flex w-full items-center justify-between gap-3 px-4 py-2.5 text-left text-label"
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
            <div className="grid grid-cols-1 gap-x-4 border-t border-border/50 px-2 py-2 md:grid-cols-2 xl:grid-cols-3">
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
