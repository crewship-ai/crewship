"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { LayoutDashboard } from "lucide-react"

import { SubBar } from "@/components/layout/sub-bar"
import { OfflineBanner, PageLoading, StaleData } from "@/components/layout/page-state"
import { Button } from "@/components/ui/button"
import {
  AttentionTiles,
  attentionState,
  heldForWorkspace,
  DashboardKpis,
  DashboardUpNext,
  buildAttentionItems,
  deriveFleetHealth,
  kpisFromInsights,
} from "@/components/features/dashboard/dashboard-overview"
import { DashCard, DashNote } from "@/components/features/dashboard/dash-card"
import { Segmented } from "@/components/ui/segmented"
import { DashboardHead, dashboardMeta, greeting, nextScheduledRun, useMinuteClock } from "@/components/features/dashboard/dashboard-head"
import { DashboardEmpty } from "@/components/features/dashboard/dashboard-empty"
import { RunVolumeChart, type RunVolumeBucket, type RunVolumeSeries } from "@/components/features/dashboard/run-volume-chart"
import { FleetBoard, deriveFleetBoard } from "@/components/features/dashboard/fleet-board"
import { DashboardResults } from "@/components/features/dashboard/dashboard-results"
import { PagesStrip } from "@/components/features/dashboard/pages-strip"
import { WelcomeChecklist } from "@/components/features/dashboard/welcome-checklist"
import { useDashboardOffline } from "@/components/features/dashboard/use-dashboard-offline"
import { useWorkspace } from "@/hooks/use-workspace"
import { roleAtLeast } from "@/lib/routine-governance"
import { useIsInstanceAdmin, useSessionSafe } from "@/hooks/use-auth"
import { dashboardLanding } from "@/lib/dashboard-landing"
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
import { serverFetch } from "@/lib/server-base"

const WINDOWS: { value: DashboardWindow; label: string }[] = [
  { value: "24h", label: "24h" },
  { value: "7d", label: "7d" },
  { value: "30d", label: "30d" },
]

/**
 * The page's header lives in the top bar (SubBar): the area name and the
 * report window, which does not wait for any data. On a phone the window is
 * not offered and the page reports the default 24 h.
 */
function DashboardBar({ value, onChange }: { value: DashboardWindow; onChange: (value: DashboardWindow) => void }) {
  return (
    <SubBar
      icon={LayoutDashboard}
      title="Dashboard"
      ariaLabel="Dashboard"
      // The greeting below is the page's h1; the top bar only names the area.
      heading={false}
      actions={<Segmented ariaLabel="Dashboard time window" size="xs" options={WINDOWS} value={value} onChange={onChange} className="max-md:hidden" />}
    />
  )
}

/** The page body: 24/28 px gutters, 14 px on a phone. */
const BODY = "mx-auto w-full max-w-[1800px] px-3.5 pb-10 pt-4 md:px-7 md:pt-6"

function runVolumeParams(window: DashboardWindow): TimeseriesParams {
  return {
    metric: "runs_count",
    window,
    bucket: window === "24h" ? "1h" : "1d",
    group_by: "crew",
  }
}

