"use client"

import { X, Clock, User, AlertCircle, CheckCircle, Repeat } from "lucide-react"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Separator } from "@/components/ui/separator"
import { TaskStatusBadge } from "@/components/features/missions/mission-status-badge"
import type { MissionTask } from "@/lib/types/mission"

interface TaskDetailPanelProps {
  task: MissionTask
  onClose: () => void
}

function formatDuration(startedAt: string | null, completedAt: string | null, durationMs: number | null): string {
  if (durationMs != null) {
    const s = Math.floor(durationMs / 1000)
    if (s < 60) return `${s}s`
    return `${Math.floor(s / 60)}m ${s % 60}s`
  }
  if (!startedAt) return "—"
  const start = new Date(startedAt).getTime()
  const end = completedAt ? new Date(completedAt).getTime() : Date.now()
  const diff = Math.floor((end - start) / 1000)
  if (diff < 60) return `${diff}s`
  return `${Math.floor(diff / 60)}m ${diff % 60}s`
}

export function TaskDetailPanel({ task, onClose }: TaskDetailPanelProps) {
  const deps = (() => {
    try {
      return JSON.parse(task.depends_on || "[]") as string[]
    } catch {
      return []
    }
  })()

  return (
    <Card className="w-80 shrink-0">
      <CardHeader className="pb-3">
        <div className="flex items-start justify-between">
          <CardTitle className="text-sm font-semibold leading-tight pr-2">{task.title}</CardTitle>
          <Button variant="ghost" size="icon" className="h-6 w-6 shrink-0" onClick={onClose}>
            <X className="h-4 w-4" />
          </Button>
        </div>
      </CardHeader>
      <CardContent className="space-y-4 text-sm">
        <div className="flex items-center gap-2">
          <TaskStatusBadge status={task.status} />
          {task.max_iterations && task.max_iterations > 1 && (
            <Badge variant="outline" className="text-xs gap-1">
              <Repeat className="h-3 w-3" />
              {task.iteration || 1}/{task.max_iterations}
            </Badge>
          )}
        </div>

        {task.description && (
          <>
            <Separator />
            <p className="text-muted-foreground">{task.description}</p>
          </>
        )}

        <Separator />

        <div className="space-y-2">
          <div className="flex items-center gap-2 text-muted-foreground">
            <User className="h-3.5 w-3.5" />
            <span>{task.agent_name || "Unassigned"}</span>
            {task.agent_slug && <span className="text-xs">(@{task.agent_slug})</span>}
          </div>

          <div className="flex items-center gap-2 text-muted-foreground">
            <Clock className="h-3.5 w-3.5" />
            <span>{formatDuration(task.started_at, task.completed_at, task.duration_ms)}</span>
          </div>

          {deps.length > 0 && (
            <div className="text-muted-foreground">
              <span className="text-xs font-medium">Depends on:</span>{" "}
              <span className="text-xs">{deps.length} task{deps.length > 1 ? "s" : ""}</span>
            </div>
          )}
        </div>

        {task.result_summary && (
          <>
            <Separator />
            <div className="flex items-start gap-2">
              <CheckCircle className="h-3.5 w-3.5 text-green-500 mt-0.5 shrink-0" />
              <p className="text-xs text-muted-foreground line-clamp-4">{task.result_summary}</p>
            </div>
          </>
        )}

        {task.error_message && (
          <>
            <Separator />
            <div className="flex items-start gap-2">
              <AlertCircle className="h-3.5 w-3.5 text-red-500 mt-0.5 shrink-0" />
              <p className="text-xs text-red-600 dark:text-red-400 line-clamp-4">{task.error_message}</p>
            </div>
          </>
        )}

        {task.token_count != null && (
          <div className="text-xs text-muted-foreground">
            Tokens: {task.token_count.toLocaleString()}
            {task.estimated_cost != null && ` ($${task.estimated_cost.toFixed(4)})`}
          </div>
        )}
      </CardContent>
    </Card>
  )
}
