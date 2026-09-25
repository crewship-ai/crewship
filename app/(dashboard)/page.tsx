"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import Link from "next/link"
import { LayoutDashboard, Radio } from "lucide-react"

import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import {
  AttentionStrip,
  heldForWorkspace,
  OutcomeKpis,
  UpNext,
  buildAttentionItems,
  deriveFleetHealth,
  kpisFromInsights,
} from "@/components/features/dashboard/dashboard-overview"
import { RunVolumeChart, type RunVolumeBucket, type RunVolumeSeries } from "@/components/features/dashboard/run-volume-chart"
import { RecipesEmptyState } from "@/components/features/dashboard/recipes-cards"
import { FleetBoard, deriveFleetBoard } from "@/components/features/dashboard/fleet-board"
import { DashboardResults } from "@/components/features/dashboard/dashboard-results"
import { PagesStrip } from "@/components/features/dashboard/pages-strip"
import { WelcomeChecklist } from "@/components/features/dashboard/welcome-checklist"
import { Appear } from "@/components/ui/detail"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { useActiveRoutineRuns } from "@/hooks/use-active-routine-runs"
import { usePipelineSchedules } from "@/hooks/use-pipeline-schedules"
import { useCredentialReadiness } from "@/hooks/use-credential-readiness"
import { useInbox } from "@/hooks/use-inbox"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import {
  useAgentSummaries,
  useCrewServiceSummaries,
  useCrewSpend,
  useCrewSummaries,
  useDashboardResults,
  useDashboardActiveRuns,
  useInvalidateDashboard,
  useMetricsTimeseries,
  useRunsInsights,
  useRuntimeCapacity,
  type TimeseriesParams,
} from "@/hooks/use-dashboard-data"
import type { DashboardWindow } from "./dashboard-types"
import { crewColor, foldRunVolumeSeries } from "./dashboard-helpers"
import { cn } from "@/lib/utils"
import { serverFetch } from "@/lib/server-base"

const WINDOW_LABELS: DashboardWindow[] = ["24h", "7d", "30d"]

function DashboardPeriodBar({ value, onChange }: { value: DashboardWindow; onChange: (value: DashboardWindow) => void }) {
  return (
    <div className="sticky top-0 z-30 flex min-h-10 items-center justify-between gap-3 border-b border-border/60 bg-card px-3 shadow-sm md:px-5">
      <div className="flex min-w-0 items-center gap-2">
        <LayoutDashboard aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-foreground/70" />
        <h1 className="truncate text-body font-medium text-foreground">Dashboard</h1>
      </div>
      <div className="flex items-center rounded-md border border-border/60 bg-background/50 p-0.5" role="group" aria-label="Dashboard time window">
        {WINDOW_LABELS.map((item) => (
          <Button key={item} type="button" variant="ghost" size="xs" aria-pressed={value === item}
            onClick={() => onChange(item)}
            className={cn("h-6 min-w-10 px-2 font-sans text-[11px] font-semibold tracking-[0.01em] tabular-nums", value === item && "bg-primary/15 text-primary-hover")}
          >{item}</Button>
        ))}
      </div>
    </div>
  )
}

function runVolumeParams(window: DashboardWindow): TimeseriesParams {
  return {
    metric: "runs_count",
    window,
    bucket: window === "24h" ? "1h" : "1d",
    group_by: "crew",
  }
}

