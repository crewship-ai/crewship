"use client"

import { useCallback, useMemo, useState } from "react"
import { Activity, LineChart, RefreshCw } from "lucide-react"
import Link from "next/link"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { cn } from "@/lib/utils"
import { useJournalList } from "@/hooks/use-journal-list"
import { useWorkspace } from "@/hooks/use-workspace"
import { MetricCard } from "@/components/features/eval/metric-card"
import { RegressionAlert } from "@/components/features/eval/regression-alert"
import { TrajectoryDiff } from "@/components/features/eval/trajectory-diff"
import { formatDateTime, formatRelativeTime } from "@/lib/time"
import type { JournalEntry } from "@/lib/types/journal"

const EVAL_TYPES = "eval.run_started,eval.metric,eval.regression_detected"

/** Pull mission id out of a run entry — handles both `refs` and `payload`. */
function runMissionId(entry: JournalEntry): string | undefined {
  if (entry.mission_id) return entry.mission_id
  const p = entry.payload ?? {}
  if (typeof p.mission_id === "string") return p.mission_id as string
  const refs = entry.refs
  if (refs && typeof refs.mission_id === "string") return refs.mission_id as string
  return undefined
}

/** Extract a display-friendly eval signature (candidate vs baseline). */
function runSignature(entry: JournalEntry): string {
  const p = entry.payload ?? {}
  const candidate = typeof p.candidate === "string" ? (p.candidate as string) : typeof p.model === "string" ? (p.model as string) : ""
  const baseline = typeof p.baseline === "string" ? (p.baseline as string) : ""
  if (candidate && baseline) return `${candidate} vs ${baseline}`
  if (candidate) return candidate
  if (baseline) return `baseline ${baseline}`
  return "—"
}

/**
 * Quartermaster Eval dashboard. Reads the journal for `eval.*` entry
 * types and renders recent runs plus a regression banner and overview
 * metrics. Per-run detail opens a drawer with the trajectory diff.
 *
 * Layout pattern: "Top strip + grid cards" (pure dashboard, no sidebar).
 * See `docs/design/patterns.md` #3.
 */
