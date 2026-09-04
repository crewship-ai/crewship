"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import Link from "next/link"
import { LayoutDashboard, MessageSquare, Plus, Radio } from "lucide-react"

import { SubBar, SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import {
  AttentionStrip,
  attentionState,
  heldForWorkspace,
  OutcomeKpis,
  RunningNow,
  SystemSignals,
  UpNext,
  buildAttentionItems,
  deriveFleetHealth,
  kpisFromInsights,
} from "@/components/features/dashboard/dashboard-overview"
import { RunVolumeChart, type RunVolumeBucket, type RunVolumeSeries } from "@/components/features/dashboard/run-volume-chart"
import { RecipesEmptyState } from "@/components/features/dashboard/recipes-cards"
import { BridgeStrip, deriveBridge } from "@/components/features/dashboard/bridge-strip"
import { FleetBoard, deriveFleetBoard } from "@/components/features/dashboard/fleet-board"
import { WorkSnapshot } from "@/components/features/dashboard/work-snapshot"
import { ActivityTicker } from "@/components/features/dashboard/activity-ticker"
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
import { useRealtimeEvent, useRealtimeStatusSafe } from "@/hooks/use-realtime"
import {
  useAgentSummaries,
  useCrewServiceSummaries,
  useCrewSpend,
  useCrewSummaries,
  useDashboardMissions,
  useInvalidateDashboard,
  useMemoryHealth,
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
  const missionsQ = useDashboardMissions(workspaceId, queryOpts)
  const insightsQ = useRunsInsights(workspaceId, reportWindow, queryOpts)
  const capacityQ = useRuntimeCapacity(queryOpts)
  const memoryQ = useMemoryHealth(workspaceId, queryOpts)
  const volumeParams = useMemo(() => runVolumeParams(reportWindow), [reportWindow])
  const volumeQ = useMetricsTimeseries(workspaceId, volumeParams, queryOpts)
  const spendQ = useCrewSpend(workspaceId, reportWindow, queryOpts)
  // Bumped on every realtime tick the dashboard already listens to, so the
  // activity ticker refreshes in step with the cards above it.
  const [liveTick, setLiveTick] = useState(0)

  const agents = useMemo(() => agentsQ.data ?? [], [agentsQ.data])
  const crews = useMemo(() => crewsQ.data ?? [], [crewsQ.data])
  const missions = useMemo(() => missionsQ.data ?? [], [missionsQ.data])
  const services = useCrewServiceSummaries(workspaceId, crews, queryOpts)
  const activeRuns = useActiveRoutineRuns()
  const schedules = usePipelineSchedules(workspaceId)
  const readiness = useCredentialReadiness(workspaceId)
  const inbox = useInbox(workspaceId, "active")
  const realtimeStatus = useRealtimeStatusSafe()

  const invalidateDashboard = useInvalidateDashboard(workspaceId)
  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined)
  const debouncedRefresh = useCallback(() => {
    clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      invalidateDashboard()
      setLiveTick((n) => n + 1)
    }, 220)
  }, [invalidateDashboard])

  useEffect(() => () => clearTimeout(debounceRef.current), [])
  useRealtimeEvent("run.started", debouncedRefresh)
  useRealtimeEvent("run.completed", debouncedRefresh)
  useRealtimeEvent("run.failed", debouncedRefresh)
  useRealtimeEvent("agent.status", debouncedRefresh)
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

  const credentialGapCount = useMemo(
    () => Array.from(gapsByCrew.values()).reduce((total, count) => total + count, 0),
    [gapsByCrew],
  )

  // /runtime/capacity is instance-scoped by design, so its holds can belong to
  // another workspace's crews — and this page renders a hold's detail string.
  // Scope to ours, one entry per crew (admission appends one per held START).
  const heldCrews = useMemo(
    () => heldForWorkspace(capacityQ.data?.held ?? null, crews),
    [capacityQ.data, crews],
  )

  const attentionItems = useMemo(
    () => buildAttentionItems({ inbox: inbox.items, heldCrews, credentialGapCount }),
    [inbox.items, heldCrews, credentialGapCount],
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
    return Object.entries(volumeQ.data.series_labels).map(([key, label]) => ({
      key,
      label,
      color: crewColor(crews.find((crew) => crew.id === key)?.color),
    }))
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

  const runSeries = useMemo(
    () => runVolumeBuckets.map((bucket) => Object.entries(bucket).reduce((sum, [key, value]) => key === "ts" ? sum : sum + Number(value), 0)),
    [runVolumeBuckets],
  )

  const fleetCards = useMemo(
    () => deriveFleetBoard({ rows: fleet, agents, spendByCrew, buckets: runVolumeBuckets }),
    [fleet, agents, spendByCrew, runVolumeBuckets],
  )

  const attention = attentionState({ items: attentionItems, inboxLoading: inbox.loading, inboxError: inbox.error })
  const bridge = useMemo(
    () => deriveBridge({
      agents,
      crews,
      spendRows: spendQ.isPending || spendQ.isError ? undefined : (spendQ.data?.rows ?? null),
      kpis,
      attentionCount: attentionItems.length,
      attentionKnown: attention.inboxKnown,
      schedules: schedules.schedules,
    }),
    [agents, crews, spendQ.data, kpis, attentionItems.length, attention.inboxKnown, schedules.schedules],
  )

  const serviceTotals = useMemo(() => {
    let running = 0
    let total = 0
    let unchecked = 0
    for (const summary of services.byCrew.values()) {
      // A crew whose /services call failed contributes nothing to the
      // numerator OR the denominator, so without counting it the row can read
      // a confident "6/6 running" over a fleet it never reached.
      if (!summary.checked) {
        unchecked += 1
        continue
      }
      running += summary.running
      total += summary.total
    }
    return { running, total, checked: services.checked, unchecked }
  }, [services.byCrew, services.checked])

  const loading = workspaceLoading || !onboardingChecked || agentsQ.isPending || crewsQ.isPending || missionsQ.isPending
  const realtimeMeta = (
    <span
      className={cn(
        "hidden items-center gap-1.5 rounded-full border px-2 py-0.5 text-micro font-medium sm:inline-flex",
        realtimeStatus === "connected"
          ? "border-success/25 bg-success/10 text-success"
          : realtimeStatus === "connecting"
            ? "border-warn/25 bg-warn/10 text-warn"
            : "border-destructive/25 bg-destructive/10 text-destructive",
      )}
    >
      <Radio className={cn("h-3 w-3", realtimeStatus === "connected" && "animate-pulse motion-reduce:animate-none")} />
      {realtimeStatus === "connected" ? "Live" : realtimeStatus === "connecting" ? "Connecting" : "Offline"}
    </span>
  )

  if (loading) return <DashboardSkeleton crews={crews.length} agents={agents.length} />

  return (
    <div className="flex min-h-[calc(100vh-48px)] flex-col bg-background">
      <SubBar
        icon={LayoutDashboard}
        title="Dashboard"
        description={`${crews.length} crew${crews.length === 1 ? "" : "s"} · ${agents.length} agent${agents.length === 1 ? "" : "s"}`}
        meta={realtimeMeta}
        ariaLabel="Dashboard"
        actions={
          <>
            <div className="hidden items-center rounded-md border border-border/60 bg-background/50 p-0.5 md:flex" role="group" aria-label="Dashboard time window">
              {WINDOW_LABELS.map((item) => (
                <Button
                  key={item}
                  type="button"
                  variant="ghost"
                  size="xs"
                  aria-pressed={reportWindow === item}
                  onClick={() => setReportWindow(item)}
                  className={cn("h-6 min-w-10 px-2 font-mono", reportWindow === item && "bg-primary/15 text-primary-hover")}
                >
                  {item}
                </Button>
              ))}
            </div>
            <SubBarSecondary asChild icon={Plus}>
              <Link href="/issues?create=1"><span className="hidden sm:inline">New issue</span></Link>
            </SubBarSecondary>
            <SubBarPrimary asChild icon={MessageSquare}>
              <Link href="/chat"><span className="hidden sm:inline">Chat with agent</span><span className="sm:hidden">Chat</span></Link>
            </SubBarPrimary>
          </>
        }
      />

      <main className="mx-auto flex w-full max-w-[1800px] flex-1 flex-col gap-4 p-4 pb-10 md:p-6">
        <WelcomeChecklist firstAgentId={firstAgentId} />
        {crews.length === 0 && workspaceId && <RecipesEmptyState workspaceId={workspaceId} onInstalled={invalidateDashboard} />}

        <div className="flex items-center justify-between md:hidden">
          <span className="text-label font-medium text-muted-foreground">Reporting window</span>
          <div className="flex items-center rounded-md border border-border/60 bg-card p-0.5" role="group" aria-label="Dashboard time window">
            {WINDOW_LABELS.map((item) => (
              <Button key={item} type="button" variant="ghost" size="xs" aria-pressed={reportWindow === item} onClick={() => setReportWindow(item)} className={cn("h-6 min-w-10 px-2 font-mono", reportWindow === item && "bg-primary/15 text-primary-hover")}>{item}</Button>
            ))}
          </div>
        </div>

        <Appear order={0}><BridgeStrip data={bridge} window={reportWindow} realtimeStatus={realtimeStatus ?? "offline"} /></Appear>

        <Appear order={1}><AttentionStrip items={attentionItems} inboxLoading={inbox.loading} inboxError={inbox.error} /></Appear>

        <Appear order={2}><FleetBoard cards={fleetCards} workspaceId={workspaceId} /></Appear>

        <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
          <Appear order={3} className="xl:col-span-2">
            <RunningNow runs={activeRuns.runs} agents={agents} crews={crews}>
              <ActivityTicker workspaceId={workspaceId} reloadKey={liveTick} />
            </RunningNow>
          </Appear>
          <Appear order={4}><UpNext schedules={schedules.schedules} /></Appear>
        </div>

        <Appear order={5}>
          <OutcomeKpis
            data={kpis}
            window={reportWindow}
            runSeries={runSeries}
            spendUsd={spendTotal}
            spendPerRun={typeof spendTotal === "number" && kpis.successTotal > 0 ? spendTotal / kpis.successTotal : null}
          />
        </Appear>

        <div className="grid grid-cols-1 gap-4 xl:grid-cols-5">
          <Appear order={6} className="xl:col-span-3">
            <DashboardCard title={`Run volume · ${reportWindow} · by crew`} icon={Radio} hint={volumeQ.data ? `${runVolumeTotal} runs` : "unavailable"} action={<Link href="/activity" className="text-primary-hover hover:underline">Report →</Link>} className="h-full">
              <RunVolumeChart buckets={runVolume.buckets} series={runVolume.series} window={reportWindow} />
            </DashboardCard>
          </Appear>
          <Appear order={7} className="xl:col-span-2"><WorkSnapshot missions={missions} workspaceId={workspaceId} /></Appear>
        </div>

        <div className="grid grid-cols-1 gap-4 xl:grid-cols-5">
          <Appear order={8} className="xl:col-span-3"><PagesStrip /></Appear>
          <Appear order={9} className="xl:col-span-2"><SystemSignals capacity={capacityQ.data ?? null} heldCrews={heldCrews} memory={memoryQ.data ?? null} credentialGapCount={credentialGapCount} services={serviceTotals} realtimeStatus={realtimeStatus ?? undefined} /></Appear>
        </div>
      </main>
    </div>
  )
}

