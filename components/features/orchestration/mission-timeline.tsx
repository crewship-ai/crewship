"use client"

import { useCallback, useMemo, useState } from "react"
import { Badge } from "@/components/ui/badge"
import { EmptyState } from "@/components/layout/empty-state"
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { ChevronDown, Clock } from "lucide-react"
import { cn } from "@/lib/utils"
import type { Mission, MissionTask, MissionStatus } from "@/lib/types/mission"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { formatDurationRounded } from "@/lib/time"

interface MissionTimelineProps {
  missions: Mission[]
  highlightSlugs?: Set<string> | null
}

const LANE_H = 52
const AGENT_W = 160
const TICK_COUNT = 8

const statusBadge: Record<MissionStatus, string> = {
  BACKLOG: "border-slate-400 text-slate-400",
  TODO: "border-slate-400 text-slate-400",
  IN_PROGRESS: "border-blue-500 text-blue-400",
  COMPLETED: "border-emerald-500 text-emerald-400",
  DONE: "border-emerald-500 text-emerald-400",
  FAILED: "border-red-500 text-red-400",
  PLANNING: "border-slate-400 text-slate-400",
  REVIEW: "border-purple-500 text-purple-400",
  CANCELLED: "border-gray-500 text-gray-400",
  DUPLICATE: "border-gray-500 text-gray-400",
}

function getTimeRange(missions: Mission[]): { start: number; end: number } {
  let start = Date.now()
  let end = Date.now()
  for (const m of missions) {
    const created = new Date(m.created_at).getTime()
    if (created < start) start = created
    const updated = new Date(m.updated_at).getTime()
    if (updated > end) end = updated
    for (const t of m.tasks || []) {
      if (t.started_at) {
        const s = new Date(t.started_at).getTime()
        if (s < start) start = s
      }
      if (t.completed_at) {
        const c = new Date(t.completed_at).getTime()
        if (c > end) end = c
      }
    }
  }
  const range = end - start || 3600000
  return { start: start - range * 0.05, end: end + range * 0.05 }
}

const TASK_BAR_STYLES: Record<string, React.CSSProperties> = {
  COMPLETED: { background: "rgba(16,185,129,0.18)", border: "1px solid rgba(16,185,129,0.4)", color: "#6ee7b7" },
  IN_PROGRESS: {
    background: "linear-gradient(90deg, #0E6BE8 0%, #1E7BFE 40%, #5DA1FF 60%, #1E7BFE 100%)",
    backgroundSize: "300% 100%",
    border: "1px solid rgba(30,123,254,0.5)",
    color: "#fff",
    animation: "shimmer 2.5s linear infinite",
  },
  BLOCKED: {
    background: "repeating-linear-gradient(-45deg, rgba(245,158,11,0.15) 0px 4px, rgba(245,158,11,0.05) 4px 8px)",
    border: "1px solid rgba(245,158,11,0.4)",
    color: "#fcd34d",
  },
  PENDING: { background: "rgba(72,79,88,0.2)", border: "1.5px dashed rgba(72,79,88,0.5)", color: "rgb(72,79,88)" },
  FAILED: { background: "rgba(244,63,94,0.15)", border: "1px solid rgba(244,63,94,0.4)", color: "#fda4af" },
  SKIPPED: { background: "rgba(72,79,88,0.2)", border: "1px solid rgba(72,79,88,0.3)", color: "rgb(107,114,128)" },
}

