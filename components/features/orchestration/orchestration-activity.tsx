"use client"

import { useMemo, useState } from "react"
import { Badge } from "@/components/ui/badge"
import { EmptyState } from "@/components/layout/empty-state"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Activity } from "lucide-react"
import { cn } from "@/lib/utils"
import type { Mission } from "@/lib/types/mission"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"

interface OrchestrationActivityProps {
  missions: Mission[]
  highlightSlugs?: Set<string> | null
}

interface ActivityEvent {
  id: string
  timestamp: Date
  type: "mission" | "task"
  status: string
  title: string
  subtitle: string
  missionTitle: string
  agentSlug: string
  missionId: string
  tokenCount: number | null
  estimatedCost: number | null
}

function buildActivityFeed(missions: Mission[]): ActivityEvent[] {
  const events: ActivityEvent[] = []
  for (const mission of missions) {
    events.push({
      id: `m-${mission.id}`,
      timestamp: new Date(mission.updated_at),
      type: "mission",
      status: mission.status,
      title: mission.title,
      subtitle: `Lead: @${mission.lead_agent_slug}`,
      missionTitle: mission.title,
      agentSlug: mission.lead_agent_slug,
      missionId: mission.id,
      tokenCount: mission.total_token_count,
      estimatedCost: mission.total_estimated_cost,
    })
    for (const task of mission.tasks || []) {
      if (task.status === "PENDING") continue
      const ts = task.completed_at || task.started_at || task.created_at
      events.push({
        id: `t-${task.id}`,
        timestamp: new Date(ts),
        type: "task",
        status: task.status,
        title: task.title,
        subtitle: `@${task.agent_slug || "unassigned"}${task.iteration && task.max_iterations && task.max_iterations > 1 ? ` (iter ${task.iteration}/${task.max_iterations})` : ""}`,
        missionTitle: mission.title,
        agentSlug: task.agent_slug || "",
        missionId: mission.id,
        tokenCount: task.token_count,
        estimatedCost: task.estimated_cost,
      })
    }
  }
  return events.sort((a, b) => b.timestamp.getTime() - a.timestamp.getTime())
}

function timeAgo(date: Date): string {
  const diff = Date.now() - date.getTime()
  const seconds = Math.floor(diff / 1000)
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  return `${Math.floor(hours / 24)}d ago`
}

function getTimeGroup(date: Date): string {
  const now = new Date()
  const diff = now.getTime() - date.getTime()
  const mins = diff / 60000
  if (mins < 5) return "Just now"
  const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime()
  if (date.getTime() >= startOfToday) return "Today"
  const startOfYesterday = startOfToday - 86400000
  if (date.getTime() >= startOfYesterday) return "Yesterday"
  return "Earlier"
}

function formatTokens(count: number): string {
  if (count >= 1000) return `${(count / 1000).toFixed(1)}k tok`
  return `${count} tok`
}

const statusFilters = ["ALL", "IN_PROGRESS", "COMPLETED", "FAILED", "BLOCKED"] as const
type StatusFilter = (typeof statusFilters)[number]

const statusColors: Record<string, { dot: string; pulse?: string; pill: string }> = {
  COMPLETED:   { dot: "bg-emerald-500", pill: "bg-emerald-500/15 text-emerald-400 border-emerald-500/20" },
  IN_PROGRESS: { dot: "bg-blue-500", pulse: "bg-blue-400", pill: "bg-blue-500/15 text-blue-400 border-blue-500/20" },
  FAILED:      { dot: "bg-red-500", pill: "bg-red-500/15 text-red-400 border-red-500/20" },
  BLOCKED:     { dot: "bg-amber-500", pill: "bg-amber-500/15 text-amber-400 border-amber-500/20" },
  CANCELLED:   { dot: "bg-zinc-500", pill: "bg-zinc-500/15 text-zinc-400 border-zinc-500/20" },
  PLANNING:    { dot: "bg-violet-500", pill: "bg-violet-500/15 text-violet-400 border-violet-500/20" },
  REVIEW:      { dot: "bg-cyan-500", pill: "bg-cyan-500/15 text-cyan-400 border-cyan-500/20" },
  SKIPPED:     { dot: "bg-zinc-500", pill: "bg-zinc-500/15 text-zinc-400 border-zinc-500/20" },
}

const filterLabels: Record<StatusFilter, string> = {
  ALL: "All", IN_PROGRESS: "Running", COMPLETED: "Done", FAILED: "Failed", BLOCKED: "Blocked",
}

function StatusDot({ status, isMission }: { status: string; isMission: boolean }) {
  const colors = statusColors[status] || statusColors.CANCELLED
  if (isMission) {
    return (
      <div className="relative flex items-center justify-center size-4">
        <div className={cn("size-2.5 rotate-45 rounded-[1px]", colors.dot)} />
        {status === "IN_PROGRESS" && colors.pulse && (
          <div className={cn("absolute size-2.5 rotate-45 rounded-[1px] animate-ping opacity-40", colors.pulse)} />
        )}
      </div>
    )
  }
  return (
    <div className="relative flex items-center justify-center size-4">
      <div className={cn("size-2.5 rounded-full", colors.dot)} />
      {status === "IN_PROGRESS" && colors.pulse && (
        <div className={cn("absolute size-2.5 rounded-full animate-ping opacity-40", colors.pulse)} />
      )}
    </div>
  )
}

