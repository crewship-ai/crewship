"use client"

import { useMemo } from "react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { EmptyState } from "@/components/layout/empty-state"
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { Clock } from "lucide-react"
import { cn } from "@/lib/utils"
import type { Mission, MissionTask } from "@/lib/types/mission"

interface MissionTimelineProps {
  missions: Mission[]
}

const statusColors: Record<string, string> = {
  COMPLETED: "bg-green-500",
  IN_PROGRESS: "bg-blue-500 animate-pulse",
  FAILED: "bg-red-500",
  BLOCKED: "bg-amber-500",
  PENDING: "bg-slate-300 dark:bg-slate-600",
  SKIPPED: "bg-gray-400",
  PLANNING: "bg-slate-400",
  REVIEW: "bg-purple-500",
  CANCELLED: "bg-gray-500",
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

  // Add 5% padding on each side
  const range = end - start || 3600000
  return { start: start - range * 0.05, end: end + range * 0.05 }
}

function formatTimeLabel(ts: number): string {
  return new Date(ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
}

function TaskBar({ task, timeRange }: { task: MissionTask; timeRange: { start: number; end: number } }) {
  const range = timeRange.end - timeRange.start
  const taskStart = task.started_at
    ? new Date(task.started_at).getTime()
    : new Date(task.created_at).getTime()
  const taskEnd = task.completed_at
    ? new Date(task.completed_at).getTime()
    : task.status === "IN_PROGRESS"
      ? Date.now()
      : taskStart + range * 0.02

  const left = Math.max(0, ((taskStart - timeRange.start) / range) * 100)
  const width = Math.max(1, ((taskEnd - taskStart) / range) * 100)

  const color = statusColors[task.status] || statusColors.PENDING

  return (
    <TooltipProvider delayDuration={100}>
      <Tooltip>
        <TooltipTrigger asChild>
          <div
            className={cn("absolute top-1 h-6 rounded-sm cursor-pointer transition-opacity hover:opacity-80", color)}
            style={{ left: `${left}%`, width: `${width}%`, minWidth: "4px" }}
          />
        </TooltipTrigger>
        <TooltipContent side="top" className="max-w-xs">
          <div className="space-y-1">
            <div className="font-medium text-xs">{task.title}</div>
            <div className="text-xs text-muted-foreground">
              @{task.agent_slug || "unassigned"} — {task.status}
            </div>
            {task.started_at && (
              <div className="text-xs text-muted-foreground">
                Started: {new Date(task.started_at).toLocaleTimeString()}
              </div>
            )}
            {task.completed_at && (
              <div className="text-xs text-muted-foreground">
                Completed: {new Date(task.completed_at).toLocaleTimeString()}
              </div>
            )}
          </div>
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

export function MissionTimeline({ missions }: MissionTimelineProps) {
  const activeMissions = useMemo(() => {
    const active = missions.filter((m) => (m.tasks?.length ?? 0) > 0)
    return active.sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()).slice(0, 10)
  }, [missions])

  if (activeMissions.length === 0) {
    return (
      <Card>
        <CardContent className="py-12">
          <EmptyState
            icon={Clock}
            title="No timeline data"
            description="Missions with tasks will appear here as a Gantt timeline"
          />
        </CardContent>
      </Card>
    )
  }

  const timeRange = getTimeRange(activeMissions)
  const tickCount = 6
  const ticks = Array.from({ length: tickCount }, (_, i) => {
    const t = timeRange.start + (i / (tickCount - 1)) * (timeRange.end - timeRange.start)
    return { time: t, label: formatTimeLabel(t) }
  })

  return (
    <Card>
      <CardContent className="py-4 space-y-1 overflow-x-auto">
        {/* Time axis */}
        <div className="flex justify-between text-[10px] text-muted-foreground px-1 mb-2 min-w-[600px]" style={{ marginLeft: "180px" }}>
          {ticks.map((tick, i) => (
            <span key={i}>{tick.label}</span>
          ))}
        </div>

        {activeMissions.map((mission) => (
          <div key={mission.id} className="mb-4">
            <div className="flex items-center gap-2 mb-1">
              <Badge
                variant="outline"
                className={cn(
                  "text-[10px]",
                  mission.status === "IN_PROGRESS" && "border-blue-500 text-blue-500",
                  mission.status === "COMPLETED" && "border-green-500 text-green-500",
                  mission.status === "FAILED" && "border-red-500 text-red-500"
                )}
              >
                {mission.status}
              </Badge>
              <span className="text-xs font-medium truncate max-w-[200px]">{mission.title}</span>
              <span className="text-[10px] text-muted-foreground">
                @{mission.lead_agent_slug}
              </span>
            </div>

            {(mission.tasks || [])
              .sort((a, b) => a.task_order - b.task_order)
              .map((task) => (
                <div key={task.id} className="flex items-center min-w-[600px]">
                  <div className="w-[180px] shrink-0 pr-2 text-right">
                    <span className="text-xs text-muted-foreground truncate inline-block max-w-[170px]">
                      {task.title}
                    </span>
                  </div>
                  <div className="flex-1 relative h-8 bg-muted/30 rounded-sm">
                    <TaskBar task={task} timeRange={timeRange} />
                  </div>
                </div>
              ))}
          </div>
        ))}
      </CardContent>
    </Card>
  )
}
