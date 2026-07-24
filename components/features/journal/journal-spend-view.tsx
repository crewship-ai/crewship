"use client"

// JournalSpendView — the "Spend" tab inside /journal (#1404). A
// journal-native cost rollup, deliberately separate from the
// Paymaster spend surface (/api/v1/paymaster/*) — see
// docs/guides/crew-journal.mdx's Spend section for why two cost
// surfaces coexist. Not a repoint of the (currently unused)
// components/features/journal/spend-view.tsx, which is built against
// the Paymaster API with a different data shape.
//
// Sections:
//   1. KPI header    — total spend + window selector + truncated warning.
//   2. Spend-over-time chart — top-5 agents by cost, pivoted by day.
//   3. By-agent breakdown table.
//   4. Top routines / top runs — each row deep-links to /routines?slug=.

import { useMemo, useState } from "react"
import Link from "next/link"
import { DollarSign, ExternalLink } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { CostBurnChart, type CostBucket, type CostSeries } from "@/components/features/dashboard/cost-burn-chart"
import { useJournalSpend } from "@/hooks/use-journal-spend"
import { SPEND_WINDOWS, type SpendWindow, type SpendByAgentBucket, type SpendTopRow } from "@/lib/types/journal-spend"
import { cn } from "@/lib/utils"

const WINDOW_LABEL: Record<SpendWindow, string> = { "24h": "Last 24h", "7d": "Last 7 days", "30d": "Last 30 days" }

// Same 4-color cycle app(dashboard)/page.tsx's cost burn chart uses —
// keeps the two "spend over time" charts visually consistent.
const SERIES_PALETTE = ["rgb(167, 139, 250)", "rgb(34, 211, 238)", "rgb(52, 211, 153)", "rgb(251, 191, 36)", "rgb(248, 113, 113)"]

function formatCost(usd: number): string {
  if (usd === 0) return "$0.00"
  if (usd < 0.01) return `$${usd.toFixed(4)}`
  return `$${usd.toFixed(2)}`
}

interface Props {
  workspaceId: string | null
  workspaceLoading: boolean
}