export default function DashboardPage() {
  const { workspaceId, loading: workspaceLoading } = useWorkspace()
  const [onboardingChecked, setOnboardingChecked] = useState(false)
  const [firstAgentId, setFirstAgentId] = useState<string | null>(null)
  const [reportWindow, setReportWindow] = useState<DashboardWindow>("24h")

  useEffect(() => {
    serverFetch("/api/v1/onboarding/status")
      .then((response) => (response.ok ? response.json() : null))
      .then((data) => {
        if (data && !data.completed) {
          window.location.assign("/onboarding")
          return
        }
        setOnboardingChecked(true)
      })
      .catch(() => setOnboardingChecked(true))
  }, [])

  useEffect(() => {
    try {
      setFirstAgentId(window.localStorage.getItem("crewship.firstAgentId"))
    } catch {
      setFirstAgentId(null)
    }
  }, [])

  const queryOpts = { enabled: onboardingChecked }
  const agentsQ = useAgentSummaries(workspaceId, queryOpts)
  const crewsQ = useCrewSummaries(workspaceId, queryOpts)
  const reviewQ = useDashboardResults(workspaceId, "REVIEW", queryOpts)
  const inProgressQ = useDashboardResults(workspaceId, "IN_PROGRESS", queryOpts)
  const completedQ = useDashboardResults(workspaceId, "DONE,COMPLETED", queryOpts)
  const agentRunsQ = useDashboardActiveRuns(workspaceId, queryOpts)
  const insightsQ = useRunsInsights(workspaceId, reportWindow, queryOpts)
  const capacityQ = useRuntimeCapacity(queryOpts)
  const volumeParams = useMemo(() => runVolumeParams(reportWindow), [reportWindow])
  const volumeQ = useMetricsTimeseries(workspaceId, volumeParams, queryOpts)
  const spendQ = useCrewSpend(workspaceId, reportWindow, queryOpts)
  const agents = useMemo(() => agentsQ.data ?? [], [agentsQ.data])
  const crews = useMemo(() => crewsQ.data ?? [], [crewsQ.data])
  const services = useCrewServiceSummaries(workspaceId, crews, queryOpts)
  const activeRuns = useActiveRoutineRuns()
  const schedules = usePipelineSchedules(workspaceId)
  const readiness = useCredentialReadiness(workspaceId)
  const inbox = useInbox(workspaceId, "active")

  const invalidateDashboard = useInvalidateDashboard(workspaceId)
  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined)
  const debouncedRefresh = useCallback(() => {
    clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      invalidateDashboard()
    }, 220)
  }, [invalidateDashboard])

  useEffect(() => () => clearTimeout(debounceRef.current), [])
  useRealtimeEvent("run.started", debouncedRefresh)
  useRealtimeEvent("run.completed", debouncedRefresh)
  useRealtimeEvent("run.failed", debouncedRefresh)
  useRealtimeEvent("agent.status", debouncedRefresh)
  useRealtimeEvent("assignment.updated", debouncedRefresh)
  useRealtimeEvent("mission.updated", debouncedRefresh)
  useRealtimeEvent("issue.updated", debouncedRefresh)
  // A6 (#2125): these were emitted server-side and dropped by the realtime
  // allowlist, so a new/deleted/started issue never moved the dashboard's
  // mission counts without a manual reload. Now registered — wire the same
  // debounced refresh issue.updated already uses.
  useRealtimeEvent("issue.created", debouncedRefresh)
  useRealtimeEvent("issue.deleted", debouncedRefresh)
  useRealtimeEvent("issue.started", debouncedRefresh)
  useRealtimeEvent("pipeline.run.started", debouncedRefresh)
  useRealtimeEvent("pipeline.run.completed", debouncedRefresh)
  useRealtimeEvent("pipeline.run.failed", debouncedRefresh)
  useRealtimeEvent("realtime.reconnected", debouncedRefresh)

  const gapsByCrew = useMemo(() => {
    const counts = new Map<string, number>()
    for (const gaps of readiness.gapsByCredential.values()) {
      for (const gap of gaps) counts.set(gap.crewId, (counts.get(gap.crewId) ?? 0) + 1)
    }
    return counts
  }, [readiness.gapsByCredential])

  // /runtime/capacity is instance-scoped by design, so its holds can belong to
  // another workspace's crews — and this page renders a hold's detail string.
  // Scope to ours, one entry per crew (admission appends one per held START).
  const heldCrews = useMemo(
    () => heldForWorkspace(capacityQ.data?.held ?? null, crews),
    [capacityQ.data, crews],
  )

  const attentionItems = useMemo(
    () => buildAttentionItems({ inbox: inbox.items, heldCrews, reviewCount: reviewQ.data?.length ?? 0, activeByKind: inbox.activeByKind }),
    [inbox.items, inbox.activeByKind, heldCrews, reviewQ.data],
  )

  const fleet = useMemo(
    () => deriveFleetHealth({ crews, agents, gapsByCrew, servicesByCrew: services.byCrew }),
    [crews, agents, gapsByCrew, services.byCrew],
  )


  const kpis = useMemo(
    () => kpisFromInsights(insightsQ.data ?? null),
    [insightsQ.data],
  )


  const runVolumeBuckets = useMemo<RunVolumeBucket[]>(
    () => (volumeQ.data?.buckets ?? []).map((bucket) => ({ ts: bucket.ts, ...bucket.series })),
    [volumeQ.data],
  )

  const runVolumeSeries = useMemo<RunVolumeSeries[]>(() => {
    // series_labels is typed as required, but the type is a claim about the
    // wire and fetchOr validates nothing — a 200 with an unexpected body
    // reaches here, and Object.entries(undefined) throws inside a useMemo,
    // which unmounts the whole dashboard rather than the one chart. The line
    // above already tolerates a missing bucket.series for the same reason.
    if (!volumeQ.data?.series_labels) return []
    return Object.entries(volumeQ.data.series_labels).map(([key, label]) => {
      const crewIndex = crews.findIndex((crew) => crew.id === key || crew.slug === key || crew.name.toLocaleLowerCase() === label.toLocaleLowerCase())
      return {
        key,
        label,
        // The metrics endpoint labels by crew id. Use the same colour as its
        // icon; a missing colour still gets the shared palette fallback.
        color: crewColor(crewIndex >= 0 ? crews[crewIndex].color : null, crewIndex >= 0 ? crewIndex : undefined),
      }
    })
  }, [volumeQ.data, crews])

  const runVolume = useMemo(
    () => foldRunVolumeSeries(runVolumeBuckets, runVolumeSeries),
    [runVolumeBuckets, runVolumeSeries],
  )

  const runVolumeTotal = useMemo(
    () => runVolumeBuckets.reduce(
      (total, bucket) => total + Object.entries(bucket).reduce((sum, [key, value]) => key === "ts" ? sum : sum + Number(value), 0),
      0,
    ),
    [runVolumeBuckets],
  )

  const spendByCrew = useMemo(() => {
    const rows = spendQ.data?.rows
    if (!rows || rows.length === 0) return null
    return new Map(rows.map((row) => [row.crew_id, row.cost_usd]))
  }, [spendQ.data])

  // undefined = the ledger has not answered (pending or failed); null = it
  // answered with no rows (not metered on this billing mode). The two used to
  // collapse into "not metered", which is a claim about the billing mode made
  // while the request was still in flight.
  const spendTotal = useMemo<number | null | undefined>(
    () => (spendQ.isPending || spendQ.isError ? undefined : spendByCrew ? Array.from(spendByCrew.values()).reduce((sum, v) => sum + v, 0) : null),
    [spendByCrew, spendQ.isPending, spendQ.isError],
  )

  const fleetCards = useMemo(
    () => deriveFleetBoard({ rows: fleet, agents, spendByCrew, buckets: runVolumeBuckets }),
    [fleet, agents, spendByCrew, runVolumeBuckets],
  )

  const loading = workspaceLoading || !onboardingChecked || agentsQ.isPending || crewsQ.isPending
  if (loading) return <DashboardSkeleton crews={crews.length} window={reportWindow} onWindowChange={setReportWindow} />

  return (
    <div className="flex min-h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col bg-background">
      <DashboardPeriodBar value={reportWindow} onChange={setReportWindow} />

      <main className="mx-auto flex w-full max-w-[1800px] flex-1 flex-col gap-3 p-4 pb-10 md:p-5">
        <WelcomeChecklist firstAgentId={firstAgentId} />
        {crews.length === 0 && workspaceId && <RecipesEmptyState workspaceId={workspaceId} onInstalled={invalidateDashboard} />}

        {/* The first visible content is the work needing a person. */}
        <Appear order={0}><AttentionStrip items={attentionItems} inboxLoading={inbox.loading} inboxError={inbox.error} /></Appear>

        <div className="grid min-w-0 grid-cols-1 gap-3 xl:h-[520px] xl:grid-cols-3">
          <Appear order={1} className="min-w-0 xl:col-span-2 xl:min-h-0">
            <DashboardResults key={workspaceId} review={reviewQ.data ?? []} inProgress={inProgressQ.data ?? []} completed={completedQ.data ?? []} activeAgentRuns={agentRunsQ.data ?? []} activeRoutineRuns={activeRuns.runs} recentRoutineRuns={activeRuns.recentDashboardRuns} agents={agents} crews={crews} workspaceId={workspaceId} loading={reviewQ.isPending || inProgressQ.isPending || completedQ.isPending || agentRunsQ.isPending} error={reviewQ.isError || inProgressQ.isError || completedQ.isError || agentRunsQ.isError} routineError={activeRuns.error} routineLoading={activeRuns.loading} onRetry={() => { void reviewQ.refetch(); void inProgressQ.refetch(); void completedQ.refetch(); void agentRunsQ.refetch(); activeRuns.refresh() }} />
          </Appear>
          <Appear order={2} className="flex min-w-0 flex-col gap-3 xl:min-h-0 [&>div]:h-auto">
            <UpNext schedules={schedules.schedules} />
            <FleetBoard cards={fleetCards} workspaceId={workspaceId} />
          </Appear>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Radio className="h-3.5 w-3.5 text-primary-hover" aria-hidden />
          <h2 className="text-[11px] font-semibold uppercase tracking-wider text-foreground/70">Agent run summary</h2>
          <span className="font-mono text-[10px] text-muted-foreground">{reportWindow} · routine runs appear in Activity</span>
        </div>

        <Appear order={2}>
          <OutcomeKpis
            data={kpis}
            window={reportWindow}
            spendUsd={spendTotal}
            spendPerRun={typeof spendTotal === "number" && kpis.successTotal > 0 ? spendTotal / kpis.successTotal : null}
          />
        </Appear>

        <div className="grid grid-cols-1 gap-3 xl:grid-cols-5">
          <Appear order={2} className="xl:col-span-3">
            <DashboardCard title={`Run volume · ${reportWindow} · by crew`} icon={Radio} hint={volumeQ.data ? `${runVolumeTotal} runs` : "unavailable"} action={<Link href="/activity" className="text-primary-hover hover:underline">Report →</Link>} className="h-full">
              <RunVolumeChart buckets={runVolume.buckets} series={runVolume.series} window={reportWindow} />
            </DashboardCard>
          </Appear>
          <Appear order={2} className="xl:col-span-2"><PagesStrip /></Appear>
        </div>

      </main>
    </div>
  )
}

