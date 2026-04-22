"use client"

import { useParams } from "next/navigation"
import { useState, useEffect, useCallback } from "react"
import Link from "next/link"
import {
  AlertCircle, Puzzle, KeyRound, MessagesSquare,
  Clock, Brain, Shield, Terminal, ScrollText,
  Zap, FileText, Check, X,
} from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { StatusBadge, StatusDot } from "@/components/ui/status-badge"
import { Skeleton } from "@/components/ui/skeleton"
import { AnimatedNumber } from "@/components/ui/animated-number"
import { FlashHighlight } from "@/components/ui/flash-highlight"
import { useAgentDetail } from "@/hooks/use-agent-detail"
import { useWorkspace } from "@/hooks/use-workspace"
import { getCrewBgClass } from "@/lib/colors"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { CLI_ADAPTERS } from "@/lib/cli-adapters"
import { HistorySection } from "@/components/features/agents/overview/history-section"
import { EmptyState } from "@/components/layout/empty-state"
import { timeAgo, formatDuration, formatTimeout } from "@/lib/time"


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

function StatMiniCard({
  href,
  icon: Icon,
  label,
  value,
  subtitle,
  disabled,
}: {
  href: string
  icon: React.ElementType
  label: string
  value: number | string
  subtitle: string
  disabled?: boolean
}) {
  const content = (
    <FlashHighlight trigger={value}>
      <Card className={disabled ? "" : "transition-all duration-150 group-hover:border-primary/30 group-hover:bg-accent/30"}>
        <CardContent className="p-4">
          <div className="flex items-center gap-2 text-muted-foreground mb-1.5">
            <Icon className="h-3.5 w-3.5" />
            <span className="text-micro uppercase tracking-wider font-semibold">{label}</span>
          </div>
          <div className="text-title font-bold">
            {typeof value === "number" ? <AnimatedNumber value={value} /> : value}
          </div>
          <div className="text-micro text-muted-foreground mt-0.5">{subtitle}</div>
        </CardContent>
      </Card>
    </FlashHighlight>
  )

  if (disabled) {
    return <div className="opacity-60">{content}</div>
  }

  return (
    <Link
      href={href}
      className="group rounded-[var(--radius)] focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-2 outline-none"
    >
      {content}
    </Link>
  )
}

