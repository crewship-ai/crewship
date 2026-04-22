"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import {
  MessageSquare, ScrollText, Settings, ExternalLink,
  Zap, Puzzle, KeyRound, Clock, Cpu, Crown, Bot,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { StatusBadge, StatusDot } from "@/components/ui/status-badge"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { timeAgo, formatDuration } from "@/lib/time"
import { cn } from "@/lib/utils"

interface AgentData {
  id: string
  name: string
  slug: string
  status: string
  description: string | null
  role_title: string | null
  agent_role: string
  llm_provider: string
  llm_model: string
  cli_adapter: string
  crew_id: string | null
  avatar_seed?: string | null
  avatar_style?: string | null
  crew?: { name: string; slug: string; color: string | null; avatar_style?: string | null } | null
  _count?: { skills: number; credentials: number; chats: number }
  last_active_at?: string | null
}

interface SessionRow {
  id: string
  title: string | null
  status: string
  started_at: string | null
  created_at: string
  message_count?: number
}

interface RunRow {
  id: string
  status: string
  trigger_type?: string
  created_at: string
  started_at: string | null
  ended_at: string | null
  duration_ms?: number | null
}

function mapAgentStatus(status: string | undefined): string {
  switch (status) {
    case "RUNNING": return "IN_PROGRESS"
    case "ERROR": return "FAILED"
    case "STOPPED": return "CANCELLED"
    default: return "PENDING"
  }
}

const RUN_STATUS_COLOR: Record<string, string> = {
  COMPLETED: "text-emerald-400",
  completed: "text-emerald-400",
  FAILED: "text-red-400",
  failed: "text-red-400",
  RUNNING: "text-blue-400",
  running: "text-blue-400",
  STOPPED: "text-amber-400",
  stopped: "text-amber-400",
}

export interface FleetAgentInlineProps {
  agent: AgentData
  workspaceId: string
}

/**
 * Middle-pane agent detail, shown when an agent is selected in /fleet.
 * Condensed analogue of the agent Overview full page — hero, quick actions,
 * 5-card stats strip, runtime card, recent sessions + recent runs.
 * No tabs: for deep work the user clicks "Open full →".
 */
