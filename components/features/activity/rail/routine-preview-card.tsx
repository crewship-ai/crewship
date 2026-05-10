"use client"

import { useMemo } from "react"
import Link from "next/link"
import { ExternalLink } from "lucide-react"
import { HoverCard, HoverCardContent, HoverCardTrigger } from "@/components/ui/hover-card"
import { formatDuration, relTime } from "@/lib/activity/format-time"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"

// RoutinePreviewCard — hover-triggered card with rollup stats for one
// routine. Shown when the user hovers a routine row in the rail tree;
// gives the at-a-glance "is this routine healthy" signal without
// expanding the row + scrolling through individual runs.
//
// Stats are computed from runs already in the page memory (we have
// up to 100 recent runs), so this is zero extra fetches.

interface RoutinePreviewCardProps {
  slug: string
  name: string
  crewName?: string
  cronExpr?: string
  runs: PipelineRun[]
  children: React.ReactNode
}

export function RoutinePreviewCard({
  slug,
  name,
  crewName,
  cronExpr,
  runs,
  children,
}: RoutinePreviewCardProps) {
  const stats = useMemo(() => computeStats(runs), [runs])

  return (
    <HoverCard openDelay={300} closeDelay={50}>
      <HoverCardTrigger asChild>{children}</HoverCardTrigger>
      <HoverCardContent side="right" align="start" className="w-[280px] p-0 text-xs">
        <div className="border-b border-border px-3 py-2">
          <div className="flex items-center gap-1.5">
            <span className="truncate font-medium">{name}</span>
          </div>
          <div className="mt-0.5 truncate font-mono text-[10px] text-muted-foreground/70">
            {slug}
          </div>
        </div>

        <dl className="space-y-1.5 px-3 py-2 text-[11px]">
          {cronExpr && (
            <Row label="Cron pattern">
              <span className="font-mono">{cronExpr}</span>
            </Row>
          )}
          {crewName && <Row label="Crew">{crewName}</Row>}
          <Row label="Recent runs">
            {stats.total === 0 ? (
              <span className="text-muted-foreground/60">none</span>
            ) : (
              <>
                <span className="tabular-nums">{stats.total}</span>
                {stats.completed > 0 && (
                  <span className="ml-1 text-emerald-400">{stats.completed} ✓</span>
                )}
                {stats.failed > 0 && (
                  <span className="ml-1 text-rose-400">{stats.failed} ✗</span>
                )}
                {stats.active > 0 && (
                  <span className="ml-1 text-blue-400">{stats.active} active</span>
                )}
              </>
            )}
          </Row>
          {stats.avgCost !== null && (
            <Row label="Avg cost">
              <span className="font-mono">${stats.avgCost.toFixed(4)}</span>
            </Row>
          )}
          {stats.avgDurationMs !== null && (
            <Row label="Avg duration">
              <span className="font-mono">{formatDuration(stats.avgDurationMs)}</span>
            </Row>
          )}
          {stats.lastAt && (
            <Row label="Last run">
              <span>{relTime(stats.lastAt)}</span>
            </Row>
          )}
        </dl>

        <div className="flex gap-1 border-t border-border p-2">
          <Link
            href={`/routines?slug=${encodeURIComponent(slug)}`}
            className="flex flex-1 items-center justify-center gap-1 rounded bg-blue-500/15 px-2 py-1 text-[10px] text-blue-300 hover:bg-blue-500/25"
          >
            <ExternalLink className="h-3 w-3" />
            Open routine
          </Link>
        </div>
      </HoverCardContent>
    </HoverCard>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-2">
      <dt className="text-muted-foreground/60">{label}</dt>
      <dd className="text-right text-foreground/80">{children}</dd>
    </div>
  )
}

interface RoutineStats {
  total: number
  completed: number
  failed: number
  active: number
  avgCost: number | null
  avgDurationMs: number | null
  lastAt?: string
}

function computeStats(runs: PipelineRun[]): RoutineStats {
  if (runs.length === 0) {
    return {
      total: 0,
      completed: 0,
      failed: 0,
      active: 0,
      avgCost: null,
      avgDurationMs: null,
    }
  }
  let completed = 0
  let failed = 0
  let active = 0
  let costSum = 0
  let costN = 0
  let durSum = 0
  let durN = 0
  let lastAt: string | undefined
  for (const r of runs) {
    if (r.status === "completed") completed++
    else if (r.status === "failed" || r.status === "cancelled" || r.status === "interrupted") failed++
    else if (r.status === "running" || r.status === "queued" || r.status === "paused") active++
    if (r.cost_usd > 0) {
      costSum += r.cost_usd
      costN++
    }
    if (r.duration_ms > 0) {
      durSum += r.duration_ms
      durN++
    }
    if (!lastAt || (r.started_at && r.started_at > lastAt)) lastAt = r.started_at
  }
  return {
    total: runs.length,
    completed,
    failed,
    active,
    avgCost: costN > 0 ? costSum / costN : null,
    avgDurationMs: durN > 0 ? durSum / durN : null,
    lastAt,
  }
}
