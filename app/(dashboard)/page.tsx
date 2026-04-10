"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { Search } from "lucide-react"
import Link from "next/link"

import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtime, useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { cn } from "@/lib/utils"
import type { Mission } from "@/lib/types/mission"

import { ActionCenter } from "@/components/features/dashboard/action-center"
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
import { CaptainTile } from "@/components/features/dashboard/captain-tile"
import { RecentMissionsTable } from "@/components/features/dashboard/recent-missions-table"

// ── Types for API responses we consume ────────────────────────────────

interface AgentSummary {
  id: string
  name: string
  slug: string
  role_title: string | null
  agent_role: string
  status: string
  crew: { name: string; slug: string; color: string | null } | null
  crew_id?: string | null
  _count: { skills: number; credentials: number; chats: number }
}

interface CrewSummary {
  id: string
  name: string
  slug: string
  color: string | null
  icon: string | null
}

interface ProjectSummary {
  id: string
  name: string
  color: string
  issue_count: number
  progress: number
}

interface RunEntry {
  id: string
  agent_id: string
  status: string
  started_at: string | null
  finished_at: string | null
  created_at: string
}

interface RunsResponse {
  data: RunEntry[]
  stats: { running: number; today: number; failed: number }
}

interface MissionMetricsResponse {
  active_missions: number
  total_missions: number
  completed_24h?: number
  failed_24h?: number
  total_cost_24h: number
}

interface KeeperRequest {
  id: string
  agent_name: string
  credential_name: string
  decision: string | null
  created_at: string
}

interface TimeseriesBucket {
  ts: string
  series: Record<string, number>
}
interface TimeseriesResponse {
  metric: string
  window: string
  bucket: string
  group_by: string
  buckets: TimeseriesBucket[]
  series_labels: Record<string, string>
}

// ── Crew color palette → CSS ──────────────────────────────────────────

const CREW_PALETTE: Record<string, string> = {
  blue: "rgb(96, 165, 250)",
  emerald: "rgb(52, 211, 153)",
  violet: "rgb(167, 139, 250)",
  amber: "rgb(251, 191, 36)",
  rose: "rgb(251, 113, 133)",
  cyan: "rgb(34, 211, 238)",
  lime: "rgb(163, 230, 53)",
  fuchsia: "rgb(232, 121, 249)",
}
function crewColor(paletteId: string | null | undefined): string {
  return CREW_PALETTE[paletteId ?? ""] ?? "rgb(148, 163, 184)"
}

// Status donut colors — aligned with orchestration board
const STATUS_PALETTE = {
  BACKLOG: "rgb(96, 165, 250)",
  TODO: "rgb(34, 211, 238)",
  IN_PROGRESS: "rgb(167, 139, 250)",
  REVIEW: "rgb(251, 191, 36)",
  COMPLETED: "rgb(52, 211, 153)",
  FAILED: "rgb(248, 113, 113)",
  CANCELLED: "rgb(148, 163, 184)",
} as const

function formatCost(cost: number): string {
  if (cost === 0) return "$0.00"
  if (cost < 0.01) return "<$0.01"
  return `$${cost.toFixed(2)}`
}

// Build an N-element fake trend series for KPI sparklines while the
// /metrics/timeseries endpoint isn't wired for every KPI yet.
// Deterministic per-label so it doesn't jitter on re-render.
function mockSparkline(seed: number, length = 24): number[] {
  const out: number[] = []
  for (let i = 0; i < length; i++) {
    const v = Math.sin((i + seed) * 0.4) * 3 + Math.cos(i * 0.2 + seed) * 1.5 + 5
    out.push(Math.max(0, v))
  }
  return out
}

function formatRelativeShort(iso: string | null | undefined): string {
  if (!iso) return ""
  const ts = new Date(iso).getTime()
  if (isNaN(ts)) return ""
  const diffSec = Math.floor((Date.now() - ts) / 1000)
  if (diffSec < 60) return `${diffSec}s`
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m`
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h`
  return `${Math.floor(diffSec / 86400)}d`
}