export function AgentOverviewPageClient() {
  const { agentId } = useParams<{ agentId: string }>()
  const { agent, loading, error, refresh } = useAgentDetail()
  const { workspaceId } = useWorkspace()
  const [recentChats, setRecentChats] = useState<RecentChat[]>([])
  const [recentRuns, setRecentRuns] = useState<RecentRun[]>([])
  const [totalRunCount, setTotalRunCount] = useState(0)
  const [totalCompletedRunCount, setTotalCompletedRunCount] = useState(0)

  const fetchActivity = useCallback(async (silent = false) => {
    if (!workspaceId || !agentId) return
    if (!silent) {
      setRecentChats([])
      setRecentRuns([])
      setTotalRunCount(0)
      setTotalCompletedRunCount(0)
    }
    try {
      const [chatsRes, runsRes] = await Promise.all([
        fetch(`/api/v1/agents/${agentId}/chats?workspace_id=${workspaceId}`),
        fetch(`/api/v1/agents/${agentId}/runs?workspace_id=${workspaceId}`),
      ])
      if (chatsRes.ok) {
        const chats: RecentChat[] = await chatsRes.json()
        setRecentChats(chats.slice(0, 4))
      }
      if (runsRes.ok) {
        const runs: RecentRun[] = await runsRes.json()
        setTotalRunCount(runs.length)
        setTotalCompletedRunCount(runs.filter((r) => r.status === "COMPLETED").length)
        setRecentRuns(runs.slice(0, 4))
      }
    } catch {
      // non-fatal
    }
  }, [agentId, workspaceId])

  useEffect(() => { fetchActivity() }, [fetchActivity])

  // Real-time: refresh agent status + runs on workspace events
  useRealtimeEvent("agent.status", useCallback(() => { refresh(); fetchActivity(true) }, [refresh, fetchActivity]))
  useRealtimeEvent("run.started", useCallback(() => { fetchActivity(true) }, [fetchActivity]))
  useRealtimeEvent("run.completed", useCallback(() => { fetchActivity(true) }, [fetchActivity]))
  useRealtimeEvent("run.failed", useCallback(() => { fetchActivity(true) }, [fetchActivity]))

  if (loading) {
    return <OverviewSkeleton />
  }

  if (error || !agent) {
    return (
      <div className="p-6">
        <EmptyState
          icon={AlertCircle}
          title={error ? "Failed to load agent" : "Agent not found"}
          description={
            error
              ? "Refresh the page or check that the workspace is running."
              : "This agent may have been renamed or deleted."
          }
        >
          <div className="mt-4">
            <Link href="/fleet" className="text-micro font-medium text-primary hover:underline">
              Back to Fleet
            </Link>
          </div>
        </EmptyState>
      </div>
    )
  }

  const adapterCfg = CLI_ADAPTERS[agent.cli_adapter]
  const totalRuns = totalRunCount
  const activeChats = recentChats.filter((c) => c.status === "ACTIVE").length

  return (
    <div className="p-4 sm:p-6 space-y-6">
      <h2 className="text-title font-semibold">Overview</h2>
      {/* Stats Row — 5 columns */}
      <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 gap-3">
        <StatMiniCard
          href={`/fleet/agents/${agentId}/runs`}
          icon={Zap}
          label="Runs"
          value={totalRuns}
          subtitle={`${totalCompletedRunCount} completed`}
        />
        <StatMiniCard
          href={`/fleet/agents/${agentId}/sessions`}
          icon={MessagesSquare}
          label="Sessions"
          value={agent._count?.chats ?? 0}
          subtitle={activeChats > 0 ? `${activeChats} active` : "none active"}
        />
        <StatMiniCard
          href={`/fleet/agents/${agentId}/tools?section=skills`}
          icon={Puzzle}
          label="Skills"
          value={agent._count?.skills ?? 0}
          subtitle={(agent._count?.skills ?? 0) > 0 ? "assigned" : "none assigned"}
        />
        <StatMiniCard
          href={`/fleet/agents/${agentId}/tools?section=credentials`}
          icon={KeyRound}
          label="Credentials"
          value={agent._count?.credentials ?? 0}
          subtitle={(agent._count?.credentials ?? 0) > 0 ? `${agent._count.credentials} active` : "none"}
        />
        <StatMiniCard
          href={`/fleet/agents/${agentId}/workspace?pane=files`}
          icon={FileText}
          label="Files"
          value="\u2014"
          subtitle="container off"
          disabled
        />
      </div>

      {/* Config + Activity Grid: 1 col config, 2 cols activity */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        {/* LEFT: Config card (merged Runtime + Identity) + System Prompt */}
        <div className="space-y-4">
          <Card>
            <CardContent className="p-4 space-y-3">
              {/* Runtime section */}
              <div className="flex items-center gap-2 mb-1">
                <Terminal className="h-4 w-4 text-muted-foreground" />
                <span className="text-micro font-semibold uppercase tracking-wider text-muted-foreground">Runtime</span>
              </div>
              <div className="space-y-2.5 text-body">
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
                  <code className="text-label bg-muted px-2 py-0.5 rounded">
                    {agent.llm_model || adapterCfg?.defaultModel || "default"}
                  </code>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Tools</span>
                  <Badge variant="secondary" className="text-micro">{agent.tool_profile}</Badge>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Timeout</span>
                  <span className="flex items-center gap-1.5 text-muted-foreground">
                    <Clock className="h-3.5 w-3.5" />
                    {formatTimeout(agent.timeout_seconds)}
                  </span>
                </div>
              </div>

              {/* Divider */}
              <div className="border-t" />

              {/* Identity section */}
              <div className="flex items-center gap-2 mb-1">
                <Shield className="h-4 w-4 text-muted-foreground" />
                <span className="text-micro font-semibold uppercase tracking-wider text-muted-foreground">Identity</span>
              </div>
              <div className="space-y-2.5 text-body">
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Slug</span>
                  <code className="text-label bg-muted px-2 py-0.5 rounded">{agent.slug}</code>
                </div>
                {agent.crew && (
                  <div className="flex items-center justify-between">
                    <span className="text-muted-foreground">Crew</span>
                    <Link href={`/fleet/crews/${agent.crew_id}`} className="flex items-center gap-1.5 hover:underline" aria-label={`Go to ${agent.crew.name} crew`}>
                      <span className={`h-2 w-2 rounded-full ${getCrewBgClass(agent.crew.color)}`} />
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
                      className={`text-micro ${agent.memory_enabled ? "status-success" : ""}`}
                    >
                      {agent.memory_enabled ? "On" : "Off"}
                    </Badge>
                  </span>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Created</span>
                  <span className="text-label text-muted-foreground">
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
                  <span className="text-micro font-semibold uppercase tracking-wider text-muted-foreground">System Prompt</span>
                </div>
                <div className="bg-surface-subtle border border-border/60 rounded-lg p-3 font-mono text-label leading-relaxed max-h-64 overflow-y-auto whitespace-pre-wrap">
                  {agent.system_prompt}
                </div>
              </CardContent>
            </Card>
          )}
        </div>

        {/* RIGHT: Recent Activity */}
        <div className="md:col-span-1 lg:col-span-2 space-y-4">
          {/* Recent Sessions */}
          <Card>
            <CardContent className="p-0">
              <div className="flex items-center justify-between px-4 py-3 border-b">
                <div className="flex items-center gap-2">
                  <MessagesSquare className="h-4 w-4 text-muted-foreground" />
                  <span className="text-micro font-semibold uppercase tracking-wider text-muted-foreground">Recent Sessions</span>
                </div>
                <Link href={`/fleet/agents/${agentId}/chat`} className="text-micro text-primary font-medium hover:underline">
                  View all
                </Link>
              </div>
              {recentChats.length === 0 ? (
                <div className="px-4 py-6 text-center text-body text-muted-foreground">
                  No sessions yet. Start a chat to begin.
                </div>
              ) : (
                <div className="divide-y">
                  {recentChats.map((chat) => (
                    <Link
                      key={chat.id}
                      href={`/fleet/agents/${agentId}/chat?session=${chat.id}`}
                      className="flex items-center justify-between px-4 py-2.5 hover:bg-muted/50 transition-colors"
                    >
                      <div className="min-w-0">
                        <div className="text-body font-medium truncate">
                          {chat.title || "Untitled session"}
                        </div>
                        <div className="text-micro text-muted-foreground">
                          {chat.message_count} messages &middot; {timeAgo(chat.created_at)}
                        </div>
                      </div>
                      {chat.status === "ACTIVE" ? (
                        <StatusBadge status="IN_PROGRESS" withDot label="active" className="text-micro shrink-0" />
                      ) : (
                        <Badge variant="secondary" className="text-micro shrink-0">done</Badge>
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
                  <span className="text-micro font-semibold uppercase tracking-wider text-muted-foreground">Recent Runs</span>
                </div>
                <Link href={`/fleet/agents/${agentId}/runs`} className="text-micro text-primary font-medium hover:underline">
                  View all
                </Link>
              </div>
              {recentRuns.length === 0 ? (
                <div className="px-4 py-6 text-center text-body text-muted-foreground">
                  No runs yet.
                </div>
              ) : (
                <div className="divide-y">
                  {recentRuns.map((run) => {
                    const isSuccess = run.status === "COMPLETED"
                    const isError = run.status === "FAILED" || run.status === "ERROR"
                    const rawCost = run.metadata?.total_cost_usd
                    const cost = typeof rawCost === "number" && isFinite(rawCost) ? rawCost : undefined
                    const rawDur = run.metadata?.duration_api_ms
                    const durationMs = typeof rawDur === "number" && isFinite(rawDur) ? rawDur : undefined
                    const runTs = run.started_at || run.created_at
                    const canonicalStatus = isError ? "FAILED" : isSuccess ? "COMPLETED" : "PENDING"
                    return (
                      <div
                        key={run.id}
                        className="flex items-center justify-between px-4 py-2.5"
                      >
                        <div className="flex items-center gap-3 min-w-0">
                          <span className={`inline-flex h-6 w-6 items-center justify-center rounded-full shrink-0 ${
                            isError
                              ? "status-error"
                              : isSuccess
                                ? "status-success"
                                : "bg-muted text-muted-foreground"
                          }`}>
                            {isError ? <X className="h-3 w-3" /> : <Check className="h-3 w-3" />}
                          </span>
                          <div className="min-w-0">
                            <div className="text-body truncate">
                              {timeAgo(runTs)}
                              {durationMs ? ` \u00B7 ${formatDuration(durationMs)}` : ""}
                            </div>
                            <div className="text-micro text-muted-foreground">
                              {cost ? `$${cost.toFixed(2)}` : run.id.slice(0, 8)}
                            </div>
                          </div>
                        </div>
                        <span className="text-micro font-medium shrink-0 inline-flex items-center gap-1.5 text-muted-foreground">
                          <StatusDot status={canonicalStatus} className="h-1.5 w-1.5" />
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

      <details className="mt-4 group">
        <summary className="cursor-pointer select-none text-label font-medium text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5">
          <span className="transition-transform group-open:rotate-90">▸</span>
          Recent changes
        </summary>
        <div className="mt-3">
          <HistorySection />
        </div>
      </details>

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
      <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 gap-3">
        {Array.from({ length: 5 }).map((_, i) => (
          <Skeleton key={i} className="h-24 rounded-[var(--radius)]" />
        ))}
      </div>
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        <div className="space-y-4">
          <Skeleton className="h-80 rounded-[var(--radius)]" />
        </div>
        <div className="md:col-span-1 lg:col-span-2 space-y-4">
          <Skeleton className="h-44 rounded-[var(--radius)]" />
          <Skeleton className="h-44 rounded-[var(--radius)]" />
        </div>
      </div>
    </div>
  )
}