function TaskBar({ task, timeRange }: { task: MissionTask; timeRange: { start: number; end: number } }) {
  const range = timeRange.end - timeRange.start
  const taskStart = task.started_at ? new Date(task.started_at).getTime() : new Date(task.created_at).getTime()
  const taskEnd = task.completed_at
    ? new Date(task.completed_at).getTime()
    : task.status === "IN_PROGRESS" ? Date.now() : taskStart + range * 0.02

  const left = Math.max(0, ((taskStart - timeRange.start) / range) * 100)
  const width = Math.max(0.5, ((taskEnd - taskStart) / range) * 100)
  const duration = task.duration_ms ?? (task.completed_at ? new Date(task.completed_at).getTime() - taskStart : null)

  return (
    <TooltipProvider delayDuration={80}>
      <Tooltip>
        <TooltipTrigger asChild>
          <div
            className="absolute top-[10px] h-[32px] rounded flex items-center px-2 cursor-pointer transition-all hover:brightness-115 hover:-translate-y-px overflow-hidden z-10 hover:z-25"
            style={{
              left: `${left}%`,
              width: `${width}%`,
              minWidth: "4px",
              ...(TASK_BAR_STYLES[task.status] ?? TASK_BAR_STYLES.PENDING),
            }}
          >
            {width > 5 && (
              <span className="text-[11px] font-medium truncate pointer-events-none">{task.title}</span>
            )}
          </div>
        </TooltipTrigger>
        <TooltipContent side="top" className="max-w-[240px] p-3">
          <p className="font-semibold text-xs mb-1">{task.title}</p>
          <div className="space-y-0.5 text-[11px]">
            <div className="flex justify-between gap-3">
              <span className="text-muted-foreground">Status</span>
              <span className="font-mono">{task.status}</span>
            </div>
            {task.started_at && (
              <div className="flex justify-between gap-3">
                <span className="text-muted-foreground">Start</span>
                <span className="font-mono">{new Date(task.started_at).toLocaleTimeString()}</span>
              </div>
            )}
            {task.completed_at && (
              <div className="flex justify-between gap-3">
                <span className="text-muted-foreground">End</span>
                <span className="font-mono">{new Date(task.completed_at).toLocaleTimeString()}</span>
              </div>
            )}
            {duration != null && (
              <div className="flex justify-between gap-3">
                <span className="text-muted-foreground">Duration</span>
                <span className="font-mono">{formatDurationRounded(duration)}</span>
              </div>
            )}
            {task.token_count != null && (
              <div className="flex justify-between gap-3">
                <span className="text-muted-foreground">Tokens</span>
                <span className="font-mono">{task.token_count.toLocaleString()}</span>
              </div>
            )}
            {task.estimated_cost != null && (
              <div className="flex justify-between gap-3">
                <span className="text-muted-foreground">Cost</span>
                <span className="font-mono">${task.estimated_cost.toFixed(4)}</span>
              </div>
            )}
          </div>
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

export function MissionTimeline({ missions, highlightSlugs }: MissionTimelineProps) {
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())

  const activeMissions = useMemo(() => {
    return missions
      .filter((m) => (m.tasks?.length ?? 0) > 0)
      .sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime())
      .slice(0, 10)
  }, [missions])

  const timeRange = useMemo(() => getTimeRange(activeMissions), [activeMissions])

  const ticks = useMemo(() => {
    const range = timeRange.end - timeRange.start
    return Array.from({ length: TICK_COUNT }, (_, i) => {
      const t = timeRange.start + (i / (TICK_COUNT - 1)) * range
      return { time: t, pct: (i / (TICK_COUNT - 1)) * 100, label: new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) }
    })
  }, [timeRange])

  const nowPct = useMemo(() => {
    const range = timeRange.end - timeRange.start
    const pct = ((Date.now() - timeRange.start) / range) * 100
    return pct >= 0 && pct <= 100 ? pct : null
  }, [timeRange])

  const toggle = useCallback((id: string) => setCollapsed((prev) => {
    const next = new Set(prev)
    next.has(id) ? next.delete(id) : next.add(id)
    return next
  }), [])

  if (activeMissions.length === 0) {
    return (
      <div className="rounded-lg border border-border bg-card p-12">
        <EmptyState icon={Clock} title="No timeline data" description="Missions with tasks will appear here as a Gantt timeline" />
      </div>
    )
  }

  return (
    <>
      <style>{`@keyframes shimmer{0%{background-position:100% 0}100%{background-position:-200% 0}}`}</style>
      <div className="flex flex-col gap-3">
        {activeMissions.map((mission) => {
          const isOpen = !collapsed.has(mission.id)
          // Only compute agent grouping when section is expanded
          let agents: string[] = []
          let agentTasks = new Map<string, MissionTask[]>()
          if (isOpen) {
            for (const task of mission.tasks || []) {
              const key = task.agent_slug || "unassigned"
              if (!agentTasks.has(key)) agentTasks.set(key, [])
              agentTasks.get(key)!.push(task)
            }
            agents = Array.from(agentTasks.keys()).sort()
          }

          return (
            <div key={mission.id} className="rounded-lg border border-border bg-card overflow-hidden">
              {/* Mission header */}
              <button
                type="button"
                onClick={() => toggle(mission.id)}
                className="w-full flex items-center gap-2 px-3 py-2 bg-muted/50 hover:bg-muted/80 transition-colors border-b border-border"
              >
                <ChevronDown className={cn("h-3.5 w-3.5 text-muted-foreground transition-transform", !isOpen && "-rotate-90")} />
                <Badge variant="outline" className={cn("text-[10px] h-5", statusBadge[mission.status])}>
                  {mission.status.replace("_", " ")}
                </Badge>
                <span className="text-xs font-semibold truncate">{mission.title}</span>
                <span className="text-[11px] text-muted-foreground font-mono">@{mission.lead_agent_slug}</span>
                <span className="ml-auto text-[10px] text-muted-foreground">
                  {agents.length} agent{agents.length !== 1 ? "s" : ""} / {(mission.tasks || []).length} tasks
                </span>
              </button>

              {isOpen && (
                <div className="overflow-x-auto relative" style={{ minWidth: 600 }}>
                  {/* Time ruler */}
                  <div className="flex" style={{ minWidth: 900 }}>
                    <div className="shrink-0 border-r border-border border-b border-b-border bg-card" style={{ width: AGENT_W }} />
                    <div className="flex-1 relative h-[36px] border-b border-border bg-card">
                      {ticks.map((tick, i) => (
                        <div key={i} className="absolute top-0 flex flex-col items-start" style={{ left: `${tick.pct}%` }}>
                          <span className="text-[10px] font-mono text-muted-foreground mt-1 -translate-x-1/2 whitespace-nowrap">{tick.label}</span>
                          <div className="w-px h-1.5 bg-muted-foreground/40 mt-0.5" />
                        </div>
                      ))}
                    </div>
                  </div>

                  {/* Swim lanes */}
                  {agents.map((slug) => {
                    const tasks = agentTasks.get(slug)!

                    return (
                      <div
                        key={slug}
                        className={cn(
                          "flex",
                          highlightSlugs && !highlightSlugs.has(slug) && "opacity-20"
                        )}
                        style={{ minWidth: 900, transition: "opacity 0.3s ease" }}
                      >
                        {/* Agent info column */}
                        <div
                          className="shrink-0 sticky left-0 z-20 bg-card border-r border-border border-b border-b-border/50 flex items-center gap-2 px-2.5 hover:bg-muted/30 transition-colors"
                          style={{ width: AGENT_W, height: LANE_H }}
                        >
                          <AgentAvatar
                            seed={slug}
                            alt={slug}
                            className="w-[26px] h-[26px] rounded-full shrink-0"
                          />
                          <div className="min-w-0">
                            <div className="text-[11px] font-mono font-medium truncate text-foreground">@{slug}</div>
                            <div className="text-[10px] text-muted-foreground truncate">
                              {tasks.filter((t) => t.status === "IN_PROGRESS").length > 0 ? "active" : tasks.every((t) => t.status === "COMPLETED") ? "done" : "idle"}
                            </div>
                          </div>
                        </div>

                        {/* Track */}
                        <div className="flex-1 relative border-b border-border/50" style={{ height: LANE_H }}>
                          {/* Grid lines */}
                          {ticks.map((tick, i) => (
                            <div key={i} className="absolute top-0 bottom-0 w-px bg-border/30 pointer-events-none" style={{ left: `${tick.pct}%` }} />
                          ))}
                          {/* Now marker */}
                          {nowPct != null && (
                            <div className="absolute top-0 bottom-0 w-0.5 bg-rose-500 z-[15] pointer-events-none" style={{ left: `${nowPct}%` }}>
                              <div className="absolute -top-1 left-1/2 -translate-x-1/2 w-0 h-0 border-l-[4px] border-r-[4px] border-t-[5px] border-l-transparent border-r-transparent border-t-rose-500" />
                            </div>
                          )}
                          {/* Task bars */}
                          {tasks.sort((a, b) => a.task_order - b.task_order).map((task) => (
                            <TaskBar key={task.id} task={task} timeRange={timeRange} />
                          ))}
                        </div>
                      </div>
                    )
                  })}
                </div>
              )}
            </div>
          )
        })}
      </div>
    </>
  )
}
