"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { Bot, Hourglass, Key, Activity, Plus, Play, CheckCircle, XCircle, Clock, AlertTriangle, MoreHorizontal, MessageSquare, FileText, ScrollText, AlertCircle, Pause, Target, Coins, Loader2, Square, ChevronRight, CheckCircle2, CircleDot } from "lucide-react"
import { BotIcon as AnimatedBot } from "@/components/ui/bot"
import { ActivityIcon as AnimatedActivity } from "@/components/ui/activity"

import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { PageShell, type PageShellStatItem } from "@/components/layout/page-shell"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
import { StatusBadge, StatusDot } from "@/components/ui/status-badge"
import { Skeleton } from "@/components/ui/skeleton"
import { SetupNudge } from "@/components/features/onboarding/setup-nudge"
import { useWorkspace } from "@/hooks/use-workspace"
import { useTick } from "@/hooks/use-tick"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import Link from "next/link"
import { formatRelativeTime } from "@/lib/time"
import { cn } from "@/lib/utils"
import { getCrewBgClass } from "@/lib/colors"
import type { Mission } from "@/lib/types/mission"

interface AgentCrew {
  name: string
  slug: string
  color: string | null
}

interface Agent {
  id: string
  name: string
  slug: string
  description: string | null
  role_title: string | null
  agent_role: string
  status: string
  cli_adapter: string
  llm_provider: string
  llm_model: string
  crew: AgentCrew | null
  _count: { skills: number; credentials: number; chats: number }
}

interface Credential {
  id: string
}

interface RunEntry {
  id: string
  agent_id: string
  status: string
  trigger_type: string
  started_at: string | null
  finished_at: string | null
  error_message: string | null
  created_at: string
  agent_name: string | null
  agent_slug: string | null
  crew_name: string | null
}

interface RunsResponse {
  data: RunEntry[]
  stats: { running: number; today: number; failed: number }
}

const runStatusLabels: Record<string, string> = {
  PENDING: "Pending",
  RUNNING: "Running",
  COMPLETED: "Completed",
  FAILED: "Failed",
  CANCELLED: "Cancelled",
  TIMEOUT: "Timeout",
}

const runStatusIcons: Record<string, React.ElementType> = {
  PENDING: Clock,
  RUNNING: Play,
  COMPLETED: CheckCircle,
  FAILED: XCircle,
  CANCELLED: XCircle,
  TIMEOUT: AlertTriangle,
}

// Map run status → the StatusBadge canonical status keys in lib/colors.ts
const runStatusToBadge: Record<string, string> = {
  PENDING: "PENDING",
  RUNNING: "IN_PROGRESS",
  COMPLETED: "COMPLETED",
  FAILED: "FAILED",
  CANCELLED: "CANCELLED",
  TIMEOUT: "FAILED",
}

const agentStatusLabels: Record<string, string> = {
  IDLE: "Idle",
  RUNNING: "Running",
  ERROR: "Error",
  STOPPED: "Stopped",
}

const agentStatusToBadge: Record<string, string> = {
  IDLE: "PENDING",
  RUNNING: "IN_PROGRESS",
  ERROR: "FAILED",
  STOPPED: "CANCELLED",
}

const agentStatusIcons: Record<string, React.ElementType> = {
  ERROR: AlertCircle,
  STOPPED: Pause,
}

const missionStatusLabels: Record<string, string> = {
  PLANNING: "Planning",
  IN_PROGRESS: "Running",
  REVIEW: "Review",
  COMPLETED: "Completed",
  FAILED: "Failed",
  CANCELLED: "Cancelled",
}

const missionStatusIcons: Record<string, React.ElementType> = {
  PLANNING: Clock,
  IN_PROGRESS: Loader2,
  REVIEW: ChevronRight,
  COMPLETED: CheckCircle2,
  FAILED: AlertTriangle,
  CANCELLED: Square,
}

