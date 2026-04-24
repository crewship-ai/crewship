"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import {
  MessageSquare, ScrollText, Settings, ExternalLink,
  Zap, Puzzle, KeyRound, Clock, Cpu, Crown, Bot,
  DollarSign, CalendarClock, FileText, Users, ChevronDown, ChevronRight,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { StatusBadge, StatusDot } from "@/components/ui/status-badge"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { timeAgo, formatDuration } from "@/lib/time"
import { cn } from "@/lib/utils"
import { useAgentInbox } from "@/hooks/use-agent-inbox"

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

interface AgentDetailExtra {
  system_prompt: string | null
  schedule_cron: string | null
  schedule_enabled: boolean | null
  schedule_next_run: string | null
  schedule_last_run: string | null
  schedule_prompt: string | null
  memory_enabled?: boolean
  timeout_seconds?: number
}

interface PeerAgent {
  id: string
  name: string
  slug: string
  avatar_seed?: string | null
  avatar_style?: string | null
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

export interface CrewsAgentInlineProps {
  agent: AgentData
  workspaceId: string
}

/**
 * Middle-pane agent detail, shown when an agent is selected in /crews.
 * Condensed analogue of the agent Overview full page — hero, quick actions,
 * 5-card stats strip, runtime card, recent sessions + recent runs.
 * No tabs: for deep work the user clicks "Open full →".
 */
export function CrewsAgentInline({ agent, workspaceId }: CrewsAgentInlineProps) {
  const [sessions, setSessions] = useState<SessionRow[]>([])
  const [runs, setRuns] = useState<RunRow[]>([])
  const [detail, setDetail] = useState<AgentDetailExtra | null>(null)
  const [peers, setPeers] = useState<PeerAgent[]>([])
  const [promptExpanded, setPromptExpanded] = useState(false)
  const { inbox } = useAgentInbox(agent.id)

  useEffect(() => {
    if (!workspaceId) return
    // Reset pane state immediately on agent switch — the new header
    // already rendered with the fresh agent prop, so leaving sessions /
    // runs / detail from the previous agent visible while fetches are
    // in flight is a confusing half-second of stale data.
    setSessions([])
    setRuns([])
    setDetail(null)
    const controller = new AbortController()
    // Swallow fetch-network failures but re-throw AbortError so the outer
    // Promise.all rejects on agent switch — otherwise stale empty arrays
    // would overwrite the panel for the next agent.
    const swallowNonAbort = <T,>(fallback: T) => (err: unknown): T => {
      if ((err as { name?: string })?.name === "AbortError") throw err
      return fallback
    }
    Promise.all([
      fetch(`/api/v1/agents/${agent.id}/chats?workspace_id=${workspaceId}&limit=3`, { signal: controller.signal })
        .then((r) => r.ok ? r.json() : [])
        .catch(swallowNonAbort<SessionRow[]>([])),
      fetch(`/api/v1/agents/${agent.id}/runs?workspace_id=${workspaceId}&limit=3`, { signal: controller.signal })
        .then((r) => r.ok ? r.json() : [])
        .catch(swallowNonAbort<RunRow[]>([])),
      fetch(`/api/v1/agents/${agent.id}?workspace_id=${workspaceId}`, { signal: controller.signal })
        .then((r) => r.ok ? r.json() : null)
        .catch(swallowNonAbort<AgentDetailExtra | null>(null)),
    ])
      .then(([s, r, d]: [SessionRow[], RunRow[], AgentDetailExtra | null]) => {
        if (controller.signal.aborted) return
        setSessions(Array.isArray(s) ? s : [])
        setRuns(Array.isArray(r) ? r : [])
        setDetail(d)
      })
      .catch((err) => {
        if ((err as { name?: string })?.name === "AbortError") return
        // genuine failure — leave previous state untouched
      })
    return () => controller.abort()
  }, [agent.id, workspaceId])

  // Peer list for crew context row. Clear immediately on agent switch so a
  // failed fetch doesn't leave the previous agent's peers under the new
  // header, and store the full list — UI-level slicing handles the visible
  // cap so a `+N more` chip can still render when there are many peers.
  useEffect(() => {
    setPeers([])
    if (!workspaceId || !agent.crew_id) return
    const controller = new AbortController()
    fetch(`/api/v1/agents?workspace_id=${workspaceId}&crew_id=${agent.crew_id}`, {
      signal: controller.signal,
    })
      .then((r) => r.ok ? r.json() : [])
      .then((agents: PeerAgent[]) => {
        if (controller.signal.aborted) return
        setPeers(
          Array.isArray(agents)
            ? agents.filter((a) => a.id !== agent.id)
            : [],
        )
      })
      .catch((err) => {
        if ((err as { name?: string })?.name === "AbortError") return
        setPeers([])
      })
    return () => controller.abort()
  }, [agent.id, agent.crew_id, workspaceId])

  // Countdown ticks every 30s so the 'next run' chip moves while the user
  // stays on the page. Round to minutes — sub-minute precision isn't worth
  // the 1s render storm.
  const [nowTick, setNowTick] = useState(() => Date.now())
  useEffect(() => {
    if (!detail?.schedule_enabled || !detail?.schedule_next_run) return
    const interval = setInterval(() => setNowTick(Date.now()), 30_000)
    return () => clearInterval(interval)
  }, [detail?.schedule_enabled, detail?.schedule_next_run])

  const scheduleCountdown = useMemo(() => {
    if (!detail?.schedule_enabled || !detail?.schedule_next_run) return null
    const ms = new Date(detail.schedule_next_run).getTime() - nowTick
    if (ms <= 0) return "imminent"
    const mins = Math.floor(ms / 60_000)
    if (mins < 60) return `${mins}m`
    const hrs = Math.floor(mins / 60)
    if (hrs < 24) return `${hrs}h ${mins % 60}m`
    const days = Math.floor(hrs / 24)
    return `${days}d ${hrs % 24}h`
  }, [detail?.schedule_enabled, detail?.schedule_next_run, nowTick])

  const agentPath = `/crews/agents/${agent.id}`
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
      <div className="p-4 sm:p-6 max-w-5xl space-y-4 sm:space-y-5">
        {/* Hero — responsive: actions stack below on mobile */}
        <div className="flex flex-col sm:flex-row sm:items-start gap-4">
          <div className="flex items-start gap-3 sm:gap-4 flex-1 min-w-0">
            <img
              src={avatarUrl}
              alt=""
              className="h-12 w-12 sm:h-16 sm:w-16 rounded-xl sm:rounded-2xl shrink-0 border border-border"
            />
            <div className="flex-1 min-w-0">
              <div className="flex items-center gap-2 sm:gap-3 flex-wrap">
                <h1 className="text-body sm:text-title font-semibold truncate">{agent.name}</h1>
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
                <p className="text-label sm:text-body text-muted-foreground mt-0.5 truncate">{agent.role_title}</p>
              )}
              {agent.description && (
                <p className="text-micro sm:text-label text-muted-foreground/80 mt-1 line-clamp-2">{agent.description}</p>
              )}
            </div>
          </div>

          <div className="grid grid-cols-4 sm:flex sm:items-center gap-2 shrink-0 w-full sm:w-auto">
            <Button variant="outline" size="sm" className="h-8 gap-1.5 px-2 sm:px-3" asChild>
              <Link href={`${agentPath}/chat`} aria-label="Chat">
                <MessageSquare className="h-3.5 w-3.5" />
                <span className="hidden sm:inline">Chat</span>
              </Link>
            </Button>
            <Button variant="outline" size="sm" className="h-8 gap-1.5 px-2 sm:px-3" asChild>
              <Link href={`${agentPath}/logs`} aria-label="Logs">
                <ScrollText className="h-3.5 w-3.5" />
                <span className="hidden sm:inline">Logs</span>
              </Link>
            </Button>
            <Button variant="outline" size="sm" className="h-8 gap-1.5 px-2 sm:px-3" asChild>
              <Link href={`${agentPath}/settings`} aria-label="Settings">
                <Settings className="h-3.5 w-3.5" />
                <span className="hidden sm:inline">Settings</span>
              </Link>
            </Button>
            <Button size="sm" className="h-8 gap-1.5 px-2 sm:px-3" asChild>
              <Link href={agentPath} aria-label="Open full agent page">
                <span className="hidden sm:inline">Open full</span>
                <ExternalLink className="h-3.5 w-3.5" />
              </Link>
            </Button>
          </div>
        </div>

        {/* Stats strip — 2 cols on mobile, 6 on desktop */}
        <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-6 gap-2 sm:gap-3">
          <StatMiniCard href={`${agentPath}/sessions`} icon={MessageSquare} label="Sessions" value={sessionCount} />
          <StatMiniCard href={`${agentPath}/runs`} icon={Zap} label="Recent runs" value={runs.length > 0 ? runs.length : "—"} />
          <StatMiniCard
            href={agent.crew_id ? `/paymaster?crew=${agent.crew_id}` : "/paymaster"}
            icon={DollarSign}
            label="Cost (month)"
            value={inbox && inbox.cost_usd_this_month > 0 ? `$${inbox.cost_usd_this_month.toFixed(2)}` : "—"}
          />
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
                    <Link href={`/crews/${agent.crew_id}`} className="hover:underline">
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

        {/* Schedule card — only when agent has an enabled cron schedule */}
        {detail?.schedule_enabled && detail?.schedule_cron && (
          <Card>
            <CardContent className="p-4 space-y-3">
              <div className="flex items-center justify-between">
                <h3 className="text-label font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-2">
                  <CalendarClock className="h-3.5 w-3.5" />
                  Schedule
                </h3>
                <Link
                  href={`${agentPath}/settings?section=schedule`}
                  className="text-micro text-primary hover:underline"
                >
                  Edit
                </Link>
              </div>
              <dl className="grid grid-cols-1 sm:grid-cols-3 gap-2 text-label">
                <Row label="Cron">
                  <span className="font-mono text-micro">{detail.schedule_cron}</span>
                </Row>
                <Row label="Next run">
                  <span className="tabular-nums">{scheduleCountdown ?? "—"}</span>
                </Row>
                <Row label="Last run">
                  <span className="text-micro">
                    {detail.schedule_last_run ? timeAgo(detail.schedule_last_run) : "never"}
                  </span>
                </Row>
              </dl>
              {detail.schedule_prompt && (
                <p className="text-micro text-muted-foreground/80 line-clamp-2">
                  {detail.schedule_prompt}
                </p>
              )}
            </CardContent>
          </Card>
        )}

        {/* System prompt peek — collapsible */}
        {detail?.system_prompt && (
          <Card>
            <CardContent className="p-4 space-y-2">
              <button
                type="button"
                onClick={() => setPromptExpanded((v) => !v)}
                className="w-full flex items-center justify-between text-left group"
                aria-expanded={promptExpanded}
              >
                <h3 className="text-label font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-2">
                  <FileText className="h-3.5 w-3.5" />
                  System prompt
                </h3>
                <span className="flex items-center gap-1 text-micro text-muted-foreground group-hover:text-foreground">
                  {promptExpanded ? "Hide" : "Show"}
                  {promptExpanded ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
                </span>
              </button>
              <pre className={cn(
                "text-micro text-muted-foreground whitespace-pre-wrap font-mono bg-muted/30 p-3 rounded",
                !promptExpanded && "line-clamp-3",
              )}>
                {detail.system_prompt}
              </pre>
              {!promptExpanded && detail.system_prompt.length > 200 && (
                <p className="text-micro text-muted-foreground/70">
                  {detail.system_prompt.length.toLocaleString()} characters — click to expand
                </p>
              )}
              <Link
                href={`${agentPath}/settings`}
                className="inline-flex items-center gap-1 text-micro text-primary hover:underline"
              >
                Edit in Settings <ExternalLink className="h-3 w-3" />
              </Link>
            </CardContent>
          </Card>
        )}

        {/* Peer context — crew peers row + outside the crew when no peers */}
        {agent.crew_id && peers.length > 0 && (
          <Card>
            <CardContent className="p-4 space-y-3">
              <div className="flex items-center justify-between">
                <h3 className="text-label font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-2">
                  <Users className="h-3.5 w-3.5" />
                  Crew peers
                </h3>
                {agent.crew && (
                  <Link
                    href={`/crews/${agent.crew_id}`}
                    className="text-micro text-primary hover:underline truncate"
                  >
                    {agent.crew.name} →
                  </Link>
                )}
              </div>
              <div className="flex items-center gap-2 flex-wrap">
                {peers.slice(0, 8).map((peer) => (
                  <Link
                    key={peer.id}
                    href={`/crews?agent=${peer.slug}&crew=${agent.crew?.slug ?? ""}`}
                    className="group flex items-center gap-2 rounded-lg border border-border bg-card/50 px-2 py-1.5 hover:bg-accent/40 transition-colors"
                    title={peer.name}
                  >
                    <img
                      src={getAgentAvatarUrl(
                        peer.avatar_seed || peer.name,
                        peer.avatar_style || agent.crew?.avatar_style,
                      )}
                      alt=""
                      loading="lazy"
                      className="h-6 w-6 rounded"
                    />
                    <span className="text-micro font-medium group-hover:text-foreground">
                      {peer.name}
                    </span>
                  </Link>
                ))}
                {peers.length > 8 && (
                  <Link
                    href={`/crews/${agent.crew_id}`}
                    className="flex items-center justify-center h-9 px-2.5 rounded-lg border border-dashed border-border bg-card/30 text-micro text-muted-foreground hover:text-foreground hover:bg-accent/40 transition-colors"
                    title={`+${peers.length - 8} more peers`}
                    aria-label={`Show ${peers.length - 8} more peers in crew`}
                  >
                    +{peers.length - 8}
                  </Link>
                )}
              </div>
              {inbox && inbox.peer_messages.length > 0 && (
                <div className="pt-2 border-t border-border space-y-1.5">
                  <p className="text-micro text-muted-foreground/70 uppercase tracking-wider">
                    Recent peer messages
                  </p>
                  {inbox.peer_messages.slice(0, 3).map((pm) => (
                    <div
                      key={pm.id}
                      className="flex items-center gap-2 text-label py-0.5"
                    >
                      <span className={cn(
                        "inline-flex items-center gap-1 text-micro font-medium",
                        pm.direction === "incoming" ? "text-amber-400" : "text-muted-foreground/70",
                      )}>
                        {pm.direction === "incoming" ? "← " : "→ "}
                        {pm.from_agent_name}
                      </span>
                      <span className="text-body text-foreground/80 truncate flex-1">
                        {pm.question}
                      </span>
                      <span className="text-micro text-muted-foreground tabular-nums shrink-0">
                        {timeAgo(pm.created_at)}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        )}
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