export function JournalSpendView({ workspaceId, workspaceLoading }: Props) {
  const [window, setWindow] = useState<SpendWindow>("24h")
  const { data, loading, error } = useJournalSpend(workspaceId, window, 5)

  // Pivot by_agent (day×crew×agent rows) into CostBurnChart's
  // day-keyed bucket shape, one series per agent — top 5 by total
  // cost across the window, so a noisy long tail doesn't crowd out
  // the chart.
  const { buckets, series } = useMemo<{ buckets: CostBucket[]; series: CostSeries[] }>(() => {
    const rows = data?.by_agent ?? []
    if (rows.length === 0) return { buckets: [], series: [] }

    const totalByAgent = new Map<string, number>()
    for (const r of rows) {
      totalByAgent.set(r.agent_id, (totalByAgent.get(r.agent_id) ?? 0) + r.cost_usd)
    }
    const topAgentIds = Array.from(totalByAgent.entries())
      .sort((a, b) => b[1] - a[1])
      .slice(0, 5)
      .map(([id]) => id)
    const topSet = new Set(topAgentIds)

    const byDate = new Map<string, CostBucket>()
    for (const r of rows) {
      if (!topSet.has(r.agent_id)) continue
      const key = r.agent_id || "(unattributed)"
      const bucket = byDate.get(r.date) ?? { ts: r.date }
      bucket[key] = (Number(bucket[key]) || 0) + r.cost_usd
      byDate.set(r.date, bucket)
    }
    const sortedBuckets = Array.from(byDate.values()).sort((a, b) => String(a.ts).localeCompare(String(b.ts)))
    const seriesOut: CostSeries[] = topAgentIds.map((id, i) => ({
      key: id || "(unattributed)",
      label: id || "(unattributed)",
      color: SERIES_PALETTE[i % SERIES_PALETTE.length],
    }))
    return { buckets: sortedBuckets, series: seriesOut }
  }, [data])

  const byAgentSorted = useMemo(() => {
    const rows = data?.by_agent ?? []
    const totals = new Map<string, SpendByAgentBucket>()
    for (const r of rows) {
      const key = `${r.crew_id}|${r.agent_id}`
      const existing = totals.get(key)
      if (existing) {
        existing.cost_usd += r.cost_usd
        existing.call_count += r.call_count
      } else {
        totals.set(key, { ...r, date: "" })
      }
    }
    return Array.from(totals.values()).sort((a, b) => b.cost_usd - a.cost_usd)
  }, [data])

  if (workspaceLoading || !workspaceId) {
    return (
      <div className="flex-1 min-h-0 overflow-auto p-4">
        <Skeleton className="h-24 w-full mb-4" />
        <Skeleton className="h-48 w-full" />
      </div>
    )
  }

  return (
    <div className="flex-1 min-h-0 overflow-auto p-4 space-y-4">
      {/* KPI header */}
      <div className="flex items-center justify-between rounded-lg border border-border/60 bg-card/50 px-4 py-3">
        <div className="flex items-center gap-3">
          <DollarSign className="h-5 w-5 text-emerald-400" />
          <div>
            <div className="text-2xl font-semibold tabular-nums">
              {loading ? <Skeleton className="h-7 w-24" /> : formatCost(data?.total_cost_usd ?? 0)}
            </div>
            <div className="text-xs text-muted-foreground">total spend · {WINDOW_LABEL[window]}</div>
          </div>
          {data?.truncated && (
            <span className="ml-2 text-[11px] text-amber-400">
              window exceeds the aggregation cap — showing the most recent rows only
            </span>
          )}
        </div>
        <Select value={window} onValueChange={(v) => setWindow(v as SpendWindow)}>
          <SelectTrigger className="w-36">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {SPEND_WINDOWS.map((w) => (
              <SelectItem key={w} value={w}>{WINDOW_LABEL[w]}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {error && (
        <div className="rounded-lg border border-rose-500/30 bg-rose-500/5 px-4 py-3 text-sm text-rose-300">
          {error}
        </div>
      )}

      {/* Spend over time */}
      <div className="rounded-lg border border-border/60 bg-card/50 p-4">
        <h3 className="mb-2 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          Spend over time (top 5 agents)
        </h3>
        {loading ? (
          <Skeleton className="h-40 w-full" />
        ) : buckets.length === 0 ? (
          <p className="py-8 text-center text-sm text-muted-foreground">No spend recorded in this window.</p>
        ) : (
          <CostBurnChart buckets={buckets} series={series} height={180} />
        )}
      </div>

      {/* By-agent breakdown */}
      <div className="rounded-lg border border-border/60 bg-card/50">
        <h3 className="border-b border-border/40 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          By agent
        </h3>
        {loading ? (
          <div className="p-4"><Skeleton className="h-24 w-full" /></div>
        ) : byAgentSorted.length === 0 ? (
          <p className="px-4 py-6 text-center text-sm text-muted-foreground">No agent spend recorded in this window.</p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-left text-[11px] text-muted-foreground">
                <th className="px-4 py-2 font-normal">Agent</th>
                <th className="px-4 py-2 font-normal">Crew</th>
                <th className="px-4 py-2 text-right font-normal">Calls</th>
                <th className="px-4 py-2 text-right font-normal">Cost</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border/30">
              {byAgentSorted.map((r) => (
                <tr key={`${r.crew_id}|${r.agent_id}`}>
                  <td className="px-4 py-2 font-mono text-xs">{r.agent_id || "(unattributed)"}</td>
                  <td className="px-4 py-2 font-mono text-xs text-muted-foreground">{r.crew_id || "—"}</td>
                  <td className="px-4 py-2 text-right tabular-nums">{r.call_count}</td>
                  <td className="px-4 py-2 text-right font-mono tabular-nums">{formatCost(r.cost_usd)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* Top routines / top runs */}
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
        <SpendTopTable title="Top routines" rows={data?.top_routines ?? []} loading={loading} />
        <SpendTopTable title="Top runs" rows={data?.top_runs ?? []} loading={loading} />
      </div>
    </div>
  )
}

function SpendTopTable({ title, rows, loading }: { title: string; rows: SpendTopRow[]; loading: boolean }) {
  return (
    <div className="rounded-lg border border-border/60 bg-card/50">
      <h3 className="border-b border-border/40 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
        {title}
      </h3>
      {loading ? (
        <div className="p-4"><Skeleton className="h-24 w-full" /></div>
      ) : rows.length === 0 ? (
        <p className="px-4 py-6 text-center text-sm text-muted-foreground">Nothing in this window.</p>
      ) : (
        <ol className="divide-y divide-border/30">
          {rows.map((r, i) => (
            <li key={`${r.kind}-${r.id}`} className="flex items-center gap-2 px-4 py-2 text-sm">
              <span className="w-5 shrink-0 text-right text-xs text-muted-foreground tabular-nums">{i + 1}.</span>
              <Link
                href={`/routines?slug=${encodeURIComponent(r.label)}`}
                className={cn(
                  "flex min-w-0 flex-1 items-center gap-1.5 truncate font-mono text-xs text-foreground/85 hover:text-foreground hover:underline",
                )}
                title={`Open ${r.label} in the routine run tree`}
              >
                <span className="truncate">{r.label}</span>
                <ExternalLink className="h-3 w-3 shrink-0 opacity-50" />
              </Link>
              <span className="shrink-0 font-mono text-xs tabular-nums text-muted-foreground">{formatCost(r.cost_usd)}</span>
            </li>
          ))}
        </ol>
      )}
    </div>
  )
}
