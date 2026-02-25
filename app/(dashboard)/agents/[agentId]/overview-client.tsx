"use client"

import { useParams } from "next/navigation"
import { useState, useEffect } from "react"
import Link from "next/link"
import {
  AlertCircle, Puzzle, KeyRound, MessagesSquare,
  Clock, Brain, Shield, Terminal, ScrollText,
  Zap, FileText, Check, X,
} from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { useAgentDetail } from "@/hooks/use-agent-detail"
import { useWorkspace } from "@/hooks/use-workspace"
import { CLI_ADAPTERS } from "@/lib/cli-adapters"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"

const AGENT_ROLE_COLORS: Record<string, string> = {
  AGENT: "text-blue-600 border-blue-300 bg-blue-50 dark:bg-blue-950/30",
  LEAD: "text-amber-600 border-amber-300 bg-amber-50 dark:bg-amber-950/30",
  COORDINATOR: "text-purple-600 border-purple-300 bg-purple-50 dark:bg-purple-950/30",
}

const STATUS_STYLES: Record<string, { class: string; dot: string; pulse: boolean }> = {
  IDLE: { class: "bg-muted text-muted-foreground", dot: "bg-gray-400", pulse: false },
  RUNNING: { class: "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400", dot: "bg-emerald-500", pulse: true },
  ERROR: { class: "bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-400", dot: "bg-red-500", pulse: false },
  STOPPED: { class: "bg-neutral-100 text-neutral-600 dark:bg-neutral-900 dark:text-neutral-400", dot: "bg-neutral-400", pulse: false },
}

function formatTimeout(seconds: number): string {
  if (seconds >= 3600) return `${Math.round(seconds / 3600)}h`
  return `${Math.round(seconds / 60)} min`
}

function timeAgo(dateStr: string): string {
  const now = Date.now()
  const then = new Date(dateStr).getTime()
  const diff = now - then
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return "just now"
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days === 1) return "yesterday"
  return `${days}d ago`
}

function formatDuration(ms: number): string {
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const remainder = s % 60
  return remainder > 0 ? `${m}m ${remainder}s` : `${m}m`
}

interface RecentChat {
  id: string
  title: string | null
  status: string
  message_count: number
  created_at: string
}

interface RecentRun {
  id: string
  status: string
  started_at: string
  finished_at: string | null
  metadata: Record<string, unknown> | null
  created_at: string
}