export function EvalView() {
  const { workspaceId, loading: wsLoading } = useWorkspace()

  // 7-day window by default — matches "regressions detected in last 7d".
  const sinceIso = useMemo(() => new Date(Date.now() - 7 * 24 * 60 * 60 * 1000).toISOString(), [])
  const queryParams = useMemo<Record<string, string | undefined>>(
    () => ({ entry_type: EVAL_TYPES, since: sinceIso }),
    [sinceIso],
  )

  const { entries, loading, error, refresh } = useJournalList({
    workspaceId,
    params: queryParams,
    enabled: !wsLoading,
    limit: 200,
  })

  const runs = useMemo(() => entries.filter((e) => e.entry_type === "eval.run_started"), [entries])
  const metrics = useMemo(() => entries.filter((e) => e.entry_type === "eval.metric"), [entries])
  const regressions = useMemo(
    () => entries.filter((e) => e.entry_type === "eval.regression_detected"),
    [entries],
  )

  const [selectedRunId, setSelectedRunId] = useState<string | null>(null)
  const [sheetOpen, setSheetOpen] = useState(false)

  const openRun = useCallback((entry: JournalEntry) => {
    setSelectedRunId(entry.id)
    setSheetOpen(true)
  }, [])

  const selectedRun = useMemo(
    () => runs.find((r) => r.id === selectedRunId) ?? null,
    [runs, selectedRunId],
  )

  // Sibling metrics belonging to the selected run — grouped by trace id if
  // present, else by mission id, else we show everything.
  const selectedRunMetrics = useMemo(() => {
    if (!selectedRun) return [] as JournalEntry[]
    const trace = selectedRun.trace_id
    const missionId = runMissionId(selectedRun)
    return metrics.filter((m) => {
      if (trace && m.trace_id === trace) return true
      if (missionId && runMissionId(m) === missionId) return true
      return false
    })
  }, [metrics, selectedRun])

  // Tool success rate from metric payloads. Looks for metrics named
  // "tool_success_rate" and averages them — if none present, we just show "—".
  const avgToolSuccess = useMemo(() => {
    const values: number[] = []
    for (const m of metrics) {
      const p = m.payload ?? {}
      const name = typeof p.name === "string" ? (p.name as string) : typeof p.metric === "string" ? (p.metric as string) : ""
      if (name !== "tool_success_rate") continue
      const v = typeof p.candidate === "number" ? p.candidate : typeof p.value === "number" ? p.value : null
      if (v !== null) values.push(v as number)
    }
    if (values.length === 0) return null
    return values.reduce((a, b) => a + b, 0) / values.length
  }, [metrics])

  return (
    <div className="flex flex-col h-full bg-background">
      {/* ---- Top strip (h-9) ---- */}
      <div className="shrink-0 z-20 flex items-center h-9 bg-card border-b border-border/60 px-3 gap-2">
        <LineChart className="h-3.5 w-3.5 text-foreground/60 shrink-0" />
        <h1 className="text-body font-medium text-foreground/80 truncate">Eval</h1>
        <Badge variant="outline" className="text-[10px] border-border/60 shrink-0">
          last 7d
        </Badge>
        <div className="flex-1" />
        <Button variant="outline" size="sm" className="h-7 px-2.5 text-xs" onClick={() => refresh()} disabled={loading}>
          <RefreshCw className={cn("h-3 w-3 mr-1.5", loading && "animate-spin")} />
          Refresh
        </Button>
      </div>

      {/* ---- Main content ---- */}
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="p-4 md:p-5 space-y-4">
          {error && (
            <div className="rounded-lg border border-red-500/40 bg-red-500/5 px-3 py-2 text-[12px] text-red-300 flex items-center justify-between gap-2">
              <span>Couldn&apos;t load eval data ({error}).</span>
              <Button variant="outline" size="sm" className="h-6 px-2 text-[11px]" onClick={() => refresh()}>
                Retry
              </Button>
            </div>
          )}

          <RegressionAlert regressions={regressions} onView={openRun} />

          <section className="grid grid-cols-2 lg:grid-cols-3 gap-3">
            <MetricCard label="Missions evaluated" value={runs.length} subtitle="last 7 days" />
            <MetricCard
              label="Regressions"
              value={regressions.length}
              subtitle="last 7 days"
              deltaDirection={regressions.length > 0 ? "up" : "flat"}
              deltaLabel={regressions.length > 0 ? "needs review" : undefined}
              upIsGood={false}
            />
            <MetricCard
              label="Avg tool success"
              value={avgToolSuccess === null ? "—" : `${(avgToolSuccess * 100).toFixed(1)}%`}
              subtitle={`${metrics.length} metric entries`}
            />
          </section>

          <Card className="py-0 gap-0 overflow-hidden">
            <CardHeader className="px-4 py-2 border-b border-border/50">
              <CardTitle className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground flex items-center justify-between">
                <span className="flex items-center gap-2">
                  <Activity className="h-3.5 w-3.5 text-muted-foreground" />
                  Recent eval runs
                </span>
                <span className="text-[11px] text-muted-foreground font-mono tabular-nums font-normal normal-case tracking-normal">
                  {runs.length}
                </span>
              </CardTitle>
            </CardHeader>
            <CardContent className="p-0">
              {runs.length === 0 && !loading && !error ? (
                <div className="py-12 text-center">
                  <div className="text-sm font-medium text-foreground/80">No eval runs yet</div>
                  <div className="mt-1 text-[11px] text-muted-foreground max-w-sm mx-auto">
                    Kick off a replay to compare a candidate model against its baseline.
                  </div>
                </div>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="text-[10px] uppercase tracking-wider text-muted-foreground">Started</TableHead>
                      <TableHead className="text-[10px] uppercase tracking-wider text-muted-foreground">Mission</TableHead>
                      <TableHead className="text-[10px] uppercase tracking-wider text-muted-foreground">Signature</TableHead>
                      <TableHead className="text-[10px] uppercase tracking-wider text-muted-foreground">Status</TableHead>
                      <TableHead />
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {runs.map((run) => {
                      const missionId = runMissionId(run)
                      const status =
                        typeof run.payload?.status === "string" ? (run.payload.status as string) : "running"
                      return (
                        <TableRow
                          key={run.id}
                          className="cursor-pointer"
                          onClick={() => openRun(run)}
                        >
                          <TableCell className="text-[11px] text-muted-foreground font-mono tabular-nums">
                            {formatRelativeTime(run.ts)}
                          </TableCell>
                          <TableCell className="text-[12px]">
                            {missionId ? (
                              <Link
                                href={`/missions/${missionId}/timeline`}
                                className="font-mono text-primary hover:underline"
                                onClick={(e) => e.stopPropagation()}
                              >
                                {missionId.slice(0, 8)}
                              </Link>
                            ) : (
                              <span className="text-muted-foreground">—</span>
                            )}
                          </TableCell>
                          <TableCell className="text-[12px] text-foreground/80 truncate max-w-xs">
                            {runSignature(run)}
                          </TableCell>
                          <TableCell>
                            <Badge
                              variant="outline"
                              className={cn(
                                "text-[10px] border",
                                status === "completed" && "bg-emerald-500/15 text-emerald-300 border-emerald-500/40",
                                status === "running" && "bg-blue-500/15 text-blue-300 border-blue-500/40",
                                status === "failed" && "bg-red-500/15 text-red-300 border-red-500/40",
                              )}
                            >
                              {status}
                            </Badge>
                          </TableCell>
                          <TableCell className="text-right">
                            <span className="text-[11px] text-muted-foreground">View</span>
                          </TableCell>
                        </TableRow>
                      )
                    })}
                  </TableBody>
                </Table>
              )}
            </CardContent>
          </Card>
        </div>
      </div>

      <Sheet open={sheetOpen} onOpenChange={setSheetOpen}>
        <SheetContent className="sm:max-w-xl w-full">
          {selectedRun && (
            <>
              <SheetHeader>
                <SheetTitle className="text-sm font-medium flex items-center gap-2">
                  Eval run
                  <span className="text-[10px] font-mono text-muted-foreground tabular-nums">
                    {selectedRun.id.slice(0, 8)}
                  </span>
                </SheetTitle>
                <SheetDescription className="text-xs">
                  Started {formatDateTime(selectedRun.ts)} · {runSignature(selectedRun)}
                </SheetDescription>
              </SheetHeader>
              <div className="px-4 space-y-4 overflow-y-auto flex-1 min-h-0">
                <Card className="py-0 gap-0 overflow-hidden">
                  <CardHeader className="px-3 py-2 border-b border-border/50">
                    <CardTitle className="text-[10px] uppercase tracking-wider text-muted-foreground font-semibold">
                      Trajectory diff
                    </CardTitle>
                  </CardHeader>
                  <CardContent className="p-3">
                    <TrajectoryDiff run={selectedRun} metrics={selectedRunMetrics} />
                  </CardContent>
                </Card>
                {selectedRun.payload && Object.keys(selectedRun.payload).length > 0 && (
                  <Card className="py-0 gap-0 overflow-hidden">
                    <CardHeader className="px-3 py-2 border-b border-border/50">
                      <CardTitle className="text-[10px] uppercase tracking-wider text-muted-foreground font-semibold">
                        Run payload
                      </CardTitle>
                    </CardHeader>
                    <CardContent className="p-0">
                      <pre className="max-h-64 overflow-auto bg-muted/30 p-2 text-[10px] font-mono text-muted-foreground">
                        {JSON.stringify(selectedRun.payload, null, 2)}
                      </pre>
                    </CardContent>
                  </Card>
                )}
              </div>
            </>
          )}
        </SheetContent>
      </Sheet>
    </div>
  )
}