function DashboardSkeleton({ crews, window, onWindowChange }: { crews: number; window: DashboardWindow; onWindowChange: (value: DashboardWindow) => void }) {
  return (
    <div className="flex min-h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col">
      <DashboardPeriodBar value={window} onChange={onWindowChange} />
      {/* Same geometry as the loaded page (results beside the next agenda and
          crews, then the KPI strip), so
          nothing jumps when the data lands. */}
      <div className="mx-auto flex w-full max-w-[1800px] flex-col gap-3 p-4 md:p-5">
        <Skeleton className="h-[52px] rounded-xl" />
        <Skeleton className="h-[110px] rounded-xl" />
        <div className="grid grid-cols-1 gap-3 xl:h-[520px] xl:grid-cols-3">
          <Skeleton className="h-[380px] rounded-xl xl:col-span-2 xl:h-full" />
          <div className="flex flex-col gap-3 xl:min-h-0">
            <Skeleton className="h-[84px] rounded-xl" />
            {/* FleetBoard renders nothing for an empty workspace, so its
                placeholder must not appear either. */}
            {crews > 0 && (
              <Skeleton className={cn("rounded-xl xl:h-auto xl:flex-1", crews >= 3 ? "h-[192px]" : crews === 2 ? "h-[148px]" : "h-[104px]")} />
            )}
          </div>
        </div>
        <div className="grid grid-cols-2 gap-3 xl:grid-cols-4">{Array.from({ length: 4 }, (_, index) => <Skeleton key={index} className="h-20 rounded-xl" />)}</div>
        <div className="grid grid-cols-1 gap-3 xl:grid-cols-5"><Skeleton className="h-[220px] rounded-xl xl:col-span-3" /><Skeleton className="h-[220px] rounded-xl xl:col-span-2" /></div>
      </div>
    </div>
  )
}