export function OrchestrationActivity({ missions, highlightSlugs }: OrchestrationActivityProps) {
  const allEvents = useMemo(() => buildActivityFeed(missions), [missions])
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("ALL")
  const [agentFilter, setAgentFilter] = useState<string>("all")

  const agents = useMemo(() => {
    const set = new Set<string>()
    for (const e of allEvents) {
      if (e.agentSlug) set.add(e.agentSlug)
    }
    return [...set].sort()
  }, [allEvents])

  const events = useMemo(() => {
    let filtered = allEvents
    if (statusFilter !== "ALL") {
      filtered = filtered.filter((e) => e.status === statusFilter)
    }
    if (agentFilter !== "all") {
      filtered = filtered.filter((e) => e.agentSlug === agentFilter)
    }
    return filtered
  }, [allEvents, statusFilter, agentFilter])

  const grouped = useMemo(() => {
    const groups: { label: string; items: ActivityEvent[] }[] = []
    let currentLabel = ""
    for (const event of events.slice(0, 100)) {
      const label = getTimeGroup(event.timestamp)
      if (label !== currentLabel) {
        groups.push({ label, items: [] })
        currentLabel = label
      }
      groups[groups.length - 1].items.push(event)
    }
    return groups
  }, [events])

  if (allEvents.length === 0) {
    return (
      <div className="py-12">
        <EmptyState
          icon={Activity}
          title="No activity yet"
          description="Task status changes and mission events will appear here in real-time"
        />
      </div>
    )
  }

  return (
    <div className="space-y-4">
      {/* Filter bar */}
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-1.5">
          {statusFilters.map((s) => (
            <button
              key={s}
              onClick={() => setStatusFilter(s)}
              className={cn(
                "px-3 py-1.5 rounded-md text-xs font-medium transition-colors",
                statusFilter === s
                  ? "bg-foreground/10 text-foreground"
                  : "text-muted-foreground hover:text-foreground hover:bg-foreground/5"
              )}
            >
              {filterLabels[s]}
            </button>
          ))}
        </div>
        <Select value={agentFilter} onValueChange={setAgentFilter}>
          <SelectTrigger size="sm" className="w-[140px] text-xs">
            <SelectValue placeholder="All agents" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All agents</SelectItem>
            {agents.map((slug) => (
              <SelectItem key={slug} value={slug}>@{slug}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {/* Timeline */}
      {events.length === 0 ? (
        <p className="text-sm text-muted-foreground text-center py-8">No matching events</p>
      ) : (
        <div className="space-y-0">
          {grouped.map((group) => (
            <div key={group.label}>
              <div className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground/70 pl-8 py-2">
                {group.label}
              </div>
              {group.items.map((event, idx) => {
                const colors = statusColors[event.status] || statusColors.CANCELLED
                const isLast = idx === group.items.length - 1
                return (
                  <div
                    key={event.id}
                    className={cn(
                      "flex items-stretch group",
                      highlightSlugs && event.type === "task" && !highlightSlugs.has(event.agentSlug) && "opacity-20",
                    )}
                    style={{ transition: "opacity 0.3s ease" }}
                  >
                    {/* Timeline rail */}
                    <div className="flex flex-col items-center w-8 shrink-0">
                      <div className={cn("w-px flex-1", isLast && idx === 0 ? "bg-transparent" : "bg-border/60")} />
                      <div className="py-1"><StatusDot status={event.status} isMission={event.type === "mission"} /></div>
                      <div className={cn("w-px flex-1", isLast ? "bg-transparent" : "bg-border/60")} />
                    </div>
                    {/* Content */}
                    <div className="flex-1 min-w-0 py-2 pl-2 pr-1 rounded-md transition-colors hover:bg-muted/40">
                      <div className="flex items-center gap-2">
                        <span className="text-[11px] text-muted-foreground/60 shrink-0 w-14 tabular-nums">
                          {timeAgo(event.timestamp)}
                        </span>
                        <span className={cn("text-sm font-medium truncate", event.type === "mission" && "text-foreground")}>
                          {event.title}
                        </span>
                        <Badge
                          className={cn("text-[10px] px-1.5 py-0 border shrink-0", colors.pill)}
                        >
                          {event.status.replace("_", " ")}
                        </Badge>
                      </div>
                      <div className="flex items-center gap-1.5 mt-0.5 pl-14">
                        {event.agentSlug && (
                          <img
                            src={getAgentAvatarUrl(event.agentSlug)}
                            alt=""
                            className="w-4 h-4 rounded-full shrink-0"
                          />
                        )}
                        <span className="text-xs text-muted-foreground">{event.subtitle}</span>
                        {event.type === "task" && (
                          <span className="text-xs text-muted-foreground/50">
                            · {event.missionTitle}
                          </span>
                        )}
                        {event.tokenCount != null && event.tokenCount > 0 && (
                          <span className="text-xs text-muted-foreground/50">
                            · {formatTokens(event.tokenCount)}
                          </span>
                        )}
                        {event.estimatedCost != null && event.estimatedCost > 0 && (
                          <span className="text-xs text-muted-foreground/50">
                            · ${event.estimatedCost.toFixed(2)}
                          </span>
                        )}
                      </div>
                    </div>
                  </div>
                )
              })}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