function DashboardSkeleton({ crews, agents }: { crews: number; agents: number }) {
  return (
    <div className="flex min-h-[calc(100vh-48px)] flex-col">
      <SubBar icon={LayoutDashboard} title="Dashboard" description={crews || agents ? `${crews} crews · ${agents} agents` : "Loading…"} ariaLabel="Dashboard" />
      <div className="mx-auto flex w-full max-w-[1800px] flex-col gap-4 p-4 md:p-6">
        <Skeleton className="h-[96px] rounded-xl" />
        <Skeleton className="h-[120px] rounded-xl" />
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">{Array.from({ length: Math.max(1, Math.min(3, crews || 3)) }, (_, index) => <Skeleton key={index} className="h-[172px] rounded-xl" />)}</div>
        <div className="grid grid-cols-1 gap-4 xl:grid-cols-3"><Skeleton className="h-[260px] rounded-xl xl:col-span-2" /><Skeleton className="h-[260px] rounded-xl" /></div>
        <div className="grid grid-cols-2 gap-4 xl:grid-cols-4">{Array.from({ length: 4 }, (_, index) => <Skeleton key={index} className="h-[118px] rounded-xl" />)}</div>
        <div className="grid grid-cols-1 gap-4 xl:grid-cols-5"><Skeleton className="h-[330px] rounded-xl xl:col-span-3" /><Skeleton className="h-[330px] rounded-xl xl:col-span-2" /></div>
      </div>
    </div>
  )
}
