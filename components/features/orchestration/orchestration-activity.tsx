"use client"

import { useMemo } from "react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { EmptyState } from "@/components/layout/empty-state"
import { Activity, CheckCircle, XCircle, Play, Pause, AlertTriangle } from "lucide-react"
import { cn } from "@/lib/utils"
import type { Mission } from "@/lib/types/mission"

interface OrchestrationActivityProps {
  missions: Mission[]
}

interface ActivityEvent {
  id: string
  timestamp: Date
  type: "mission" | "task"
  status: string
  title: string
  subtitle: string
  missionId: string
}

function getStatusIcon(status: string) {
  switch (status) {
    case "COMPLETED":
      return <CheckCircle className="h-4 w-4 text-green-500" />
    case "FAILED":
      return <XCircle className="h-4 w-4 text-red-500" />
    case "IN_PROGRESS":
      return <Play className="h-4 w-4 text-blue-500" />
    case "BLOCKED":
      return <Pause className="h-4 w-4 text-amber-500" />
    case "CANCELLED":
      return <XCircle className="h-4 w-4 text-gray-500" />
    default:
      return <AlertTriangle className="h-4 w-4 text-muted-foreground" />
  }
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
      missionId: mission.id,
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
        missionId: mission.id,
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

export function OrchestrationActivity({ missions }: OrchestrationActivityProps) {
  const events = useMemo(() => buildActivityFeed(missions), [missions])

  if (events.length === 0) {
    return (
      <Card>
        <CardContent className="py-12">
          <EmptyState
            icon={Activity}
            title="No activity yet"
            description="Task status changes and mission events will appear here in real-time"
          />
        </CardContent>
      </Card>
    )
  }

  return (
    <Card>
      <CardContent className="py-4">
        <div className="space-y-0">
          {events.slice(0, 100).map((event, idx) => (
            <div
              key={event.id}
              className={cn(
                "flex items-start gap-3 py-3 px-2 rounded-md transition-colors hover:bg-muted/50",
                idx < events.length - 1 && "border-b border-border/50"
              )}
            >
              <div className="mt-0.5 shrink-0">{getStatusIcon(event.status)}</div>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium truncate">{event.title}</span>
                  <Badge
                    variant={event.type === "mission" ? "default" : "outline"}
                    className="text-[10px] px-1.5 py-0 shrink-0"
                  >
                    {event.type}
                  </Badge>
                </div>
                <div className="text-xs text-muted-foreground mt-0.5">{event.subtitle}</div>
              </div>
              <div className="text-[11px] text-muted-foreground shrink-0 whitespace-nowrap mt-0.5">
                {timeAgo(event.timestamp)}
              </div>
            </div>
          ))}
        </div>
      </CardContent>
    </Card>
  )
}