export default function DashboardPage() {
  const router = useRouter()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { status: wsStatus } = useRealtime()

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
      if (agentsRes.ok) setAgents(await agentsRes.json())
      if (crewsRes.ok) setCrews(await crewsRes.json())
      if (projectsRes.ok) setProjects(await projectsRes.json())
      if (missionsRes.ok) setMissions(await missionsRes.json())
      if (metricsRes.ok) setMetrics(await metricsRes.json())
      if (runsRes.ok) setRunsData(await runsRes.json())
      if (escCountRes.ok) {
        const data = await escCountRes.json()
        setEscalationCount(Number(data?.count) || 0)
      }
      if (keeperRes.ok) {
        const data = await keeperRes.json()
        setKeeperRequests(Array.isArray(data) ? data : (data?.data ?? []))
      }
    } catch {
      // silent — empty cards will show empty states
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
      if (thruRes.ok) setThroughputData(await thruRes.json())
      if (costRes.ok) setCostData(await costRes.json())
    } catch {
      // metrics endpoint may not be available yet; charts will show empty state
    }
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
    if (typeof p.container_id !== "string") return
    setContainerStats((prev) => {
      const next = new Map(prev)
      const existing = next.get(p.container_id)
      const cpu = Number(p.cpu_percent) || 0
      const history = existing?.cpu_history ? [...existing.cpu_history, cpu] : [cpu]
      if (history.length > 30) history.shift()
      next.set(p.container_id, {
        container_id: p.container_id,
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
  const successRate = runsToday > 0 ? Math.round(((runsToday - runsFailed) / runsToday) * 100) : 100
  const missionsInReview = missions.filter((m) => m.status === "REVIEW").length

  const openIssues = missions.filter(
    (m) => (m.mission_type === "issue" || !m.mission_type) && m.status !== "COMPLETED" && m.status !== "CANCELLED" && m.status !== "FAILED",
  ).length

  const pendingKeeperCount = keeperRequests.filter((k) => !k.decision || k.decision === "PENDING").length

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
        crew_color: crewColor(crew?.color),
        cost: m.total_estimated_cost ?? 0,
        href: m.identifier ? `/orchestration/issues/${m.identifier}` : "/orchestration",
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
      href: `/orchestration?project=${encodeURIComponent(p.id)}`,
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
        href: m.identifier ? `/orchestration/issues/${m.identifier}` : "/orchestration",
      })
    }

    if (escalationCount > 0) {
      entries.unshift({
        id: "escalation-summary",
        kind: "escalation",
        title: `${escalationCount} unresolved escalation${escalationCount === 1 ? "" : "s"}`,
        subtitle: "click to triage",
        relative: "",
        href: "/orchestration?tab=escalations",
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
      <div className="p-3 space-y-3">
        <Skeleton className="h-9 rounded-lg" />
        <Skeleton className="h-12 rounded-lg" />
        <div className="grid gap-2.5" style={{ gridTemplateColumns: "repeat(6, minmax(0,1fr))" }}>
          {Array.from({ length: 6 }).map((_, i) => <Skeleton key={i} className="h-[96px] rounded-lg" />)}
        </div>
        <div className="grid gap-2.5" style={{ gridTemplateColumns: "2fr 1fr" }}>
          <Skeleton className="h-[240px] rounded-lg" />
          <Skeleton className="h-[240px] rounded-lg" />
        </div>
        <Skeleton className="h-[200px] rounded-lg" />
      </div>
    )
  }

  return (
    <div className="p-3 pb-8 space-y-3 bg-background min-h-[calc(100vh-48px)]">
      {/* ── Toolbar ───────────────────────────────────────────────── */}
      <div className="flex items-center gap-2 h-9 px-2 sm:px-3 rounded-lg border border-border bg-card">
        <span className="text-[12px] font-medium text-foreground/80 px-1.5">🗺 Overview</span>
        <span className="flex-1" />
        <div className="hidden md:flex items-center gap-1.5 h-[26px] px-2 bg-white/[0.04] border border-border rounded-md text-[11px] text-muted-foreground min-w-[220px]">
          <Search className="h-3 w-3" />
          <span>Search agents, missions, issues…</span>
          <kbd className="ml-auto text-[9px] font-mono bg-white/[0.06] border border-border rounded px-1 py-0.5">⌘K</kbd>
        </div>
        <Link
          href="/captain"
          className="inline-flex items-center gap-1.5 h-[26px] px-2.5 text-[11px] font-medium rounded-md border border-border bg-white/[0.03] hover:bg-white/[0.06] text-foreground/80 transition-colors"
        >
          ⌁ Ask Captain
        </Link>
        <div
          title={
            wsStatus === "connected" ? "Live — realtime updates active"
              : wsStatus === "connecting" ? "Connecting to realtime…"
              : "Realtime disconnected"
          }
          className={cn(
            "inline-flex items-center gap-1.5 h-[26px] px-2 rounded-md text-[10px] font-semibold uppercase tracking-wide border",
            wsStatus === "connected" ? "text-emerald-400 border-emerald-500/30 bg-emerald-500/10"
              : wsStatus === "connecting" ? "text-amber-400 border-amber-500/30 bg-amber-500/10"
              : "text-red-400 border-red-500/30 bg-red-500/10",
          )}
        >
          <span className="relative flex h-1.5 w-1.5">
            {wsStatus === "connected" && (
              <span className="absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-60 animate-ping" />
            )}
            <span className={cn(
              "relative inline-flex h-1.5 w-1.5 rounded-full",
              wsStatus === "connected" && "bg-emerald-400",
              wsStatus === "connecting" && "bg-amber-400",
              wsStatus !== "connected" && wsStatus !== "connecting" && "bg-red-400",
            )} />
          </span>
          {wsStatus === "connected" ? "Live" : wsStatus === "connecting" ? "Connecting" : "Offline"}
        </div>
      </div>

      {/* ── Action Center ────────────────────────────────────────── */}
      <ActionCenter
        escalations={escalationCount}
        keeperRequests={pendingKeeperCount}
        missionsInReview={missionsInReview}
        proposals={0}
        mentions={0}
      />

      {/* ── Row 1: 6 KPI cards ───────────────────────────────────── */}
      <div className="grid gap-2.5" style={{ gridTemplateColumns: "repeat(6, minmax(0,1fr))" }}>
        <KpiCard
          label="Agents"
          value={totalAgents}
          subtitle={`${crews.length} crew${crews.length === 1 ? "" : "s"}`}
          sparklineData={mockSparkline(1)}
          sparklineColor="rgb(96, 165, 250)"
        />
        <KpiCard
          label="Running now"
          value={runningNow}
          valueColor={runningNow > 0 ? "rgb(52, 211, 153)" : undefined}
          subtitle={`of ${totalAgents} agents`}
          sparklineData={mockSparkline(2)}
          sparklineColor="rgb(52, 211, 153)"
        />
        <KpiCard
          label="Active missions"
          value={activeMissionCount}
          subtitle={`${metrics?.total_missions ?? 0} total`}
          sparklineData={mockSparkline(3)}
          sparklineColor="rgb(167, 139, 250)"
        />
        <KpiCard
          label="Open issues"
          value={openIssues}
          subtitle={openIssues === 0 ? "nothing open" : `in pipeline`}
          sparklineData={mockSparkline(4)}
          sparklineColor="rgb(251, 191, 36)"
        />
        <KpiCard
          label="Cost (24h)"
          value={formatCost(totalCost24h)}
          subtitle={runsToday > 0 ? `${runsToday} run${runsToday === 1 ? "" : "s"}` : "no runs"}
          sparklineData={mockSparkline(5)}
          sparklineColor="rgb(34, 211, 238)"
        />
        <KpiCard
          label="Success rate"
          value={`${successRate}%`}
          valueColor={successRate >= 90 ? "rgb(52, 211, 153)" : successRate >= 70 ? "rgb(251, 191, 36)" : "rgb(248, 113, 113)"}
          subtitle={runsFailed > 0 ? `${runsFailed} failed today` : "all clean"}
          sparklineData={mockSparkline(6)}
          sparklineColor="rgb(52, 211, 153)"
        />
      </div>

      {/* ── Row 2: Throughput + Status donut ─────────────────────── */}
      <div className="grid gap-2.5" style={{ gridTemplateColumns: "2fr 1fr" }}>
        <DashboardCard
          title="📈 Issue throughput · 24h · by crew"
          hint={throughputTotal > 0 ? `${throughputTotal} closed` : "awaiting data"}
        >
          {throughputData && throughputSeries.length > 0 ? (
            <ThroughputChart buckets={throughputBuckets} series={throughputSeries} height={180} />
          ) : (
            <div className="flex items-center justify-center h-[180px] text-[11px] text-muted-foreground/50">
              {throughputData ? "No issues closed in the last 24h" : "Metrics endpoint unavailable"}
            </div>
          )}
        </DashboardCard>

        <DashboardCard title="🎯 Mission status" hint={`${missions.length} total`}>
          {donutData.length > 0 ? (
            <StatusDonut data={donutData} />
          ) : (
            <div className="flex items-center justify-center h-[160px] text-[11px] text-muted-foreground/50">No missions yet</div>
          )}
        </DashboardCard>
      </div>

      {/* ── Row 3: Cost burn + Top missions ──────────────────────── */}
      <div className="grid gap-2.5" style={{ gridTemplateColumns: "2fr 1fr" }}>
        <DashboardCard
          title="💸 Cost burn · 7 days"
          hint={costTotal > 0 ? `${formatCost(costTotal)} total` : "awaiting data"}
        >
          {costData && costSeries.length > 0 ? (
            <CostBurnChart buckets={costBuckets} series={costSeries} height={160} />
          ) : (
            <div className="flex items-center justify-center h-[160px] text-[11px] text-muted-foreground/50">
              {costData ? "No cost data in the last 7 days" : "Metrics endpoint unavailable"}
            </div>
          )}
        </DashboardCard>

        <DashboardCard title="💎 Top cost missions" hint={`top ${topMissions.length}`}>
          <TopMissionsChart missions={topMissions} />
        </DashboardCard>
      </div>

      {/* ── Row 4: Container resources (live) ────────────────────── */}
      <DashboardCard title="🐳 Container resources · live" hint="via container.stats">
        <ContainerResourcesTile entries={containerEntries} />
      </DashboardCard>

      {/* ── Row 5: Heatmap + Crew radial + Projects ──────────────── */}
      <div className="grid gap-2.5" style={{ gridTemplateColumns: "1.5fr 1fr 1fr" }}>
        <DashboardCard title="🔥 Agent activity · 24h" hint="runs per hour">
          <AgentHeatmap agents={heatmapAgents} buckets={heatmapBuckets} />
        </DashboardCard>
        <DashboardCard title="🛟 Crew health" hint={`${crews.length} crew${crews.length === 1 ? "" : "s"}`}>
          <CrewRadial crews={crewHealth} />
        </DashboardCard>
        <DashboardCard title="📁 Active projects" hint={`${projects.length} project${projects.length === 1 ? "" : "s"}`}>
          <ProjectProgress projects={projectEntries} />
        </DashboardCard>
      </div>

      {/* ── Row 6: Activity feed + Inbox + Captain ───────────────── */}
      <div className="grid gap-2.5" style={{ gridTemplateColumns: "1.5fr 1fr 1fr" }}>
        <DashboardCard title="📡 Live activity" hint="streaming">
          <ActivityFeed />
        </DashboardCard>
        <DashboardCard title="📬 Inbox" hint={`${inboxEntries.length} item${inboxEntries.length === 1 ? "" : "s"}`}>
          <InboxTile entries={inboxEntries} />
        </DashboardCard>
        <DashboardCard title="⌁ Captain" hint="workspace AI">
          <CaptainTile />
        </DashboardCard>
      </div>

      {/* ── Row 7: Recent missions table ─────────────────────────── */}
      <DashboardCard
        title="🗂 Recent missions"
        action={<Link href="/orchestration" className="text-[10px] hover:text-foreground">Orchestration →</Link>}
      >
        <RecentMissionsTable missions={recentMissions} />
      </DashboardCard>
    </div>
  )
}
