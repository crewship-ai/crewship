"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import Link from "next/link"
import {
  TrendingUp, Target, Banknote, Gem, Box, Activity, HeartPulse,
  FolderOpen, Radio, Inbox as InboxIcon, ListChecks,
} from "lucide-react"

import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import type { Mission } from "@/lib/types/mission"

import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { StatusDonut, type StatusDonutDatum } from "@/components/features/dashboard/status-donut"
import { ThroughputChart, type ThroughputBucket, type ThroughputSeries } from "@/components/features/dashboard/throughput-chart"
import { CostBurnChart, type CostBucket, type CostSeries } from "@/components/features/dashboard/cost-burn-chart"
import { TopMissionsChart, type TopMissionEntry } from "@/components/features/dashboard/top-missions-chart"
import { ContainerResourcesTile, type ContainerStatsEntry } from "@/components/features/dashboard/container-resources-tile"
import { AgentHeatmap, type HeatmapBucket, type HeatmapAgent } from "@/components/features/dashboard/agent-heatmap"
import { CrewRadial, type CrewHealthEntry } from "@/components/features/dashboard/crew-radial"
import { ProjectProgress, type ProjectProgressEntry } from "@/components/features/dashboard/project-progress"
import { ActivityFeed } from "@/components/features/dashboard/activity-feed"
import { InboxTile, type InboxEntry } from "@/components/features/dashboard/inbox-tile"
import { RecentMissionsTable } from "@/components/features/dashboard/recent-missions-table"
import { RecipesEmptyState } from "@/components/features/dashboard/recipes-cards"
import { WelcomeChecklist } from "@/components/features/dashboard/welcome-checklist"

import {
  AgentSummary, CrewSummary, ProjectSummary, RunsResponse,
  MissionMetricsResponse, KeeperRequest, TimeseriesResponse,
} from "./dashboard-types"
import {
  crewColor, STATUS_PALETTE, formatCost, formatRelativeShort,
} from "./dashboard-helpers"