function formatDuration(ms: number): string {
  if (ms < 60_000) return `${Math.round(ms / 1000)}s`
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)}m`
  return `${Math.floor(ms / 3_600_000)}h ${Math.floor((ms % 3_600_000) / 60_000)}m`
}

function formatCost(cost: number): string {
  if (cost === 0) return "$0.00"
  if (cost < 0.01) return "<$0.01"
  return `$${cost.toFixed(2)}`
}

export default function DashboardPage() {
  const router = useRouter()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [agents, setAgents] = useState<Agent[]>([])
  const [credentials, setCredentials] = useState<Credential[]>([])
  const [crewCount, setCrewCount] = useState(0)
  const [missions, setMissions] = useState<Mission[]>([])
  const [metrics, setMetrics] = useState<{ active_missions: number; total_cost_24h: number; total_missions: number } | null>(null)
  const [openIssueCount, setOpenIssueCount] = useState(0)
  const [runsData, setRunsData] = useState<RunsResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [activeFilter, setActiveFilter] = useState("all")
  const [onboardingChecked, setOnboardingChecked] = useState(false)
  const [containerStats, setContainerStats] = useState<Map<string, { crew_id: string; cpu_percent: number; memory_used: number; memory_limit: number; memory_percent: number; pids: number }>>(new Map())

  useTick(1000)

  // Check onboarding status on mount
  useEffect(() => {
    fetch("/api/v1/onboarding/status")
      .then((res) => {
        if (!res.ok) return null
        return res.json()
      })
      .then((data) => {
        if (data && !data.completed) {
          router.push("/onboarding")
          return
        }
        setOnboardingChecked(true)
      })
      .catch(() => setOnboardingChecked(true))
  }, [router])

  const fetchData = useCallback(async (showLoading = true) => {
    if (!workspaceId) return
    if (showLoading) {
      setLoading(true)
      setError(null)
    }
    try {
      const [agentsRes, credsRes, crewsRes, runsRes, missionsRes, metricsRes, issuesRes] = await Promise.all([
        fetch(`/api/v1/agents?workspace_id=${workspaceId}`),
        fetch(`/api/v1/credentials?workspace_id=${workspaceId}`),
        fetch(`/api/v1/crews?workspace_id=${workspaceId}`),
        fetch(`/api/v1/runs?workspace_id=${workspaceId}&limit=50`),
        fetch(`/api/v1/missions?workspace_id=${workspaceId}&limit=50&include_tasks=true`),
        fetch(`/api/v1/mission-metrics?workspace_id=${workspaceId}`),
        fetch(`/api/v1/issues?workspace_id=${workspaceId}&status=BACKLOG,TODO,IN_PROGRESS,REVIEW&limit=100`),
      ])

      if (!agentsRes.ok || !credsRes.ok) {
        setError("Failed to load dashboard data")
        return
      }

      const [agentsData, credsData] = await Promise.all([
        agentsRes.json() as Promise<Agent[]>,
        credsRes.json() as Promise<Credential[]>,
      ])

      const crewsData = crewsRes.ok ? ((await crewsRes.json()) as unknown[]) : []
      const runsResult = runsRes.ok ? ((await runsRes.json()) as RunsResponse) : null
      const missionsData = missionsRes.ok ? ((await missionsRes.json()) as Mission[]) : []
      const metricsData = metricsRes.ok ? await metricsRes.json() : null
      const issuesData = issuesRes.ok ? ((await issuesRes.json()) as unknown[]) : []

      setAgents(agentsData)
      setCredentials(credsData)
      setCrewCount(crewsData.length)
      setRunsData(runsResult)
      setMissions(missionsData)
      setMetrics(metricsData)
      setOpenIssueCount(issuesData.length)
    } catch {
      if (showLoading) setError("Failed to load dashboard data")
    } finally {
      if (showLoading) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    if (!workspaceId || !onboardingChecked) return
    fetchData()
  }, [workspaceId, onboardingChecked, fetchData])

  // Real-time: debounced refetch on dashboard events (prevents burst of 9 concurrent fetches)
  const dashDebounceRef = useRef<ReturnType<typeof setTimeout>>(undefined)
  const debouncedFetchData = useCallback(() => {
    clearTimeout(dashDebounceRef.current)
    dashDebounceRef.current = setTimeout(() => fetchData(false), 200)
  }, [fetchData])

  useRealtimeEvent("run.started", debouncedFetchData)
  useRealtimeEvent("run.completed", debouncedFetchData)
  useRealtimeEvent("run.failed", debouncedFetchData)
  useRealtimeEvent("agent.status", debouncedFetchData)
  useRealtimeEvent("mission.updated", debouncedFetchData)
  useRealtimeEvent("task.updated", debouncedFetchData)
  useRealtimeEvent("escalation.created", debouncedFetchData)
  useRealtimeEvent("agent.created", debouncedFetchData)
  useRealtimeEvent("agent.deleted", debouncedFetchData)

  useRealtimeEvent("container.stats", useCallback((event: RealtimeEvent) => {
    const p = event.payload
    if (typeof p.container_id !== "string") return
    setContainerStats(prev => {
      const next = new Map(prev)
      next.set(p.container_id, {
        crew_id: String(p.crew_id ?? ""),
        cpu_percent: Number(p.cpu_percent) || 0,
        memory_used: Number(p.memory_used) || 0,
        memory_limit: Number(p.memory_limit) || 0,
        memory_percent: Number(p.memory_percent) || 0,
        pids: Number(p.pids) || 0,
      })
      return next
    })
  }, []))

  const isLoading = wsLoading || loading

  const totalAgents = agents.length
  const runningNow = agents.filter((a) => a.status === "RUNNING").length
  const apiKeysActive = credentials.length

  // Mission stats from metrics API (accurate, not capped by limit=50)
  const activeMissionCount = metrics?.active_missions ?? 0
  const totalCost24h = metrics?.total_cost_24h ?? 0
  const totalMissionCount = metrics?.total_missions ?? 0

  // Recent missions sorted by activity
  const recentMissions = useMemo(() =>
    [...missions]
      .sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime())
      .slice(0, 10),
    [missions],
  )

  // Build agent → last run map (keep most recent per agent)
  const agentLastRun = useMemo(() => {
    const map = new Map<string, RunEntry>()
    for (const run of runsData?.data ?? []) {
      const existing = map.get(run.agent_id)
      if (!existing) {
        map.set(run.agent_id, run)
        continue
      }
      const tsExisting = new Date(existing.started_at ?? existing.created_at).getTime()
      const tsNew = new Date(run.started_at ?? run.created_at).getTime()
      if (tsNew > tsExisting) map.set(run.agent_id, run)
    }
    return map
  }, [runsData])

  // Filter agents
  const filteredAgents = useMemo(() =>
    activeFilter === "all"
      ? agents
      : agents.filter((a) => a.status === activeFilter.toUpperCase()),
    [agents, activeFilter],
  )

  // Sort: RUNNING first, then by last activity (newest first), no activity last
  const sortedAgents = useMemo(() =>
    [...filteredAgents].sort((a, b) => {
      if (a.status === "RUNNING" && b.status !== "RUNNING") return -1
      if (b.status === "RUNNING" && a.status !== "RUNNING") return 1

      const runA = agentLastRun.get(a.id)
      const runB = agentLastRun.get(b.id)

      if (runA && !runB) return -1
      if (runB && !runA) return 1
      if (!runA && !runB) return 0

      const tsA = new Date(runA!.started_at ?? runA!.created_at).getTime()
      const tsB = new Date(runB!.started_at ?? runB!.created_at).getTime()
      return tsB - tsA
    }),
    [filteredAgents, agentLastRun],
  )

  if (error) {
    return (
      <div className="p-6 space-y-6">
        <PageHeader title="Dashboard" description="Overview of your AI workforce" />
        <p className="text-body text-destructive">{error}</p>
      </div>
    )
  }

  const dashboardStats: PageShellStatItem[] = isLoading
    ? []
    : [
        {
          title: "Total Agents",
          value: totalAgents,
          subtitle: totalAgents === 0 ? "No agents yet" : `${totalAgents} agent${totalAgents === 1 ? "" : "s"}`,
          icon: Bot,
          iconClassName: "bg-primary/10 text-primary",
          animatedIcon: <AnimatedBot size={16} />,
        },
        {
          title: "Running Now",
          value: runningNow,
          subtitle: `of ${totalAgents} agents`,
          icon: Activity,
          iconClassName: "bg-primary/10 text-primary",
          animatedIcon: <AnimatedActivity size={16} />,
        },
        {
          title: "Active Missions",
          value: activeMissionCount,
          subtitle: totalMissionCount === 0 ? "No missions yet" : `${totalMissionCount} total`,
          icon: Target,
          iconClassName: "bg-primary/10 text-primary",
        },
        {
          title: "Open Issues",
          value: openIssueCount,
          subtitle: openIssueCount === 0 ? "No open issues" : `${openIssueCount} issue${openIssueCount === 1 ? "" : "s"} open`,
          icon: CircleDot,
          iconClassName: "bg-primary/10 text-primary",
        },
        {
          title: "Today's Runs",
          value: runsData?.stats.today ?? 0,
          subtitle:
            runsData?.stats.failed
              ? `${runsData.stats.failed} failed`
              : runsData?.stats.today
                ? `${runsData.stats.today} run${runsData.stats.today === 1 ? "" : "s"} today`
                : "No runs today",
          icon: Hourglass,
          iconClassName: runsData?.stats.failed ? "bg-destructive/10 text-destructive" : undefined,
        },
        {
          title: "Cost (24h)",
          value: formatCost(totalCost24h),
          subtitle: totalCost24h === 0 ? "No cost tracked" : "last 24 hours",
          icon: Coins,
          iconClassName: "bg-primary/10 text-primary",
        },
        {
          title: "API Keys Active",
          value: apiKeysActive,
          subtitle: apiKeysActive === 0 ? "Add credentials to get started" : `${apiKeysActive} key${apiKeysActive === 1 ? "" : "s"} configured`,
          icon: Key,
        },
      ]

  return (
    <PageShell
      title="Dashboard"
      description="Overview of your AI workforce"
      actions={
        <Button asChild>
          <Link href="/agents/new">
            <Plus className="mr-2 h-4 w-4" />
            New Agent
          </Link>
        </Button>
      }
      stats={isLoading ? undefined : dashboardStats}
    >
      {isLoading && (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {Array.from({ length: 7 }).map((_, i) => (
            <Skeleton key={i} className="h-[104px] rounded-xl" />
          ))}
        </div>
      )}

      {!isLoading && (
        <SetupNudge
          crewCount={crewCount}
          agentCount={totalAgents}
          credentialCount={apiKeysActive}
        />
      )}

      {containerStats.size > 0 && (
        <Card>
          <CardHeader className="pb-3">
            <CardTitle className="text-heading font-semibold">Container Resources</CardTitle>
          </CardHeader>
          <CardContent>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Crew</TableHead>
                  <TableHead>CPU</TableHead>
                  <TableHead>Memory</TableHead>
                  <TableHead>PIDs</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {Array.from(containerStats.entries()).map(([containerId, stats]) => {
                  const cpuColor = stats.cpu_percent > 80 ? "text-destructive" : stats.cpu_percent > 50 ? "text-foreground" : "text-muted-foreground"
                  const memColor = stats.memory_percent > 80 ? "text-destructive" : stats.memory_percent > 50 ? "text-foreground" : "text-muted-foreground"
                  const memMB = Math.round(stats.memory_used / 1024 / 1024)
                  const memLimitMB = Math.round(stats.memory_limit / 1024 / 1024)
                  return (
                    <TableRow key={containerId}>
                      <TableCell className="font-mono text-micro">{stats.crew_id.slice(0, 8)}…</TableCell>
                      <TableCell className={cn(cpuColor, "font-medium text-micro")}>{stats.cpu_percent.toFixed(1)}%</TableCell>
                      <TableCell className={cn(memColor, "text-micro")}>{memMB} / {memLimitMB} MB ({stats.memory_percent.toFixed(0)}%)</TableCell>
                      <TableCell className="text-micro">{stats.pids}</TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      {/* Missions Table */}
      {!isLoading && recentMissions.length > 0 && (
        <Card>
          <CardHeader className="pb-3">
            <div className="flex items-center justify-between">
              <CardTitle className="text-heading font-semibold">Recent Missions</CardTitle>
              <Button variant="ghost" size="sm" asChild>
                <Link href="/orchestration">View All</Link>
              </Button>
            </div>
          </CardHeader>
          <CardContent className="pt-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Mission</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Progress</TableHead>
                  <TableHead>Lead</TableHead>
                  <TableHead>Cost</TableHead>
                  <TableHead>Updated</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {recentMissions.map((mission) => {
                  const label = missionStatusLabels[mission.status] ?? mission.status
                  const StatusIcon = missionStatusIcons[mission.status] ?? Clock
                  const stats = mission.task_stats
                  const completed = stats?.completed ?? 0
                  const total = stats?.total ?? 0
                  const progressPct = total > 0 ? Math.round((completed / total) * 100) : 0
                  const duration = mission.completed_at && mission.created_at
                    ? new Date(mission.completed_at).getTime() - new Date(mission.created_at).getTime()
                    : null

                  return (
                    <TableRow key={mission.id}>
                      <TableCell>
                        <Link href="/orchestration" className="hover:underline">
                          <div className="font-medium text-body max-w-[240px] truncate">{mission.title}</div>
                          {mission.pattern && (
                            <div className="text-label text-muted-foreground">{mission.pattern.toLowerCase()}</div>
                          )}
                        </Link>
                      </TableCell>
                      <TableCell>
                        <StatusBadge status={mission.status} label={
                          <span className="inline-flex items-center gap-1.5">
                            {mission.status === "IN_PROGRESS" && (
                              <StatusDot status="IN_PROGRESS" live className="h-1.5 w-1.5" />
                            )}
                            <StatusIcon className="h-3 w-3" />
                            {label}
                          </span>
                        } />
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-2 min-w-[100px]">
                          <div className="flex-1 h-1.5 rounded-full bg-muted overflow-hidden">
                            <div
                              className="h-full rounded-full bg-primary transition-all duration-500"
                              style={{ width: `${progressPct}%` }}
                            />
                          </div>
                          <span className="text-label text-muted-foreground tabular-nums">
                            {completed}/{total}
                          </span>
                        </div>
                      </TableCell>
                      <TableCell>
                        <span className="text-body text-muted-foreground">
                          {mission.lead_agent_name ?? "—"}
                        </span>
                      </TableCell>
                      <TableCell>
                        <span className="text-body text-muted-foreground tabular-nums">
                          {mission.total_estimated_cost != null ? formatCost(mission.total_estimated_cost) : "—"}
                        </span>
                      </TableCell>
                      <TableCell>
                        <span className="text-body text-muted-foreground">
                          {formatRelativeTime(mission.updated_at)}
                          {duration !== null && (
                            <span className="text-label ml-1">({formatDuration(duration)})</span>
                          )}
                        </span>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      {/* Agents Table */}
      <Card>
        <CardHeader className="pb-3">
          <div className="flex items-center justify-between">
            <CardTitle className="text-heading font-semibold">Agents</CardTitle>
            <Select value={activeFilter} onValueChange={setActiveFilter}>
              <SelectTrigger size="sm" className="w-[120px]">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All</SelectItem>
                <SelectItem value="running">Running</SelectItem>
                <SelectItem value="idle">Idle</SelectItem>
                <SelectItem value="error">Error</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </CardHeader>
        <CardContent className="pt-0">
          {isLoading ? (
            <div className="space-y-3">
              {Array.from({ length: 3 }).map((_, i) => (
                <Skeleton key={i} className="h-12 rounded-md" />
              ))}
            </div>
          ) : sortedAgents.length === 0 ? (
            <EmptyState
              icon={Bot}
              title={agents.length === 0 ? "No agents yet" : "No matching agents"}
              description={
                agents.length === 0
                  ? "Create your first AI agent to start automating tasks. Agents work in crews and can chat, run tasks, and produce files."
                  : "No agents match the current filter. Try changing the filter."
              }
            >
              {agents.length === 0 && (
                <Button className="mt-4" asChild>
                  <Link href="/agents/new">
                    <Plus className="mr-2 h-4 w-4" />
                    Create First Agent
                  </Link>
                </Button>
              )}
            </EmptyState>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Agent</TableHead>
                  <TableHead>Crew</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Last Activity</TableHead>
                  <TableHead className="text-right">Sessions</TableHead>
                  <TableHead className="w-[50px]" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {sortedAgents.map((agent) => {
                  const lastRun = agentLastRun.get(agent.id)
                  const agentBadgeStatus = agentStatusToBadge[agent.status] ?? "PENDING"
                  const agentLabel = agentStatusLabels[agent.status] ?? agent.status
                  const StatusIcon = agentStatusIcons[agent.status]
                  const runBadgeStatus = lastRun ? (runStatusToBadge[lastRun.status] ?? "PENDING") : null
                  const runLabel = lastRun ? (runStatusLabels[lastRun.status] ?? lastRun.status) : null
                  const RunIcon = lastRun ? (runStatusIcons[lastRun.status] ?? Clock) : null

                  return (
                    <TableRow key={agent.id} className="transition-colors duration-500">
                      <TableCell>
                        <Link href={`/agents/${agent.id}`} className="hover:underline">
                          <div className="font-medium text-body">{agent.name}</div>
                          {agent.role_title && (
                            <div className="text-label text-muted-foreground">{agent.role_title}</div>
                          )}
                        </Link>
                      </TableCell>
                      <TableCell>
                        {agent.crew ? (
                          <div className="flex items-center gap-1.5">
                            <span className={cn("h-2 w-2 rounded-full shrink-0", getCrewBgClass(agent.crew.color))} />
                            <span className="text-body">{agent.crew.name}</span>
                          </div>
                        ) : (
                          <span className="text-body text-muted-foreground">&mdash;</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <StatusBadge
                          status={agentBadgeStatus}
                          label={
                            <span className="inline-flex items-center gap-1.5">
                              {agent.status === "RUNNING" && (
                                <StatusDot status="IN_PROGRESS" live className="h-1.5 w-1.5" />
                              )}
                              {StatusIcon && <StatusIcon className="h-3 w-3" />}
                              {agentLabel}
                            </span>
                          }
                        />
                      </TableCell>
                      <TableCell>
                        {lastRun && runBadgeStatus && RunIcon ? (
                          <div className="flex items-center gap-1.5">
                            <StatusDot status={runBadgeStatus} className="h-1.5 w-1.5" />
                            <RunIcon className="h-3.5 w-3.5 text-muted-foreground" />
                            <span className="text-body text-muted-foreground">
                              {runLabel} {formatRelativeTime(lastRun.started_at ?? lastRun.created_at)}
                            </span>
                          </div>
                        ) : (
                          <span className="text-body text-muted-foreground">No activity</span>
                        )}
                      </TableCell>
                      <TableCell className="text-right">
                        <span className="text-body text-muted-foreground">{agent._count.chats}</span>
                      </TableCell>
                      <TableCell>
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <Button variant="ghost" size="icon" className="h-8 w-8">
                              <MoreHorizontal className="h-4 w-4" />
                              <span className="sr-only">Actions</span>
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem asChild>
                              <Link href={`/agents/${agent.id}/chat`}>
                                <MessageSquare className="h-4 w-4" />
                                Open Chat
                              </Link>
                            </DropdownMenuItem>
                            <DropdownMenuItem asChild>
                              <Link href={`/agents/${agent.id}`}>
                                <FileText className="h-4 w-4" />
                                View Detail
                              </Link>
                            </DropdownMenuItem>
                            <DropdownMenuItem asChild>
                              <Link href={`/agents/${agent.id}/logs`}>
                                <ScrollText className="h-4 w-4" />
                                Logs
                              </Link>
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </PageShell>
  )
}
