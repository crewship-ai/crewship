"use client"

import { useMemo } from "react"
import type { JournalEntry } from "@/lib/types/journal"
import {
  GROUP_COLOR,
  SEVERITY_COLOR,
  groupOf,
  severityOf,
} from "@/lib/journal-style"
import { LogsNetworkCard } from "./logs-network-card"

interface LogsStatsRailProps {
  /** All entries currently visible (after filters). */
  entries: JournalEntry[]
  /** id → display name lookup for resolving UUIDs. */
  agentLookup?: Record<string, string>
  /** Render the admin-only Network observability card (open ports, egress). */
  showNetworkCard?: boolean
}

/**
 * Right-side stats rail — derived from currently-visible entries.
 * Mirrors the Grafana Logs panel "metrics" sidebar.
 */
export function LogsStatsRail({ entries, agentLookup, showNetworkCard }: LogsStatsRailProps) {
  const stats = useMemo(() => deriveStats(entries), [entries])

  return (
    <div className="p-3 space-y-3 bg-background/60 overflow-y-auto h-full">
      <StatCard title="Severity mix">
        <div className="flex h-2 rounded-full overflow-hidden mb-2 bg-muted/40">
          {(["info", "notice", "warn", "error"] as const).map((s) => {
            const v = stats.sev[s]
            const total = entries.length
            const pct = total > 0 ? (v / total) * 100 : 0
            if (pct === 0) return null
            return (
              <div key={s} style={{ width: `${pct}%`, background: SEVERITY_COLOR[s] }} />
            )
          })}
        </div>
        <div className="text-[11px] font-mono space-y-0.5">
          {(["info", "notice", "warn", "error"] as const).map((s) => (
            <div key={s} className="flex justify-between">
              <span style={{ color: SEVERITY_COLOR[s] }}>● {s}</span>
              <span className="tabular-nums text-foreground/80">{stats.sev[s]}</span>
            </div>
          ))}
        </div>
      </StatCard>

      <StatCard title="Top types">
        {stats.topTypes.length === 0 ? (
          <Empty />
        ) : (
          <div className="space-y-1.5 text-[11px] font-mono">
            {stats.topTypes.map((row) => (
              <BarRow
                key={row.type}
                label={row.type}
                value={row.count}
                total={stats.maxTypeCount}
                color={GROUP_COLOR[groupOf(row.type)]}
              />
            ))}
          </div>
        )}
      </StatCard>

      <StatCard title="Top agents">
        {stats.topAgents.length === 0 ? (
          <Empty />
        ) : (
          <div className="space-y-1.5 text-[11px] font-mono">
            {stats.topAgents.map((row) => (
              <BarRow
                key={row.agent}
                label={agentLookup?.[row.agent] ?? shortenId(row.agent)}
                title={row.agent}
                value={row.count}
                total={stats.maxAgentCount}
                color="#94a3b8"
              />
            ))}
          </div>
        )}
      </StatCard>

      {showNetworkCard && <LogsNetworkCard entries={entries} />}

      <StatCard title="Last 60s rate">
        <div className="flex items-baseline gap-2">
          <span className="text-2xl font-mono tabular-nums text-foreground">
            {stats.eventsPerSec.toFixed(1)}
          </span>
          <span className="text-[11px] text-muted-foreground">events / sec</span>
        </div>
        <div className="text-[10px] text-muted-foreground/70 mt-1">
          {stats.last60s} events in the last minute
        </div>
      </StatCard>
    </div>
  )
}

function StatCard({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="rounded-md border border-border/50 bg-card/40 px-3 py-2">
      <div className="text-[10px] uppercase tracking-wider text-muted-foreground mb-2">
        {title}
      </div>
      {children}
    </div>
  )
}

function BarRow({
  label,
  title,
  value,
  total,
  color,
}: {
  label: string
  title?: string
  value: number
  total: number
  color: string
}) {
  const pct = total > 0 ? (value / total) * 100 : 0
  return (
    <div>
      <div className="flex items-center gap-2">
        <span
          className="inline-block h-2 w-2 rounded-sm shrink-0"
          style={{ background: color }}
        />
        <span className="flex-1 truncate text-foreground/85" title={title ?? label}>
          {label}
        </span>
        <span className="tabular-nums text-muted-foreground/85">{value}</span>
      </div>
      <div className="ml-4 mt-0.5 h-[3px] rounded-full bg-muted/30 overflow-hidden">
        <div className="h-full" style={{ width: `${pct}%`, background: color, opacity: 0.6 }} />
      </div>
    </div>
  )
}

function Empty() {
  return <div className="text-[11px] text-muted-foreground/60 italic">—</div>
}

/** Shorten an opaque id for display when no lookup name is available. */
function shortenId(id: string): string {
  if (id.length <= 12) return id
  return `${id.slice(0, 6)}…${id.slice(-4)}`
}

interface DerivedStats {
  sev: Record<"info" | "notice" | "warn" | "error", number>
  topTypes: Array<{ type: string; count: number }>
  topAgents: Array<{ agent: string; count: number }>
  maxTypeCount: number
  maxAgentCount: number
  last60s: number
  eventsPerSec: number
}

function deriveStats(entries: JournalEntry[]): DerivedStats {
  const sev = { info: 0, notice: 0, warn: 0, error: 0 } as Record<"info" | "notice" | "warn" | "error", number>
  const types = new Map<string, number>()
  const agents = new Map<string, number>()
  const now = Date.now()
  let last60s = 0

  for (const e of entries) {
    sev[severityOf(e.severity)] += 1
    types.set(e.entry_type, (types.get(e.entry_type) ?? 0) + 1)
    if (e.agent_id) agents.set(e.agent_id, (agents.get(e.agent_id) ?? 0) + 1)
    // Annotated entries carry _tsMs already; otherwise fall back to parse.
    const t = (e as JournalEntry & { _tsMs?: number })._tsMs ?? new Date(e.ts).getTime()
    if (!Number.isNaN(t) && now - t <= 60_000) last60s += 1
  }

  const topTypes = [...types.entries()]
    .sort((a, b) => b[1] - a[1])
    .slice(0, 6)
    .map(([type, count]) => ({ type, count }))

  const topAgents = [...agents.entries()]
    .sort((a, b) => b[1] - a[1])
    .slice(0, 6)
    .map(([agent, count]) => ({ agent, count }))

  return {
    sev,
    topTypes,
    topAgents,
    maxTypeCount: topTypes[0]?.count ?? 0,
    maxAgentCount: topAgents[0]?.count ?? 0,
    last60s,
    eventsPerSec: last60s / 60,
  }
}