export function FleetAgentInline({ agent, workspaceId }: FleetAgentInlineProps) {
  const [sessions, setSessions] = useState<SessionRow[]>([])
  const [runs, setRuns] = useState<RunRow[]>([])

  useEffect(() => {
    if (!workspaceId) return
    const controller = new AbortController()
    Promise.all([
      fetch(`/api/v1/agents/${agent.id}/chats?workspace_id=${workspaceId}&limit=3`, { signal: controller.signal })
        .then((r) => r.ok ? r.json() : [])
        .catch(() => []),
      fetch(`/api/v1/agents/${agent.id}/runs?workspace_id=${workspaceId}&limit=3`, { signal: controller.signal })
        .then((r) => r.ok ? r.json() : [])
        .catch(() => []),
    ]).then(([s, r]: [SessionRow[], RunRow[]]) => {
      setSessions(Array.isArray(s) ? s : [])
      setRuns(Array.isArray(r) ? r : [])
    })
    return () => controller.abort()
  }, [agent.id, workspaceId])

  const agentPath = `/fleet/agents/${agent.id}`
  const canonicalStatus = mapAgentStatus(agent.status)
  const isLive = agent.status === "RUNNING"
  const avatarUrl = getAgentAvatarUrl(
    agent.avatar_seed || agent.name,
    agent.avatar_style || agent.crew?.avatar_style,
  )
  const skillCount = agent._count?.skills ?? 0
  const credCount = agent._count?.credentials ?? 0
  const sessionCount = agent._count?.chats ?? 0

  return (
    <div className="h-full overflow-y-auto">
      <div className="p-6 max-w-5xl space-y-5">
        {/* Hero */}
        <div className="flex items-start gap-4">
          <img
            src={avatarUrl}
            alt=""
            className="h-16 w-16 rounded-2xl shrink-0 border border-border"
          />
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-3 flex-wrap">
              <h1 className="text-title font-semibold truncate">{agent.name}</h1>
              <StatusBadge status={canonicalStatus} label={agent.status.toLowerCase()} />
              {agent.agent_role === "LEAD" && (
                <Badge variant="outline" className="gap-1 text-micro">
                  <Crown className="h-3 w-3" /> Lead
                </Badge>
              )}
              {isLive && (
                <span className="inline-flex items-center gap-1 text-micro text-emerald-400">
                  <StatusDot status="IN_PROGRESS" live className="h-1.5 w-1.5" />
                  live
                </span>
              )}
            </div>
            {agent.role_title && (
              <p className="text-body text-muted-foreground mt-0.5">{agent.role_title}</p>
            )}
            {agent.description && (
              <p className="text-label text-muted-foreground/80 mt-1 line-clamp-2">{agent.description}</p>
            )}
          </div>
          <div className="flex items-center gap-2 shrink-0">
            <Button variant="outline" size="sm" className="h-8 gap-1.5" asChild>
              <Link href={`${agentPath}/chat`}>
                <MessageSquare className="h-3.5 w-3.5" />
                Chat
              </Link>
            </Button>
            <Button variant="outline" size="sm" className="h-8 gap-1.5" asChild>
              <Link href={`${agentPath}/logs`}>
                <ScrollText className="h-3.5 w-3.5" />
                Logs
              </Link>
            </Button>
            <Button variant="outline" size="sm" className="h-8 gap-1.5" asChild>
              <Link href={`${agentPath}/settings`}>
                <Settings className="h-3.5 w-3.5" />
                Settings
              </Link>
            </Button>
            <Button size="sm" className="h-8 gap-1.5" asChild>
              <Link href={agentPath}>
                Open full
                <ExternalLink className="h-3.5 w-3.5" />
              </Link>
            </Button>
          </div>
        </div>

        {/* Stats strip */}
        <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-5 gap-3">
          <StatMiniCard href={`${agentPath}/sessions`} icon={MessageSquare} label="Sessions" value={sessionCount} />
          <StatMiniCard href={`${agentPath}/runs`} icon={Zap} label="Runs" value={runs.length > 0 ? runs.length : "—"} />
          <StatMiniCard href={`${agentPath}/tools?section=skills`} icon={Puzzle} label="Skills" value={skillCount} />
          <StatMiniCard href={`${agentPath}/tools?section=credentials`} icon={KeyRound} label="Credentials" value={credCount} />
          <StatMiniCard href={`${agentPath}/logs`} icon={Clock} label="Last active" value={agent.last_active_at ? timeAgo(agent.last_active_at) : "—"} />
        </div>

        {/* Body: runtime + recent activity */}
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          {/* Runtime */}
          <Card>
            <CardContent className="p-4 space-y-3">
              <h3 className="text-label font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-2">
                <Cpu className="h-3.5 w-3.5" />
                Runtime
              </h3>
              <dl className="space-y-2 text-label">
                <Row label="Adapter">
                  <span className="font-mono">{agent.cli_adapter}</span>
                </Row>
                <Row label="Model">
                  <span className="font-mono text-micro">{agent.llm_provider} / {agent.llm_model}</span>
                </Row>
                <Row label="Role">
                  <span className="inline-flex items-center gap-1">
                    {agent.agent_role === "LEAD" ? <Crown className="h-3 w-3" /> : <Bot className="h-3 w-3" />}
                    {agent.agent_role}
                  </span>
                </Row>
                {agent.crew && (
                  <Row label="Crew">
                    <Link href={`/fleet/crews/${agent.crew_id}`} className="hover:underline">
                      {agent.crew.name}
                    </Link>
                  </Row>
                )}
              </dl>
            </CardContent>
          </Card>

          {/* Recent activity */}
          <div className="space-y-4">
            <Card>
              <CardContent className="p-4 space-y-2.5">
                <div className="flex items-center justify-between">
                  <h3 className="text-label font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-2">
                    <MessageSquare className="h-3.5 w-3.5" />
                    Recent sessions
                  </h3>
                  <Link href={`${agentPath}/sessions`} className="text-micro text-primary hover:underline">
                    View all
                  </Link>
                </div>
                {sessions.length === 0 ? (
                  <p className="text-micro text-muted-foreground">No sessions yet.</p>
                ) : (
                  <ul className="space-y-1.5">
                    {sessions.slice(0, 3).map((s) => (
                      <li key={s.id}>
                        <Link
                          href={`${agentPath}/chat?session=${s.id}`}
                          className="flex items-center gap-2 py-1 px-1 -mx-1 rounded hover:bg-accent/50 transition-colors"
                        >
                          <span className="text-body truncate flex-1">{s.title || "Untitled session"}</span>
                          <Badge variant="outline" className="text-micro h-4 px-1">{s.status}</Badge>
                          <span className="text-micro text-muted-foreground tabular-nums shrink-0">
                            {timeAgo(s.created_at)}
                          </span>
                        </Link>
                      </li>
                    ))}
                  </ul>
                )}
              </CardContent>
            </Card>

            <Card>
              <CardContent className="p-4 space-y-2.5">
                <div className="flex items-center justify-between">
                  <h3 className="text-label font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-2">
                    <Zap className="h-3.5 w-3.5" />
                    Recent runs
                  </h3>
                  <Link href={`${agentPath}/runs`} className="text-micro text-primary hover:underline">
                    View all
                  </Link>
                </div>
                {runs.length === 0 ? (
                  <p className="text-micro text-muted-foreground">No runs yet.</p>
                ) : (
                  <ul className="space-y-1.5">
                    {runs.slice(0, 3).map((r) => {
                      const dur = r.duration_ms ?? (r.started_at && r.ended_at
                        ? new Date(r.ended_at).getTime() - new Date(r.started_at).getTime()
                        : null)
                      return (
                        <li key={r.id}>
                          <Link
                            href={`${agentPath}/runs`}
                            className="flex items-center gap-2 py-1 px-1 -mx-1 rounded hover:bg-accent/50 transition-colors"
                          >
                            <span className={cn(
                              "text-micro font-mono uppercase shrink-0",
                              RUN_STATUS_COLOR[r.status] || "text-muted-foreground",
                            )}>
                              {r.status}
                            </span>
                            {r.trigger_type && (
                              <Badge variant="outline" className="text-micro h-4 px-1 font-normal">
                                {r.trigger_type}
                              </Badge>
                            )}
                            <span className="flex-1" />
                            {dur !== null && (
                              <span className="text-micro text-muted-foreground/70 tabular-nums shrink-0">
                                {formatDuration(dur)}
                              </span>
                            )}
                            <span className="text-micro text-muted-foreground tabular-nums shrink-0">
                              {timeAgo(r.created_at)}
                            </span>
                          </Link>
                        </li>
                      )
                    })}
                  </ul>
                )}
              </CardContent>
            </Card>
          </div>
        </div>
      </div>
    </div>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4">
      <dt className="text-micro text-muted-foreground uppercase tracking-wider">{label}</dt>
      <dd className="text-body text-foreground/90 truncate">{children}</dd>
    </div>
  )
}

function StatMiniCard({
  href, icon: Icon, label, value,
}: {
  href: string
  icon: React.ElementType
  label: string
  value: string | number
}) {
  return (
    <Link
      href={href}
      className="rounded-lg border border-border bg-card/50 px-3 py-2.5 hover:bg-accent/40 hover:border-border/80 transition-colors group"
    >
      <div className="flex items-center gap-1.5 text-micro text-muted-foreground uppercase tracking-wider">
        <Icon className="h-3 w-3" />
        {label}
      </div>
      <p className="text-title font-semibold tabular-nums mt-1 group-hover:text-foreground">
        {value}
      </p>
    </Link>
  )
}
