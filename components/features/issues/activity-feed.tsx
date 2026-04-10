"use client"

import {
  Star,
  CircleX,
  PieChart,
  UserRoundCheck,
  MessageSquareWarning,
  ChartColumn,
  Circle,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { timeAgo } from "@/lib/time"
import type { IssueActivity } from "@/lib/types/mission"

/** Renders a lucide icon representing the given activity action type. */
export function ActivityIcon({ action }: { action: string }) {
  const size = "h-3.5 w-3.5 shrink-0 mt-0.5"
  switch (action) {
    case "task_completed":
    case "review_approved":
      return <Star className={cn(size, "text-green-500")} />
    case "task_failed":
      return <CircleX className={cn(size, "text-red-500")} />
    case "status_changed":
      return <PieChart className={cn(size, "text-amber-500")} />
    case "assignee_changed":
      return <UserRoundCheck className={cn(size, "text-green-500")} />
    case "review_changes_requested":
      return <MessageSquareWarning className={cn(size, "text-red-500")} />
    case "priority_changed":
      return <ChartColumn className={cn(size, "text-blue-400")} />
    default:
      return <Circle className={cn(size, "text-muted-foreground/40")} />
  }
}

/** Maps an activity action key to a human-readable label string. */
export function actionLabel(action: string): string {
  switch (action) {
    case "task_completed": return "completed a task"
    case "task_failed": return "task failed"
    case "status_changed": return "changed status"
    case "assignee_changed": return "changed assignee"
    case "priority_changed": return "changed priority"
    case "review_approved": return "approved"
    case "review_changes_requested": return "requested changes"
    case "issue_started": return "started the issue"
    case "issue_stopped": return "stopped the issue"
    default: return action.replace(/_/g, " ")
  }
}

/** Chronological activity timeline with connector lines and actor labels. */
export function ActivityFeed({ activities }: { activities: IssueActivity[] }) {
  return (
    <div className="border-t border-white/[0.06] pt-3 px-4 pb-4">
      <div className="flex items-center justify-between mb-3">
        <span className="text-[11px] font-semibold text-foreground/80">Activity</span>
      </div>
      <div className="space-y-0">
        {activities.length === 0 ? (
          <p className="text-[11px] text-foreground/40">No activity yet</p>
        ) : (
          activities.map((a, i) => (
            <div key={a.id} className="flex items-start gap-2.5 py-1.5 relative">
              {i < activities.length - 1 && (
                <div className="absolute left-[7px] top-[22px] w-px h-[calc(100%-6px)] bg-white/[0.06]" />
              )}
              <ActivityIcon action={a.action} />
              <div className="flex-1 min-w-0">
                <span className="text-[11px] text-foreground/70">
                  <span className="text-foreground/90 font-medium">{a.actor_name || a.actor_id}</span>
                  {" "}
                  {actionLabel(a.action)}
                  {a.details && a.action === "status_changed" && (
                    <span className="text-foreground/50"> {a.details}</span>
                  )}
                </span>
                <span className="text-[10px] text-foreground/35 ml-1.5">{timeAgo(a.created_at)}</span>
              </div>
            </div>
          ))
        )}
      </div>
    </div>
  )
}
