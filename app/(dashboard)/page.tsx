"use client"

import { useCallback, useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { Bot, Hourglass, Key, Activity, Plus, Play, CheckCircle, XCircle, Clock, AlertTriangle, MoreHorizontal, MessageSquare, FileText, ScrollText } from "lucide-react"
import { BotIcon as AnimatedBot } from "@/components/ui/bot"
import { ActivityIcon as AnimatedActivity } from "@/components/ui/activity"
import { KeyIcon as AnimatedKey } from "@/components/ui/key"

import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
import { StatCard } from "@/components/layout/stat-card"
import { Skeleton } from "@/components/ui/skeleton"
import { SetupNudge } from "@/components/features/onboarding/setup-nudge"
import { useWorkspace } from "@/hooks/use-workspace"
import { useTick } from "@/hooks/use-tick"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import Link from "next/link"
import { getCrewDotColor } from "@/lib/crew-icon"

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

const runStatusConfig: Record<string, { label: string; variant: "default" | "secondary" | "destructive" | "outline"; icon: React.ElementType }> = {
  PENDING: { label: "Pending", variant: "outline", icon: Clock },
  RUNNING: { label: "Running", variant: "default", icon: Play },
  COMPLETED: { label: "Completed", variant: "secondary", icon: CheckCircle },
  FAILED: { label: "Failed", variant: "destructive", icon: XCircle },
  CANCELLED: { label: "Cancelled", variant: "outline", icon: XCircle },
  TIMEOUT: { label: "Timeout", variant: "destructive", icon: AlertTriangle },
}

const agentStatusConfig: Record<string, { label: string; variant: "default" | "secondary" | "destructive" | "outline" }> = {
  IDLE: { label: "Idle", variant: "outline" },
  RUNNING: { label: "Running", variant: "default" },
  ERROR: { label: "Error", variant: "destructive" },
}

function formatTimeAgo(ts: string): string {
  const seconds = Math.floor((Date.now() - new Date(ts).getTime()) / 1000)
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  return `${days}d ago`
}

export default function DashboardPage() {
  const router = useRouter()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [agents, setAgents] = useState<Agent[]>([])
  const [credentials, setCredentials] = useState<Credential[]>([])
  const [crewCount, setCrewCount] = useState(0)
  const [runsData, setRunsData] = useState<RunsResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [activeFilter, setActiveFilter] = useState("all")
  const [onboardingChecked, setOnboardingChecked] = useState(false)

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
      const [agentsRes, credsRes, crewsRes, runsRes] = await Promise.all([
        fetch(`/api/v1/agents?workspace_id=${workspaceId}`),
        fetch(`/api/v1/credentials?workspace_id=${workspaceId}`),
        fetch(`/api/v1/crews?workspace_id=${workspaceId}`),
        fetch(`/api/v1/runs?workspace_id=${workspaceId}&limit=50`),
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

      setAgents(agentsData)
      setCredentials(credsData)
      setCrewCount(crewsData.length)
      setRunsData(runsResult)
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

  // Real-time: refetch dashboard data when agent/run events arrive
  useRealtimeEvent("run.started", useCallback(() => { fetchData(false) }, [fetchData]))
  useRealtimeEvent("run.completed", useCallback(() => { fetchData(false) }, [fetchData]))
  useRealtimeEvent("run.failed", useCallback(() => { fetchData(false) }, [fetchData]))
  useRealtimeEvent("agent.status", useCallback(() => { fetchData(false) }, [fetchData]))

  const isLoading = wsLoading || loading

  const totalAgents = agents.length
  const runningNow = agents.filter((a) => a.status === "RUNNING").length
  const apiKeysActive = credentials.length

  // Build agent → last run map (keep most recent per agent)
  const agentLastRun = new Map<string, RunEntry>()
  for (const run of runsData?.data ?? []) {
    const existing = agentLastRun.get(run.agent_id)
    if (!existing) {
      agentLastRun.set(run.agent_id, run)
      continue
    }
    const tsExisting = new Date(existing.started_at ?? existing.created_at).getTime()
    const tsNew = new Date(run.started_at ?? run.created_at).getTime()
    if (tsNew > tsExisting) agentLastRun.set(run.agent_id, run)
  }

  // Filter agents
  const filteredAgents =
    activeFilter === "all"
      ? agents
      : agents.filter((a) => a.status === activeFilter.toUpperCase())

  // Sort: RUNNING first, then by last activity (newest first), no activity last
  const sortedAgents = [...filteredAgents].sort((a, b) => {
    // Running agents first
    if (a.status === "RUNNING" && b.status !== "RUNNING") return -1
    if (b.status === "RUNNING" && a.status !== "RUNNING") return 1

    const runA = agentLastRun.get(a.id)
    const runB = agentLastRun.get(b.id)

    // Agents with activity before those without
    if (runA && !runB) return -1
    if (runB && !runA) return 1
    if (!runA && !runB) return 0

    // Both have runs — sort by most recent
    const tsA = new Date(runA!.started_at ?? runA!.created_at).getTime()
    const tsB = new Date(runB!.started_at ?? runB!.created_at).getTime()
    return tsB - tsA
  })

  if (error) {
    return (
      <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
        <PageHeader title="Dashboard" description="Overview of your AI workforce" />
        <p className="text-sm text-destructive">{error}</p>
      </div>
    )
  }

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Dashboard" description="Overview of your AI workforce">
        <Button asChild>
          <Link href="/agents/new">
            <Plus className="mr-2 h-4 w-4" />
            New Agent
          </Link>
        </Button>
      </PageHeader>

      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3 sm:gap-4">
        {isLoading ? (
          <>
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-[104px] rounded-xl" />
            ))}
          </>
        ) : (
          <>
            <StatCard
              title="Total Agents"
              value={totalAgents}
              subtitle={totalAgents === 0 ? "No agents yet" : `${totalAgents} agent${totalAgents === 1 ? "" : "s"}`}
              icon={Bot}
              iconClassName="bg-primary/10 text-primary"
              animatedIcon={<AnimatedBot size={16} />}
            />
            <StatCard
              title="Running Now"
              value={runningNow}
              subtitle={`of ${totalAgents} agents`}
              icon={Activity}
              iconClassName="bg-emerald-500/10 text-emerald-600"
              animatedIcon={<AnimatedActivity size={16} />}
            />
            <StatCard
              title="Today's Runs"
              value={runsData?.stats.today ?? 0}
              subtitle={
                runsData?.stats.failed
                  ? `${runsData.stats.failed} failed`
                  : runsData?.stats.today
                    ? `${runsData.stats.today} run${runsData.stats.today === 1 ? "" : "s"} today`
                    : "No runs today"
              }
              icon={Hourglass}
              iconClassName={runsData?.stats.failed ? "bg-destructive/10 text-destructive" : undefined}
            />
            <StatCard
              title="API Keys Active"
              value={apiKeysActive}
              subtitle={apiKeysActive === 0 ? "Add credentials to get started" : `${apiKeysActive} key${apiKeysActive === 1 ? "" : "s"} configured`}
              icon={Key}
              animatedIcon={<AnimatedKey size={16} />}
            />
          </>
        )}
      </div>

      {!isLoading && (
        <SetupNudge
          crewCount={crewCount}
          agentCount={totalAgents}
          credentialCount={apiKeysActive}
        />
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
                  const statusCfg = agentStatusConfig[agent.status] ?? agentStatusConfig.IDLE
                  const runCfg = lastRun ? (runStatusConfig[lastRun.status] ?? runStatusConfig.PENDING) : null
                  const RunIcon = runCfg?.icon

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
                            <span
                              className="h-2 w-2 rounded-full shrink-0"
                              style={{ backgroundColor: getCrewDotColor(agent.crew.color) }}
                            />
                            <span className="text-body">{agent.crew.name}</span>
                          </div>
                        ) : (
                          <span className="text-body text-muted-foreground">&mdash;</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <Badge variant={statusCfg.variant} className="gap-1.5">
                          {agent.status === "RUNNING" && (
                            <span className="relative flex h-2 w-2">
                              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
                              <span className="relative inline-flex rounded-full h-2 w-2 bg-emerald-500" />
                            </span>
                          )}
                          {statusCfg.label}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        {lastRun ? (
                          <div className="flex items-center gap-1.5">
                            {RunIcon && <RunIcon className="h-3.5 w-3.5 text-muted-foreground" />}
                            <span className="text-body text-muted-foreground">
                              {runCfg!.label} {formatTimeAgo(lastRun.started_at ?? lastRun.created_at)}
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
    </div>
  )
}
