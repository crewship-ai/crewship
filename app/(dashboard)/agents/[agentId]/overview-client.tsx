"use client"

import { useParams } from "next/navigation"

import { use, useState, useEffect, useCallback } from "react"
import Link from "next/link"
import { Bot, MessageSquare, ScrollText, Settings, Pause, AlertCircle, Puzzle, KeyRound, MessagesSquare, Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"

interface AgentCrew {
  name: string
  slug: string
  color: string | null
}

interface AgentDetail {
  id: string
  workspace_id: string
  crew_id: string | null
  name: string
  slug: string
  description: string | null
  role_title: string | null
  agent_role: string
  status: string
  cli_adapter: string
  llm_provider: string | null
  llm_model: string | null
  system_prompt: string | null
  temperature: number | null
  max_tokens: number | null
  timeout_seconds: number
  tool_profile: string
  memory_enabled: boolean
  created_at: string
  updated_at: string
  crew: AgentCrew | null
  _count: {
    skills: number
    credentials: number
    chats: number
  }
}

const CLI_ADAPTER_LABELS: Record<string, string> = {
  CLAUDE_CODE: "Claude Code",
  OPENCODE: "OpenCode",
  CODEX_CLI: "Codex CLI",
  GEMINI_CLI: "Gemini CLI",
}

const AGENT_ROLE_COLORS: Record<string, string> = {
  AGENT: "text-blue-600 border-blue-300",
  LEAD: "text-amber-600 border-amber-300",
  COORDINATOR: "text-purple-600 border-purple-300",
}

const STATUS_STYLES: Record<string, { class: string; pulse: boolean }> = {
  IDLE: { class: "bg-muted text-muted-foreground", pulse: false },
  RUNNING: { class: "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400", pulse: true },
  ERROR: { class: "bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-400", pulse: false },
  STOPPED: { class: "bg-neutral-100 text-neutral-600 dark:bg-neutral-900 dark:text-neutral-400", pulse: false },
}

function formatTimeout(seconds: number): string {
  if (seconds >= 3600) return `${Math.round(seconds / 3600)}h`
  return `${Math.round(seconds / 60)} min`
}

export function AgentOverviewPageClient() {
  const { agentId } = useParams<{ agentId: string }>()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [agent, setAgent] = useState<AgentDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [stopping, setStopping] = useState(false)

  useEffect(() => {
    if (!workspaceId) return

    let cancelled = false

    async function fetchAgent() {
      try {
        const res = await fetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`)
        if (!res.ok) {
          const data = await res.json().catch(() => ({ error: "Failed to load agent" }))
          if (!cancelled) setError(typeof data.error === "string" ? data.error : "Failed to load agent")
          return
        }
        const data: AgentDetail = await res.json()
        if (!cancelled) setAgent(data)
      } catch {
        if (!cancelled) setError("Network error. Please try again.")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchAgent()
    return () => { cancelled = true }
  }, [agentId, workspaceId])

  const handleStop = useCallback(async () => {
    if (!workspaceId || !agent || stopping) return
    setStopping(true)
    try {
      const res = await fetch(`/api/v1/agents/${agentId}/stop?workspace_id=${workspaceId}`, { method: "POST" })
      if (res.ok) {
        const data = await res.json()
        setAgent((prev) => prev ? { ...prev, status: data.status } : prev)
      }
    } catch {
      // silently fail
    } finally {
      setStopping(false)
    }
  }, [agentId, workspaceId, agent, stopping])

  if (wsLoading || loading) {
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

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      {/* Quick Actions */}
      <div className="flex flex-wrap items-center gap-2 sm:gap-3">
        <Button
          variant="outline"
          size="sm"
          className="text-destructive border-destructive/30 hover:bg-destructive/10 gap-2"
          onClick={handleStop}
          disabled={stopping || agent.status === "STOPPED"}
        >
          {stopping ? <Loader2 className="h-4 w-4 animate-spin" /> : <Pause className="h-4 w-4" />}
          {stopping ? "Stopping..." : "Stop Agent"}
        </Button>
        <Button size="sm" className="gap-2" asChild>
          <Link href={`/agents/${agentId}/chat`}>
            <MessageSquare className="h-4 w-4" />
            Open Chat
          </Link>
        </Button>
        <Button variant="outline" size="sm" className="gap-2" asChild>
          <Link href={`/agents/${agentId}/logs`}>
            <ScrollText className="h-4 w-4" />
            View Logs
          </Link>
        </Button>
        <Button variant="outline" size="sm" className="gap-2" asChild>
          <Link href={`/agents/${agentId}/settings`}>
            <Settings className="h-4 w-4" />
            Edit Settings
          </Link>
        </Button>
      </div>

      {/* Identity Card */}
      <Card>
        <CardContent className="p-4 sm:p-6">
          <div className="flex items-center gap-4 mb-4">
            <div className="flex h-14 w-14 items-center justify-center rounded-xl bg-emerald-50 dark:bg-emerald-950/30">
              <Bot className="h-7 w-7 text-emerald-700 dark:text-emerald-400" />
            </div>
            <div>
              <div className="flex items-center gap-2 flex-wrap">
                <h2 className="text-lg font-semibold">{agent.name}</h2>
                <Badge variant="secondary" className={`${statusStyle.class} text-xs gap-1`}>
                  {statusStyle.pulse && <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />}
                  {agent.status}
                </Badge>
              </div>
              {agent.role_title && (
                <p className="text-sm text-muted-foreground">{agent.role_title}</p>
              )}
            </div>
          </div>

          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
            {/* Identity details */}
            <div className="space-y-3 text-sm">
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">Slug</span>
                <code className="text-xs bg-muted px-2 py-0.5 rounded">{agent.slug}</code>
              </div>
              {agent.crew && (
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Crew</span>
                  <span className="flex items-center gap-1.5">
                    <span
                      className="h-2 w-2 rounded-full"
                      style={{ backgroundColor: agent.crew.color ?? "#6b7280" }}
                    />
                    {agent.crew.name}
                  </span>
                </div>
              )}
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">Role</span>
                <Badge variant="outline" className={`${AGENT_ROLE_COLORS[agent.agent_role] ?? ""} text-xs`}>
                  {agent.agent_role}
                </Badge>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">Memory</span>
                <Badge variant="secondary" className={`text-xs ${agent.memory_enabled ? "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400" : ""}`}>
                  {agent.memory_enabled ? "Enabled" : "Disabled"}
                </Badge>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">Timeout</span>
                <span>{formatTimeout(agent.timeout_seconds)}</span>
              </div>
            </div>

            {/* LLM Config */}
            <div className="space-y-3 text-sm">
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">CLI Adapter</span>
                <span className="font-medium">{CLI_ADAPTER_LABELS[agent.cli_adapter] ?? agent.cli_adapter}</span>
              </div>
              {agent.llm_provider && (
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Provider</span>
                  <span>{agent.llm_provider}</span>
                </div>
              )}
              {agent.llm_model && (
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Model</span>
                  <code className="text-xs">{agent.llm_model}</code>
                </div>
              )}
              {agent.temperature !== null && (
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Temperature</span>
                  <span className="font-mono text-xs">{agent.temperature}</span>
                </div>
              )}
              {agent.max_tokens !== null && (
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Max Tokens</span>
                  <span className="font-mono text-xs">{agent.max_tokens.toLocaleString()}</span>
                </div>
              )}
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">Tool Profile</span>
                <Badge variant="secondary" className="text-xs">{agent.tool_profile}</Badge>
              </div>
            </div>
          </div>

          {/* Description */}
          {agent.description && (
            <p className="mt-4 text-sm text-muted-foreground leading-relaxed border-t pt-4">
              {agent.description}
            </p>
          )}

          {/* System Prompt */}
          {agent.system_prompt && (
            <div className="mt-4 border-t pt-4">
              <p className="text-xs text-muted-foreground uppercase tracking-wide font-medium mb-2">System Prompt</p>
              <div className="bg-muted/50 rounded-lg p-3 font-mono text-xs leading-relaxed max-h-32 overflow-y-auto whitespace-pre-wrap">
                {agent.system_prompt}
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      {/* Stats Row */}
      <div className="grid grid-cols-2 sm:grid-cols-3 gap-3 sm:gap-4">
        <Card>
          <CardContent className="p-4">
            <div className="flex items-center gap-2 text-muted-foreground mb-1">
              <Puzzle className="h-3.5 w-3.5" />
              <span className="text-xs uppercase tracking-wide font-medium">Skills</span>
            </div>
            <div className="text-2xl font-bold">{agent._count.skills}</div>
            <Link href={`/agents/${agentId}/skills`} className="text-xs text-primary hover:underline">
              View skills
            </Link>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="p-4">
            <div className="flex items-center gap-2 text-muted-foreground mb-1">
              <KeyRound className="h-3.5 w-3.5" />
              <span className="text-xs uppercase tracking-wide font-medium">Credentials</span>
            </div>
            <div className="text-2xl font-bold">{agent._count.credentials}</div>
            <Link href={`/agents/${agentId}/credentials`} className="text-xs text-primary hover:underline">
              View credentials
            </Link>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="p-4">
            <div className="flex items-center gap-2 text-muted-foreground mb-1">
              <MessagesSquare className="h-3.5 w-3.5" />
              <span className="text-xs uppercase tracking-wide font-medium">Chats</span>
            </div>
            <div className="text-2xl font-bold">{agent._count.chats}</div>
            <Link href={`/agents/${agentId}/chats`} className="text-xs text-primary hover:underline">
              View chats
            </Link>
          </CardContent>
        </Card>
      </div>
    </div>
  )
}

function OverviewSkeleton() {
  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <div className="flex gap-2">
        <Skeleton className="h-8 w-28" />
        <Skeleton className="h-8 w-28" />
        <Skeleton className="h-8 w-24" />
      </div>
      <Card>
        <CardContent className="p-4 sm:p-6 space-y-4">
          <div className="flex items-center gap-4">
            <Skeleton className="h-14 w-14 rounded-xl" />
            <div className="space-y-2">
              <Skeleton className="h-5 w-48" />
              <Skeleton className="h-4 w-32" />
            </div>
          </div>
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
            <div className="space-y-3">
              {Array.from({ length: 5 }).map((_, i) => (
                <Skeleton key={i} className="h-5 w-full" />
              ))}
            </div>
            <div className="space-y-3">
              {Array.from({ length: 5 }).map((_, i) => (
                <Skeleton key={i} className="h-5 w-full" />
              ))}
            </div>
          </div>
        </CardContent>
      </Card>
      <div className="grid grid-cols-2 sm:grid-cols-3 gap-3 sm:gap-4">
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-24 rounded-lg" />
        ))}
      </div>
    </div>
  )
}