export function AgentOverviewPageClient() {
  const { agentId } = useParams<{ agentId: string }>()
  const { agent, loading, error } = useAgentDetail()
  const { workspaceId } = useWorkspace()
  const [recentChats, setRecentChats] = useState<RecentChat[]>([])
  const [recentRuns, setRecentRuns] = useState<RecentRun[]>([])

  useEffect(() => {
    if (!workspaceId || !agentId) return
    let cancelled = false

    async function fetchActivity() {
      try {
        const [chatsRes, runsRes] = await Promise.all([
          fetch(`/api/v1/agents/${agentId}/chats?workspace_id=${workspaceId}`),
          fetch(`/api/v1/agents/${agentId}/runs?workspace_id=${workspaceId}`),
        ])
        if (chatsRes.ok && !cancelled) {
          const chats: RecentChat[] = await chatsRes.json()
          setRecentChats(chats.slice(0, 4))
        }
        if (runsRes.ok && !cancelled) {
          const runs: RecentRun[] = await runsRes.json()
          setRecentRuns(runs.slice(0, 4))
        }
      } catch {
        // non-fatal
      }
    }

    fetchActivity()
    return () => { cancelled = true }
  }, [agentId, workspaceId])

  if (loading) {
    return <OverviewSkeleton />
  }

  if (error || !agent) {
    return (
      <div className="p-4 sm:p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-sm">{error ?? "Agent not found"}</p>
        </div>
      </div>
    )
  }

  const statusStyle = STATUS_STYLES[agent.status] ?? STATUS_STYLES.IDLE
  const adapterCfg = CLI_ADAPTERS[agent.cli_adapter]
  const totalRuns = recentRuns.length > 0 ? recentRuns.length : 0
  const activeChats = recentChats.filter((c) => c.status === "ACTIVE").length

  return (
    <div className="p-4 sm:p-6 space-y-5">
      {/* Hero */}
      <div className="flex items-start gap-4">
        <img
          src={getAgentAvatarUrl(agent.avatar_seed || agent.name, agent.avatar_style || agent.crew?.avatar_style)}
          alt={agent.name}
          className="h-14 w-14 rounded-2xl shrink-0"
        />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2.5 flex-wrap">
            <h1 className="text-xl font-semibold">{agent.name}</h1>
            <Badge variant="secondary" className={`${statusStyle.class} text-xs gap-1.5`}>
              <span className={`h-1.5 w-1.5 rounded-full ${statusStyle.dot} ${statusStyle.pulse ? "animate-pulse" : ""}`} />
              {agent.status}
            </Badge>
            <Badge variant="outline" className={`${AGENT_ROLE_COLORS[agent.agent_role] ?? ""} text-xs`}>
              {agent.agent_role}
            </Badge>
          </div>
          {agent.role_title && (
            <p className="text-sm text-muted-foreground mt-0.5">{agent.role_title}</p>
          )}
          {agent.description && (
            <p className="text-sm text-muted-foreground mt-1.5 leading-relaxed">{agent.description}</p>
          )}
        </div>
      </div>

      {/* Stats Row — 5 columns */}
      <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-3">
        <Link href={`/agents/${agentId}/runs`} className="group">
          <Card className="transition-colors group-hover:border-primary/30">
            <CardContent className="p-4">
              <div className="flex items-center gap-2 text-muted-foreground mb-1.5">
                <Zap className="h-3.5 w-3.5" />
                <span className="text-[10px] uppercase tracking-wider font-semibold">Runs</span>
              </div>
              <div className="text-2xl font-bold">{recentRuns.length}</div>
              <div className="text-[10px] text-muted-foreground mt-0.5">
                {recentRuns.filter((r) => r.status === "COMPLETED").length} completed
              </div>
            </CardContent>
          </Card>
        </Link>
        <Link href={`/agents/${agentId}/chat`} className="group">
          <Card className="transition-colors group-hover:border-primary/30">
            <CardContent className="p-4">
              <div className="flex items-center gap-2 text-muted-foreground mb-1.5">
                <MessagesSquare className="h-3.5 w-3.5" />
                <span className="text-[10px] uppercase tracking-wider font-semibold">Sessions</span>
              </div>
              <div className="text-2xl font-bold">{agent._count?.chats ?? 0}</div>
              <div className="text-[10px] text-muted-foreground mt-0.5">
                {activeChats > 0 ? `${activeChats} active` : "none active"}
              </div>
            </CardContent>
          </Card>
        </Link>
        <Link href={`/agents/${agentId}/skills`} className="group">
          <Card className="transition-colors group-hover:border-primary/30">
            <CardContent className="p-4">
              <div className="flex items-center gap-2 text-muted-foreground mb-1.5">
                <Puzzle className="h-3.5 w-3.5" />
                <span className="text-[10px] uppercase tracking-wider font-semibold">Skills</span>
              </div>
              <div className="text-2xl font-bold">{agent._count?.skills ?? 0}</div>
              <div className="text-[10px] text-muted-foreground mt-0.5">
                {(agent._count?.skills ?? 0) > 0 ? "assigned" : "none assigned"}
              </div>
            </CardContent>
          </Card>
        </Link>
        <Link href={`/agents/${agentId}/credentials`} className="group">
          <Card className="transition-colors group-hover:border-primary/30">
            <CardContent className="p-4">
              <div className="flex items-center gap-2 text-muted-foreground mb-1.5">
                <KeyRound className="h-3.5 w-3.5" />
                <span className="text-[10px] uppercase tracking-wider font-semibold">Credentials</span>
              </div>
              <div className="text-2xl font-bold">{agent._count?.credentials ?? 0}</div>
              <div className="text-[10px] text-muted-foreground mt-0.5">
                {(agent._count?.credentials ?? 0) > 0 ? `${agent._count.credentials} active` : "none"}
              </div>
            </CardContent>
          </Card>
        </Link>
        <Link href={`/agents/${agentId}/files`} className="group col-span-2 sm:col-span-1">
          <Card className="transition-colors group-hover:border-primary/30">
            <CardContent className="p-4">
              <div className="flex items-center gap-2 text-muted-foreground mb-1.5">
                <FileText className="h-3.5 w-3.5" />
                <span className="text-[10px] uppercase tracking-wider font-semibold">Files</span>
              </div>
              <div className="text-2xl font-bold">&mdash;</div>
              <div className="text-[10px] text-muted-foreground mt-0.5">container off</div>
            </CardContent>
          </Card>
        </Link>
      </div>

      {/* Config + Activity Grid: 1 col config, 2 cols activity */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
        {/* LEFT: Config cards stacked */}
        <div className="space-y-4">
          {/* Runtime */}
          <Card>
            <CardContent className="p-4 space-y-3">
              <div className="flex items-center gap-2 mb-1">
                <Terminal className="h-4 w-4 text-muted-foreground" />
                <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Runtime</span>
              </div>
              <div className="space-y-2.5 text-sm">
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Adapter</span>
                  <span className="flex items-center gap-1.5 font-medium">
                    {adapterCfg ? (
                      <>
                        <adapterCfg.icon className="h-4 w-4" />
                        {adapterCfg.label}
                      </>
                    ) : (
                      agent.cli_adapter
                    )}
                  </span>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Model</span>
                  <code className="text-xs bg-muted px-2 py-0.5 rounded">
                    {agent.llm_model || adapterCfg?.defaultModel || "default"}
                  </code>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Tools</span>
                  <Badge variant="secondary" className="text-xs">{agent.tool_profile}</Badge>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Timeout</span>
                  <span className="flex items-center gap-1.5 text-muted-foreground">
                    <Clock className="h-3.5 w-3.5" />
                    {formatTimeout(agent.timeout_seconds)}
                  </span>
                </div>
              </div>
            </CardContent>
          </Card>

          {/* Identity */}
          <Card>
            <CardContent className="p-4 space-y-3">
              <div className="flex items-center gap-2 mb-1">
                <Shield className="h-4 w-4 text-muted-foreground" />
                <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Identity</span>
              </div>
              <div className="space-y-2.5 text-sm">
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Slug</span>
                  <code className="text-xs bg-muted px-2 py-0.5 rounded">{agent.slug}</code>
                </div>
                {agent.crew && (
                  <div className="flex items-center justify-between">
                    <span className="text-muted-foreground">Crew</span>
                    <Link href={`/crews/${agent.crew_id}`} className="flex items-center gap-1.5 hover:underline">
                      <span
                        className="h-2 w-2 rounded-full"
                        style={{ backgroundColor: agent.crew.color ?? "#6b7280" }}
                      />
                      {agent.crew.name}
                    </Link>
                  </div>
                )}
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Memory</span>
                  <span className="flex items-center gap-1.5">
                    <Brain className="h-3.5 w-3.5 text-muted-foreground" />
                    <Badge
                      variant="secondary"
                      className={`text-xs ${agent.memory_enabled ? "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400" : ""}`}
                    >
                      {agent.memory_enabled ? "On" : "Off"}
                    </Badge>
                  </span>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Created</span>
                  <span className="text-xs text-muted-foreground">
                    {new Date(agent.created_at).toLocaleDateString()}
                  </span>
                </div>
              </div>
            </CardContent>
          </Card>

          {/* System Prompt */}
          {agent.system_prompt && (
            <Card>
              <CardContent className="p-4 space-y-3">
                <div className="flex items-center gap-2 mb-1">
                  <ScrollText className="h-4 w-4 text-muted-foreground" />
                  <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">System Prompt</span>
                </div>
                <div className="bg-muted/50 rounded-lg p-3 font-mono text-xs leading-relaxed max-h-64 overflow-y-auto whitespace-pre-wrap">
                  {agent.system_prompt}
                </div>
              </CardContent>
            </Card>
          )}
        </div>

        {/* RIGHT: Recent Activity */}
        <div className="lg:col-span-2 space-y-4">
          {/* Recent Sessions */}
          <Card>
            <CardContent className="p-0">
              <div className="flex items-center justify-between px-4 py-3 border-b">
                <div className="flex items-center gap-2">
                  <MessagesSquare className="h-4 w-4 text-muted-foreground" />
                  <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Recent Sessions</span>
                </div>
                <Link href={`/agents/${agentId}/chat`} className="text-[10px] text-primary font-medium hover:underline">
                  View all
                </Link>
              </div>
              {recentChats.length === 0 ? (
                <div className="px-4 py-6 text-center text-sm text-muted-foreground">
                  No sessions yet. Start a chat to begin.
                </div>
              ) : (
                <div className="divide-y">
                  {recentChats.map((chat) => (
                    <Link
                      key={chat.id}
                      href={`/agents/${agentId}/chat?session=${chat.id}`}
                      className="flex items-center justify-between px-4 py-2.5 hover:bg-muted/50 transition-colors"
                    >
                      <div className="min-w-0">
                        <div className="text-sm font-medium truncate">
                          {chat.title || "Untitled session"}
                        </div>
                        <div className="text-[11px] text-muted-foreground">
                          {chat.message_count} messages &middot; {timeAgo(chat.created_at)}
                        </div>
                      </div>
                      {chat.status === "ACTIVE" ? (
                        <Badge variant="secondary" className="text-[10px] bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400 gap-1 shrink-0">
                          <span className="h-1 w-1 rounded-full bg-emerald-500" />
                          active
                        </Badge>
                      ) : (
                        <Badge variant="secondary" className="text-[10px] shrink-0">done</Badge>
                      )}
                    </Link>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>

          {/* Recent Runs */}
          <Card>
            <CardContent className="p-0">
              <div className="flex items-center justify-between px-4 py-3 border-b">
                <div className="flex items-center gap-2">
                  <Zap className="h-4 w-4 text-muted-foreground" />
                  <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Recent Runs</span>
                </div>
                <Link href={`/agents/${agentId}/runs`} className="text-[10px] text-primary font-medium hover:underline">
                  View all
                </Link>
              </div>
              {recentRuns.length === 0 ? (
                <div className="px-4 py-6 text-center text-sm text-muted-foreground">
                  No runs yet.
                </div>
              ) : (
                <div className="divide-y">
                  {recentRuns.map((run, idx) => {
                    const isSuccess = run.status === "COMPLETED"
                    const isError = run.status === "FAILED" || run.status === "ERROR"
                    const cost = run.metadata?.total_cost_usd as number | undefined
                    const durationMs = run.metadata?.duration_api_ms as number | undefined
                    return (
                      <div
                        key={run.id}
                        className="flex items-center justify-between px-4 py-2.5 hover:bg-muted/50 transition-colors"
                      >
                        <div className="flex items-center gap-3 min-w-0">
                          <span className={`inline-flex h-6 w-6 items-center justify-center rounded-full shrink-0 ${
                            isError
                              ? "bg-red-50 text-red-600 dark:bg-red-950/30 dark:text-red-400"
                              : isSuccess
                                ? "bg-emerald-50 text-emerald-600 dark:bg-emerald-950/30 dark:text-emerald-400"
                                : "bg-muted text-muted-foreground"
                          }`}>
                            {isError ? <X className="h-3 w-3" /> : <Check className="h-3 w-3" />}
                          </span>
                          <div className="min-w-0">
                            <div className="text-sm truncate">Run #{recentRuns.length - idx}</div>
                            <div className="text-[11px] text-muted-foreground">
                              {timeAgo(run.started_at || run.created_at)}
                              {durationMs ? ` \u00B7 ${formatDuration(durationMs)}` : ""}
                              {cost ? ` \u00B7 $${cost.toFixed(2)}` : ""}
                            </div>
                          </div>
                        </div>
                        <span className={`text-[10px] font-medium shrink-0 ${
                          isError ? "text-red-600" : isSuccess ? "text-emerald-600" : "text-muted-foreground"
                        }`}>
                          {run.status.toLowerCase()}
                        </span>
                      </div>
                    )
                  })}
                </div>
              )}
            </CardContent>
          </Card>
        </div>
      </div>


    </div>
  )
}

function OverviewSkeleton() {
  return (
    <div className="p-4 sm:p-6 space-y-5">
      <div className="flex items-start gap-4">
        <Skeleton className="h-14 w-14 rounded-2xl" />
        <div className="space-y-2 flex-1">
          <Skeleton className="h-6 w-48" />
          <Skeleton className="h-4 w-32" />
        </div>
      </div>
      <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-3">
        {Array.from({ length: 5 }).map((_, i) => (
          <Skeleton key={i} className="h-24 rounded-xl" />
        ))}
      </div>
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
        <div className="space-y-4">
          <Skeleton className="h-48 rounded-xl" />
          <Skeleton className="h-40 rounded-xl" />
        </div>
        <div className="lg:col-span-2 space-y-4">
          <Skeleton className="h-44 rounded-xl" />
          <Skeleton className="h-44 rounded-xl" />
        </div>
      </div>
    </div>
  )
}