export default function DashboardPage() {
  const { workspaceId, role, loading: workspaceLoading } = useWorkspace()
  const [onboardingChecked, setOnboardingChecked] = useState(false)
  const [firstAgentId, setFirstAgentId] = useState<string | null>(null)
  const [reportWindow, setReportWindow] = useState<DashboardWindow>("24h")
  const session = useSessionSafe()
  const now = useMinuteClock()

  const instanceAdmin = useIsInstanceAdmin()
  const [onboardingCompleted, setOnboardingCompleted] = useState<boolean | null>(null)
  useEffect(() => {
    serverFetch("/api/v1/onboarding/status")
      .then((response) => (response.ok ? response.json() : null))
      .then((data) => setOnboardingCompleted(data ? !!data.completed : true))
      .catch(() => setOnboardingCompleted(true))
  }, [])
  // An instance admin with no workspace lands in Admin, not the first-run
  // wizard (lib/dashboard-landing.ts).
  useEffect(() => {
    const to = dashboardLanding({ workspaceId, workspaceLoading, instanceAdmin, onboardingCompleted })
    if (to === "wait") return
    if (to) {
      window.location.assign(to)
      return
    }
    setOnboardingChecked(true)
  }, [workspaceId, workspaceLoading, instanceAdmin, onboardingCompleted])

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
  // The ledger read is OWNER/ADMIN on the server (#3590): other roles neither
  // ask for it nor see the Spend tile.
  const canSeeSpend = roleAtLeast(role, "ADMIN")
  const spendQ = useCrewSpend(workspaceId, reportWindow, { ...queryOpts, enabled: (queryOpts?.enabled ?? true) && canSeeSpend })
  const agents = useMemo(() => agentsQ.data ?? [], [agentsQ.data])
  const crews = useMemo(() => crewsQ.data ?? [], [crewsQ.data])
  const services = useCrewServiceSummaries(workspaceId, crews, queryOpts)
  const activeRuns = useActiveRoutineRuns()
  const schedules = usePipelineSchedules(workspaceId)
  const readiness = useCredentialReadiness(workspaceId)
  const inbox = useInbox(workspaceId, "active")

  const invalidateDashboard = useInvalidateDashboard(workspaceId)
  const offline = useDashboardOffline()
  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined)
  const debouncedRefresh = useCallback(() => {
    clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      invalidateDashboard()
    }, 220)
  }, [invalidateDashboard])

  useEffect(() => () => clearTimeout(debounceRef.current), [debouncedRefresh])
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
    () => buildAttentionItems({
      inbox: inbox.items,
      heldCrews,
      reviewCount: reviewQ.data?.length ?? 0,
      reviewIdentifiers: (reviewQ.data ?? []).map((issue) => issue.identifier).filter((id): id is string => Boolean(id)),
      activeByKind: inbox.activeByKind,
      decisionCount: inbox.decisionCount,
    }),
    [inbox.items, inbox.activeByKind, inbox.decisionCount, heldCrews, reviewQ.data],
  )
  const attention = attentionState({ items: attentionItems, inboxLoading: inbox.loading, inboxError: inbox.error })

  const fleet = useMemo(
    () => deriveFleetHealth({ crews, agents, gapsByCrew, servicesByCrew: services.byCrew }),
    [crews, agents, gapsByCrew, services.byCrew],
  )

  // fetchOr answers null for a failed read; undefined is still loading.
  const insightsFailed = insightsQ.isError || insightsQ.data === null
  const volumeFailed = volumeQ.isError || volumeQ.data === null

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

  // Runs per bucket across all crews, oldest first — the one real series the
  // KPI strip can draw a sparkline from.
  const runsPerBucket = useMemo(
    () => runVolumeBuckets.map((bucket) => Object.entries(bucket).reduce((sum, [key, value]) => key === "ts" ? sum : sum + Number(value), 0)),
    [runVolumeBuckets],
  )
  const runVolumeTotal = useMemo(() => runsPerBucket.reduce((total, n) => total + n, 0), [runsPerBucket])

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
    () => deriveFleetBoard({ rows: fleet, agents, buckets: runVolumeBuckets }),
    [fleet, agents, runVolumeBuckets],
  )

  const runningAgents = agents.filter((agent) => agent.status === "RUNNING").length
  const nextRunAt = nextScheduledRun(schedules.schedules, now.getTime())

  const bar = <DashboardBar value={reportWindow} onChange={setReportWindow} />

  const loading = workspaceLoading || !onboardingChecked || agentsQ.isPending || crewsQ.isPending
  if (loading) {
    return (
      <>
        {bar}
        <PageLoading variant="cards" onRetry={() => { void agentsQ.refetch(); void crewsQ.refetch() }} />
      </>
    )
  }

  if (crews.length === 0) {
    return (
      <>
        {bar}
        <main className={BODY}>
          <WelcomeChecklist firstAgentId={firstAgentId} />
          {workspaceId && <DashboardEmpty workspaceId={workspaceId} onInstalled={invalidateDashboard} />}
        </main>
      </>
    )
  }

  const retryVolume = () => { void volumeQ.refetch() }

  return (
    <>
      {bar}
      <main className={BODY}>
        {/* DSH-01, v67 Offline state: one banner, the data under it dimmed
            and not clickable until live updates are back. */}
        {offline.offline && <OfflineBanner since={offline.since} onRetry={invalidateDashboard} className="mb-4" />}
        <StaleData stale={offline.offline}>
          <DashboardHead
            title={greeting(now, session.data?.user.name)}
            meta={dashboardMeta({ now, attentionCount: attentionItems.length, attentionKnown: attention.inboxKnown, runningAgents, nextRunAt })}
          />
          <WelcomeChecklist firstAgentId={firstAgentId} />

          {/* The first visible content is the work needing a person. */}
          <AttentionTiles items={attentionItems} inboxLoading={inbox.loading} inboxError={inbox.error} onRetry={inbox.refresh} />

          <div className="grid min-w-0 grid-cols-1 items-start gap-4 xl:grid-cols-3">
            <DashboardResults key={workspaceId} review={reviewQ.data ?? []} inProgress={inProgressQ.data ?? []} completed={completedQ.data ?? []} activeAgentRuns={agentRunsQ.data ?? []} activeRoutineRuns={activeRuns.runs} recentRoutineRuns={activeRuns.recentDashboardRuns} agents={agents} crews={crews} loading={reviewQ.isPending || inProgressQ.isPending || completedQ.isPending || agentRunsQ.isPending} error={reviewQ.isError || inProgressQ.isError || completedQ.isError || agentRunsQ.isError} routineError={activeRuns.error} routineLoading={activeRuns.loading} onRetry={() => { void reviewQ.refetch(); void inProgressQ.refetch(); void completedQ.refetch(); void agentRunsQ.refetch(); activeRuns.refresh() }} />
            <div className="grid min-w-0 gap-4">
              <DashboardUpNext schedules={schedules.schedules} />
              <FleetBoard cards={fleetCards} />
            </div>
          </div>

          {insightsFailed ? (
            <div data-testid="dashboard-kpis" className="mt-4 overflow-hidden rounded-card border border-border bg-card [&>*]:border-t-0">
              <DashNote tone="error" action={<Button size="xs" variant="outline" onClick={() => { void insightsQ.refetch() }} className="coarse:min-h-[44px]">Retry</Button>}>
                Could not load the run summary
              </DashNote>
            </div>
          ) : (
            <DashboardKpis
              data={kpis}
              window={reportWindow}
              spendUsd={spendTotal}
              showSpend={canSeeSpend}
              spendPerRun={typeof spendTotal === "number" && kpis.successTotal > 0 ? spendTotal / kpis.successTotal : null}
              completedSeries={volumeFailed ? undefined : runsPerBucket}
              completedSeriesLabel={`Runs started per ${reportWindow === "24h" ? "hour" : "day"}`}
            />
          )}

          <div className="mt-4 grid min-w-0 grid-cols-1 items-start gap-4 xl:grid-cols-3">
            <DashCard
              title={`Run volume · ${reportWindow} · by crew`}
              concept="activity"
              meta={volumeQ.data ? `${runVolumeTotal} runs` : undefined}
              link={{ label: "Report", href: "/activity" }}
              className="xl:col-span-2"
            >
              {volumeFailed ? (
                <DashNote tone="error" action={<Button size="xs" variant="outline" onClick={retryVolume} className="coarse:min-h-[44px]">Retry</Button>}>
                  Could not load run volume
                </DashNote>
              ) : (
                // A phone scrolls the chart sideways rather than squeezing it.
                <div className="overflow-x-auto px-4 pb-3.5 pt-1">
                  <div className="max-md:min-w-[480px]">
                    <RunVolumeChart buckets={runVolume.buckets} series={runVolume.series} window={reportWindow} />
                  </div>
                </div>
              )}
            </DashCard>
            <PagesStrip />
          </div>
        </StaleData>
      </main>
    </>
  )
}