export default function DashboardPage() {
  const router = useRouter()
  const { workspaceId, loading: wsLoading } = useWorkspace()

  // ── Core data state ────────────────────────────────────────────────
  const [agents, setAgents] = useState<AgentSummary[]>([])
  const [crews, setCrews] = useState<CrewSummary[]>([])
  const [projects, setProjects] = useState<ProjectSummary[]>([])
  const [missions, setMissions] = useState<Mission[]>([])
  const [metrics, setMetrics] = useState<MissionMetricsResponse | null>(null)
  const [runsData, setRunsData] = useState<RunsResponse | null>(null)
  const [escalationCount, setEscalationCount] = useState(0)
  const [keeperRequests, setKeeperRequests] = useState<KeeperRequest[]>([])
  const [throughputData, setThroughputData] = useState<TimeseriesResponse | null>(null)
  const [costData, setCostData] = useState<TimeseriesResponse | null>(null)
  const [containerStats, setContainerStats] = useState<Map<string, ContainerStatsEntry>>(new Map())
  const [loading, setLoading] = useState(true)
  const [onboardingChecked, setOnboardingChecked] = useState(false)

  // ── Onboarding gate ────────────────────────────────────────────────
  useEffect(() => {
    fetch("/api/v1/onboarding/status")
      .then((res) => (res.ok ? res.json() : null))
      .then((data) => {
        if (data && !data.completed) {
          router.push("/onboarding")
          return
        }
        setOnboardingChecked(true)
      })
      .catch(() => setOnboardingChecked(true))
  }, [router])

  // ── Fetchers ────────────────────────────────────────────────────────
  //
  // Each fetch builds the next snapshot first, then commits it atomically.
  // If any individual request fails, we still clear the matching slice so a
  // workspace switch never leaves yesterday's data showing under today's
  // workspace (use empty-state rather than stale state).
  const fetchData = useCallback(async (showLoading = true) => {
    if (!workspaceId) return
    if (showLoading) setLoading(true)
    try {
      const ws = encodeURIComponent(workspaceId)
      const [
        agentsRes,
        crewsRes,
        projectsRes,
        missionsRes,
        metricsRes,
        runsRes,
        escCountRes,
        keeperRes,
      ] = await Promise.all([
        fetch(`/api/v1/agents?workspace_id=${ws}`),
        fetch(`/api/v1/crews?workspace_id=${ws}`),
        fetch(`/api/v1/projects?workspace_id=${ws}`),
        fetch(`/api/v1/missions?workspace_id=${ws}&limit=50&include_tasks=true`),
        fetch(`/api/v1/mission-metrics?workspace_id=${ws}`),
        fetch(`/api/v1/runs?workspace_id=${ws}&limit=50`),
        fetch(`/api/v1/escalations/pending-count?workspace_id=${ws}`),
        fetch(`/api/v1/admin/keeper/requests?workspace_id=${ws}&limit=10`),
      ])
      setAgents(agentsRes.ok ? await agentsRes.json() : [])
      setCrews(crewsRes.ok ? await crewsRes.json() : [])
      setProjects(projectsRes.ok ? await projectsRes.json() : [])
      setMissions(missionsRes.ok ? await missionsRes.json() : [])
      setMetrics(metricsRes.ok ? await metricsRes.json() : null)
      setRunsData(runsRes.ok ? await runsRes.json() : null)
      if (escCountRes.ok) {
        const data = await escCountRes.json()
        setEscalationCount(Number(data?.count) || 0)
      } else {
        setEscalationCount(0)
      }
      if (keeperRes.ok) {
        const data = await keeperRes.json()
        setKeeperRequests(Array.isArray(data) ? data : (data?.data ?? []))
      } else {
        setKeeperRequests([])
      }
    } catch {
      // Network-level failure — clear every slice so we don't keep showing
      // another workspace's data. The UI renders its normal empty states.
      setAgents([])
      setCrews([])
      setProjects([])
      setMissions([])
      setMetrics(null)
      setRunsData(null)
      setEscalationCount(0)
      setKeeperRequests([])
    } finally {
      if (showLoading) setLoading(false)
    }
  }, [workspaceId])

  const fetchTimeseries = useCallback(async () => {
    if (!workspaceId) return
    try {
      const ws = encodeURIComponent(workspaceId)
      const [thruRes, costRes] = await Promise.all([
        fetch(`/api/v1/metrics/timeseries?workspace_id=${ws}&metric=issues_closed&window=24h&bucket=1h&group_by=crew`),
        fetch(`/api/v1/metrics/timeseries?workspace_id=${ws}&metric=cost_usd&window=7d&bucket=1d&group_by=none`),
      ])
      setThroughputData(thruRes.ok ? await thruRes.json() : null)
      setCostData(costRes.ok ? await costRes.json() : null)
    } catch {
      setThroughputData(null)
      setCostData(null)
    }
  }, [workspaceId])

  // Reset workspace-scoped state when the user switches workspaces — otherwise
  // state Maps like `containerStats` accumulate across workspace boundaries
  // and the live resources tile would render the previous workspace's crews
  // until a fresh container.stats frame arrives for the new one.
  useEffect(() => {
    setContainerStats(new Map())
  }, [workspaceId])

  useEffect(() => {
    if (!workspaceId || !onboardingChecked) return
    fetchData()
    fetchTimeseries()
  }, [workspaceId, onboardingChecked, fetchData, fetchTimeseries])

  // ── Realtime: debounced refetch so a burst of events doesn't storm the API ─
  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined)
  const debouncedRefetch = useCallback(() => {
    clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      fetchData(false)
      fetchTimeseries()
    }, 250)
  }, [fetchData, fetchTimeseries])

  useRealtimeEvent("run.started", debouncedRefetch)
  useRealtimeEvent("run.completed", debouncedRefetch)
  useRealtimeEvent("run.failed", debouncedRefetch)
  useRealtimeEvent("agent.status", debouncedRefetch)
  // mission.updated covers issue status changes too (issues ARE missions).
  useRealtimeEvent("mission.updated", debouncedRefetch)
  useRealtimeEvent("task.updated", debouncedRefetch)
  useRealtimeEvent("escalation.created", debouncedRefetch)

  // Container stats stream — per-container CPU history for sparklines
  useRealtimeEvent("container.stats", useCallback((event: RealtimeEvent) => {
    const p = event.payload
    const containerId = p.container_id
    if (typeof containerId !== "string") return
    setContainerStats((prev) => {
      const next = new Map(prev)
      const existing = next.get(containerId)
      const cpu = Number(p.cpu_percent) || 0
      const history = existing?.cpu_history ? [...existing.cpu_history, cpu] : [cpu]
      if (history.length > 30) history.shift()
      next.set(containerId, {
        container_id: containerId,
        crew_id: String(p.crew_id ?? ""),
        crew_slug: (p.crew_slug as string | undefined) ?? existing?.crew_slug ?? null,
        crew_name: (p.crew_name as string | undefined) ?? existing?.crew_name ?? null,
        crew_color: (p.crew_color as string | undefined) ?? existing?.crew_color ?? null,
        cpu_percent: cpu,
        memory_used: Number(p.memory_used) || 0,
        memory_limit: Number(p.memory_limit) || 0,
        memory_percent: Number(p.memory_percent) || 0,
        pids: Number(p.pids) || 0,
        cpu_history: history,
      })
      return next
    })
  }, []))

  // ── Derived data ────────────────────────────────────────────────────
  const isLoading = wsLoading || loading

  const totalAgents = agents.length
  const runningNow = agents.filter((a) => a.status === "RUNNING").length
  const activeMissionCount = metrics?.active_missions ?? 0
  const totalCost24h = metrics?.total_cost_24h ?? 0
  const runsToday = runsData?.stats.today ?? 0
  const runsFailed = runsData?.stats.failed ?? 0
  // No runs today → "—" rather than a misleading 100% ("100% of 0 = success" is a lie)
  const successRateDisplay = runsToday > 0
    ? `${Math.round(((runsToday - runsFailed) / runsToday) * 100)}%`
    : "—"
  const successRatePct = runsToday > 0 ? Math.round(((runsToday - runsFailed) / runsToday) * 100) : null

  const openIssues = missions.filter(
    (m) => (m.mission_type === "issue" || !m.mission_type) && m.status !== "COMPLETED" && m.status !== "CANCELLED" && m.status !== "FAILED",
  ).length

  // Mission status donut
  const donutData = useMemo<StatusDonutDatum[]>(() => {
    const counts: Partial<Record<keyof typeof STATUS_PALETTE, number>> = {}
    for (const m of missions) {
      const key = m.status as keyof typeof STATUS_PALETTE
      counts[key] = (counts[key] ?? 0) + 1
    }
    return (Object.keys(STATUS_PALETTE) as Array<keyof typeof STATUS_PALETTE>)
      .filter((k) => (counts[k] ?? 0) > 0)
      .map((k) => ({
        key: k,
        label: k.charAt(0) + k.slice(1).toLowerCase().replace("_", " "),
        count: counts[k] ?? 0,
        color: STATUS_PALETTE[k],
      }))
  }, [missions])

  // Throughput chart data — from timeseries API if available, else empty
  const throughputBuckets = useMemo<ThroughputBucket[]>(() => {
    if (!throughputData) return []
    return throughputData.buckets.map((b) => ({
      ts: b.ts,
      ...b.series,
    }))
  }, [throughputData])

  const throughputSeries = useMemo<ThroughputSeries[]>(() => {
    if (!throughputData) return []
    return Object.keys(throughputData.series_labels).map((crewId) => {
      const crew = crews.find((c) => c.id === crewId)
      return {
        key: crewId,
        label: throughputData.series_labels[crewId],
        color: crewColor(crew?.color),
      }
    })
  }, [throughputData, crews])

  const throughputTotal = useMemo(() => {
    if (!throughputData) return 0
    let total = 0
    for (const b of throughputData.buckets) {
      for (const v of Object.values(b.series)) total += v
    }
    return total
  }, [throughputData])

  // Cost burn chart data
  const costBuckets = useMemo<CostBucket[]>(() => {
    if (!costData) return []
    return costData.buckets.map((b) => ({
      ts: b.ts,
      ...b.series,
    }))
  }, [costData])

  const costSeries = useMemo<CostSeries[]>(() => {
    if (!costData) return []
    return Object.keys(costData.series_labels).map((key, i) => ({
      key,
      label: costData.series_labels[key],
      color: ["rgb(167, 139, 250)", "rgb(34, 211, 238)", "rgb(52, 211, 153)", "rgb(251, 191, 36)"][i % 4],
    }))
  }, [costData])

  const costTotal = useMemo(() => {
    if (!costData) return 0
    let total = 0
    for (const b of costData.buckets) {
      for (const v of Object.values(b.series)) total += v
    }
    return total
  }, [costData])

  // Top cost missions
  const topMissions = useMemo<TopMissionEntry[]>(() => {
    const withCost = missions
      .filter((m) => (m.total_estimated_cost ?? 0) > 0)
      .sort((a, b) => (b.total_estimated_cost ?? 0) - (a.total_estimated_cost ?? 0))
      .slice(0, 6)
    return withCost.map((m) => {
      const crew = crews.find((c) => c.id === m.crew_id)
      return {
        id: m.id,
        identifier: m.identifier ?? null,
        title: m.title,
        crew_id: m.crew_id,
        // Pass crew palette ID (e.g. "blue"), not a raw hex — TopMissionsChart
        // resolves it to a Tailwind bg class internally.
        crew_color: crew?.color ?? null,
        cost: m.total_estimated_cost ?? 0,
        href: m.identifier ? `/issues/${m.identifier}` : "/issues",
      }
    })
  }, [missions, crews])

  // Crew health radial
  const crewHealth = useMemo<CrewHealthEntry[]>(() => {
    return crews.map((c) => {
      const crewAgents = agents.filter((a) => (a.crew_id ?? a.crew?.slug) === c.id || a.crew?.slug === c.slug)
      const total = crewAgents.length
      const running = crewAgents.filter((a) => a.status === "RUNNING").length
      const errored = crewAgents.filter((a) => a.status === "ERROR").length
      const health = total > 0 ? Math.round(((total - errored) / total) * 100) : 100
      return {
        id: c.id,
        name: c.name,
        slug: c.slug,
        color: crewColor(c.color),
        runningCount: running,
        totalAgents: total,
        healthPct: health,
      }
    })
  }, [crews, agents])

  // Projects
  const projectEntries = useMemo<ProjectProgressEntry[]>(() => {
    return projects.slice(0, 6).map((p) => ({
      id: p.id,
      name: p.name,
      color: p.color || "#60a5fa",
      issueCount: p.issue_count,
      completedCount: Math.round((p.issue_count * p.progress) / 100),
      href: `/issues?project=${encodeURIComponent(p.id)}`,
    }))
  }, [projects])

  // Agent heatmap — 24h grouped by agent × hour from runs
  const heatmapAgents = useMemo<HeatmapAgent[]>(() => {
    return agents
      .filter((a) => a.agent_role !== "LEAD")
      .slice(0, 12)
      .map((a) => ({ id: a.id, slug: a.slug, name: a.name }))
  }, [agents])

  const heatmapBuckets = useMemo<HeatmapBucket[]>(() => {
    if (!runsData) return []
    const now = new Date()
    const buckets: HeatmapBucket[] = []
    for (let i = 23; i >= 0; i--) {
      const bucketStart = new Date(now)
      bucketStart.setMinutes(0, 0, 0)
      bucketStart.setHours(bucketStart.getHours() - i)
      buckets.push({ ts: bucketStart.toISOString(), series: {} })
    }
    for (const run of runsData.data) {
      const start = new Date(run.started_at ?? run.created_at)
      if (isNaN(start.getTime())) continue
      const diffHours = Math.floor((now.getTime() - start.getTime()) / (60 * 60 * 1000))
      if (diffHours < 0 || diffHours > 23) continue
      const bucket = buckets[23 - diffHours]
      if (!bucket) continue
      bucket.series[run.agent_id] = (bucket.series[run.agent_id] ?? 0) + 1
    }
    return buckets
  }, [runsData])

  // Container stats → array
  const containerEntries = useMemo<ContainerStatsEntry[]>(() => {
    const list = Array.from(containerStats.values())
    return list.map((e) => {
      if (e.crew_color) return e
      const crew = crews.find((c) => c.id === e.crew_id)
      return { ...e, crew_color: crew?.color ?? null, crew_slug: e.crew_slug ?? crew?.slug ?? null, crew_name: e.crew_name ?? crew?.name ?? null }
    })
  }, [containerStats, crews])

  // Inbox — mix escalations / keeper / reviews
  const inboxEntries = useMemo<InboxEntry[]>(() => {
    const entries: InboxEntry[] = []

    for (const k of keeperRequests.slice(0, 3)) {
      if (k.decision && k.decision !== "PENDING") continue
      entries.push({
        id: `keeper-${k.id}`,
        kind: "keeper",
        title: `Keeper request — ${k.credential_name}`,
        subtitle: `for @${k.agent_name}`,
        relative: formatRelativeShort(k.created_at),
        href: "/credentials?filter=pending",
      })
    }

    const reviews = missions.filter((m) => m.status === "REVIEW").slice(0, 3)
    for (const m of reviews) {
      entries.push({
        id: `review-${m.id}`,
        kind: "review",
        title: m.title,
        subtitle: m.identifier ? `${m.identifier} · awaiting review` : "awaiting review",
        relative: formatRelativeShort(m.updated_at),
        href: m.identifier ? `/issues/${m.identifier}` : "/issues",
      })
    }

    if (escalationCount > 0) {
      entries.unshift({
        id: "escalation-summary",
        kind: "escalation",
        title: `${escalationCount} unresolved escalation${escalationCount === 1 ? "" : "s"}`,
        subtitle: "click to triage",
        relative: "",
        href: "/inbox?filter=escalation",
      })
    }

    return entries.slice(0, 6)
  }, [keeperRequests, missions, escalationCount])

  // Recent missions — 5 newest by updated_at
  const recentMissions = useMemo(() => {
    return [...missions]
      .sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime())
      .slice(0, 5)
  }, [missions])

  // ── Render ──────────────────────────────────────────────────────────
  if (isLoading) {
    return (
      <div className="p-4 md:p-6 space-y-4">
        <div className="grid gap-4 grid-cols-2 sm:grid-cols-3 lg:grid-cols-6">
          {Array.from({ length: 6 }).map((_, i) => <Skeleton key={i} className="h-[112px] rounded-xl" />)}
        </div>
        <div className="grid gap-4 grid-cols-1 lg:grid-cols-3">
          <Skeleton className="h-[260px] rounded-xl lg:col-span-2" />
          <Skeleton className="h-[260px] rounded-xl" />
        </div>
        <Skeleton className="h-[200px] rounded-xl" />
      </div>
    )
  }

  return (
    <div className="p-4 md:p-6 pb-10 space-y-4 bg-background min-h-[calc(100vh-48px)]">
      {/* Post-onboarding welcome — self-gates on a localStorage flag
          set at the end of the wizard, dismisses persistently. Reads
          firstAgentId from the same localStorage breadcrumb so the
          "Open chat" CTA lands on the agent the user just created. */}
      <WelcomeChecklist
        firstAgentId={typeof window !== "undefined" ? window.localStorage.getItem("crewship.firstAgentId") : null}
      />

      {/* Recipes empty state — only when workspace has 0 crews. */}
      {crews.length === 0 && workspaceId && (
        <RecipesEmptyState
          workspaceId={workspaceId}
          onInstalled={() => { fetchData(); fetchTimeseries() }}
        />
      )}

      {/* ── Row 1: 6 KPI cards ─ responsive 2→3→6 cols ──────────── */}
      <div className="grid gap-4 grid-cols-2 sm:grid-cols-3 lg:grid-cols-6">
        <KpiCard
          label="Agents"
          value={totalAgents}
          subtitle={`${crews.length} crew${crews.length === 1 ? "" : "s"}`}
        />
        <KpiCard
          label="Running"
          value={runningNow}
          valueColor={runningNow > 0 ? "rgb(52, 211, 153)" : undefined}
          subtitle={totalAgents > 0 ? `of ${totalAgents} agents` : undefined}
        />
        <KpiCard
          label="Active missions"
          value={activeMissionCount}
          subtitle={`${metrics?.total_missions ?? 0} total`}
        />
        <KpiCard
          label="Open issues"
          value={openIssues}
          subtitle={openIssues === 0 ? "nothing open" : "in pipeline"}
        />
        <KpiCard
          label="Cost (24h)"
          value={totalCost24h > 0 ? formatCost(totalCost24h) : runsToday > 0 ? "—" : "$0.00"}
          subtitle={
            runsToday === 0
              ? "no runs today"
              : totalCost24h > 0
                ? `${runsToday} run${runsToday === 1 ? "" : "s"}`
                : "token tracking not wired"
          }
        />
        <KpiCard
          label="Success (24h)"
          value={successRateDisplay}
          valueColor={
            successRatePct == null ? undefined
              : successRatePct >= 90 ? "rgb(52, 211, 153)"
              : successRatePct >= 70 ? "rgb(251, 191, 36)"
              : "rgb(248, 113, 113)"
          }
          subtitle={runsFailed > 0 ? `${runsFailed} failed` : runsToday > 0 ? "all clean" : "no data"}
        />
      </div>

      {/* ── Row 2: Throughput (2fr) + Status donut (1fr) ─────────── */}
      <div className="grid gap-4 grid-cols-1 lg:grid-cols-3">
        <DashboardCard
          title="Issue throughput · 24h · by crew"
          icon={TrendingUp}
          hint={throughputTotal > 0 ? `${throughputTotal} closed` : "awaiting data"}
          className="lg:col-span-2"
        >
          {throughputData && throughputSeries.length > 0 ? (
            <ThroughputChart buckets={throughputBuckets} series={throughputSeries} height={180} />
          ) : (
            <div className="flex items-center justify-center h-[180px] text-[11px] text-muted-foreground/50">
              {throughputData ? "No issues closed in the last 24h" : "Metrics endpoint unavailable"}
            </div>
          )}
        </DashboardCard>

        <DashboardCard title="Mission status" icon={Target} hint={`${missions.length} total`}>
          {donutData.length > 0 ? (
            <StatusDonut data={donutData} />
          ) : (
            <div className="flex items-center justify-center h-[160px] text-[11px] text-muted-foreground/50">No missions yet</div>
          )}
        </DashboardCard>
      </div>

      {/* ── Row 3: Cost burn (2fr) + Top missions (1fr) ──────────── */}
      <div className="grid gap-4 grid-cols-1 lg:grid-cols-3">
        <DashboardCard
          title="Cost burn · 7 days"
          icon={Banknote}
          hint={costTotal > 0 ? `${formatCost(costTotal)} total` : "awaiting data"}
          className="lg:col-span-2"
        >
          {costData && costSeries.length > 0 ? (
            <CostBurnChart buckets={costBuckets} series={costSeries} height={160} />
          ) : (
            <div className="flex items-center justify-center h-[160px] text-[11px] text-muted-foreground/50">
              {costData ? "No cost data in the last 7 days" : "Metrics endpoint unavailable"}
            </div>
          )}
        </DashboardCard>

        <DashboardCard title="Top cost missions" icon={Gem} hint={`top ${topMissions.length}`}>
          <TopMissionsChart missions={topMissions} />
        </DashboardCard>
      </div>

      {/* ── Row 4: Container resources (live, full width) ────────── */}
      <DashboardCard title="Container resources · live" icon={Box} hint="via container.stats">
        <ContainerResourcesTile entries={containerEntries} />
      </DashboardCard>

      {/* ── Row 5: Heatmap (2fr) + Crew radial (1fr) + Projects (1fr) ──
          Mobile: each card on its own row.
          Tablet: heatmap full width, radial+projects side by side.
          Desktop: 4-col grid with heatmap spanning 2. */}
      <div className="grid gap-4 grid-cols-1 md:grid-cols-2 lg:grid-cols-4">
        <DashboardCard title="Agent activity · 24h" icon={Activity} hint="runs per hour" className="md:col-span-2">
          <AgentHeatmap agents={heatmapAgents} buckets={heatmapBuckets} />
        </DashboardCard>
        <DashboardCard title="Crew health" icon={HeartPulse} hint={`${crews.length} crew${crews.length === 1 ? "" : "s"}`}>
          <CrewRadial crews={crewHealth} />
        </DashboardCard>
        <DashboardCard title="Active projects" icon={FolderOpen} hint={`${projects.length} project${projects.length === 1 ? "" : "s"}`}>
          <ProjectProgress projects={projectEntries} />
        </DashboardCard>
      </div>

      {/* ── Row 6: Activity feed (2fr) + Inbox (1fr) ─────────────── */}
      <div className="grid gap-4 grid-cols-1 lg:grid-cols-3">
        <DashboardCard title="Live activity" icon={Radio} hint="streaming" className="lg:col-span-2">
          <ActivityFeed />
        </DashboardCard>
        <DashboardCard title="Inbox" icon={InboxIcon} hint={`${inboxEntries.length} item${inboxEntries.length === 1 ? "" : "s"}`}>
          <InboxTile entries={inboxEntries} />
        </DashboardCard>
      </div>

      {/* ── Row 7: Recent missions table ─────────────────────────── */}
      <DashboardCard
        title="Recent missions"
        icon={ListChecks}
        action={<Link href="/issues" className="text-[10px] hover:text-foreground">Issues →</Link>}
      >
        <RecentMissionsTable missions={recentMissions} />
      </DashboardCard>
    </div>
  )
}
