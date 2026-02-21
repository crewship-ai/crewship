"use client"

import { Fragment, useState } from "react"
import { Card, CardContent } from "@/components/ui/card"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { TaskStatusBadge } from "./mission-status-badge"
import { formatCost } from "@/lib/utils/format"
import type { MissionTask, TaskStats } from "@/lib/types/mission"

interface MissionBoardProps {
  tasks: MissionTask[]
  taskStats: TaskStats | null
}

function formatDuration(startedAt: string | null, completedAt: string | null, durationMs: number | null): string {
  if (durationMs != null) {
    const seconds = Math.floor(durationMs / 1000)
    if (seconds < 60) return `${seconds}s`
    const minutes = Math.floor(seconds / 60)
    return `${minutes}m ${seconds % 60}s`
  }
  if (!startedAt) return "—"
  const start = new Date(startedAt).getTime()
  const end = completedAt ? new Date(completedAt).getTime() : Date.now()
  const diffMs = end - start
  const seconds = Math.floor(diffMs / 1000)
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  return `${minutes}m ${seconds % 60}s`
}

function formatTime(dateStr: string | null): string {
  if (!dateStr) return "—"
  return new Date(dateStr).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
}

export function MissionBoard({ tasks, taskStats }: MissionBoardProps) {
  const [expandedId, setExpandedId] = useState<string | null>(null)

  if (tasks.length === 0) {
    return (
      <div className="flex flex-col items-center gap-3 py-8 text-center">
        <p className="text-sm text-muted-foreground">No tasks defined yet.</p>
        <p className="text-xs text-muted-foreground/70">
          Tasks will appear here when the lead agent plans the mission.
        </p>
      </div>
    )
  }

  return (
    <div className="space-y-3">
      <Card>
        <CardContent className="p-0">
          <TooltipProvider>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-12">#</TableHead>
                  <TableHead>Task</TableHead>
                  <TableHead className="w-28">Agent</TableHead>
                  <TableHead className="w-28">Status</TableHead>
                  <TableHead className="w-20">Started</TableHead>
                  <TableHead className="w-20">Duration</TableHead>
                  <TableHead className="w-20">Cost</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {tasks.map((task) => {
                  const isExpanded = expandedId === task.id
                  const hasDetail = task.result_summary || task.error_message

                  return (
                    <Fragment key={task.id}>
                      <TableRow
                        className={hasDetail ? "cursor-pointer" : ""}
                        role={hasDetail ? "button" : undefined}
                        aria-expanded={hasDetail ? isExpanded : undefined}
                        tabIndex={hasDetail ? 0 : -1}
                        onClick={() => {
                          if (hasDetail) setExpandedId(isExpanded ? null : task.id)
                        }}
                        onKeyDown={(e) => {
                          if (!hasDetail) return
                          if (e.key === "Enter" || e.key === " ") {
                            e.preventDefault()
                            setExpandedId(isExpanded ? null : task.id)
                          }
                        }}
                      >
                        <TableCell className="text-xs text-muted-foreground font-mono">
                          {task.task_order + 1}
                        </TableCell>
                        <TableCell>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <span className="text-sm line-clamp-1">{task.title}</span>
                            </TooltipTrigger>
                            <TooltipContent className="max-w-sm">
                              <p className="whitespace-pre-wrap">{task.description ?? task.title}</p>
                            </TooltipContent>
                          </Tooltip>
                        </TableCell>
                        <TableCell className="text-sm text-muted-foreground">
                          {task.agent_slug ? `@${task.agent_slug}` : "—"}
                        </TableCell>
                        <TableCell>
                          <TaskStatusBadge status={task.status} />
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {formatTime(task.started_at)}
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {formatDuration(task.started_at, task.completed_at, task.duration_ms)}
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {formatCost(task.estimated_cost)}
                        </TableCell>
                      </TableRow>
                      {isExpanded && hasDetail && (
                        <TableRow>
                          <TableCell colSpan={7} className="bg-muted/30">
                            <div className="text-sm whitespace-pre-wrap max-h-60 overflow-y-auto p-2">
                              {task.error_message && (
                                <div className="text-destructive mb-2">
                                  <span className="font-medium">Error: </span>
                                  {task.error_message}
                                </div>
                              )}
                              {task.result_summary && (
                                <div>
                                  <span className="font-medium text-muted-foreground">Result: </span>
                                  {task.result_summary}
                                </div>
                              )}
                            </div>
                          </TableCell>
                        </TableRow>
                      )}
                    </Fragment>
                  )
                })}
              </TableBody>
            </Table>
          </TooltipProvider>
        </CardContent>
      </Card>

      {taskStats && (
        <div className="flex items-center gap-4 text-xs text-muted-foreground">
          <span>{taskStats.total} total</span>
          <span className="text-emerald-600">{taskStats.completed} completed</span>
          {taskStats.in_progress > 0 && (
            <span className="text-blue-600">{taskStats.in_progress} working</span>
          )}
          {taskStats.blocked > 0 && (
            <span className="text-orange-600">{taskStats.blocked} blocked</span>
          )}
          {taskStats.failed > 0 && (
            <span className="text-red-600">{taskStats.failed} failed</span>
          )}
        </div>
      )}
    </div>
  )
}
