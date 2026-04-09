"use client"

import { cn } from "@/lib/utils"
import { timeAgo } from "@/lib/time"
import type { IssueActivity } from "@/lib/types/mission"

export function ActivityIcon({ action }: { action: string }) {
  const size = "h-3.5 w-3.5 shrink-0 mt-0.5"
  switch (action) {
    case "task_completed":
    case "review_approved":
      return <svg className={cn(size, "text-green-500")} viewBox="0 0 16 16"><polygon points="8,2 10,6 14,6 11,9 12,14 8,11 4,14 5,9 2,6 6,6" fill="currentColor"/></svg>
    case "task_failed":
      return <svg className={cn(size, "text-red-500")} viewBox="0 0 16 16"><circle cx="8" cy="8" r="6" fill="currentColor"/><path d="M5.5 5.5l5 5M10.5 5.5l-5 5" stroke="white" strokeWidth="1.5" strokeLinecap="round"/></svg>
    case "status_changed":
      return <svg className={cn(size, "text-amber-500")} viewBox="0 0 16 16"><circle cx="8" cy="8" r="5.5" stroke="currentColor" strokeWidth="1.5" fill="none"/><path d="M8 2.5A5.5 5.5 0 0 0 8 13.5V2.5z" fill="currentColor"/></svg>
    case "assignee_changed":
      return <svg className={cn(size, "text-green-500")} viewBox="0 0 16 16"><polygon points="8,2 10,6 14,6 11,9 12,14 8,11 4,14 5,9 2,6 6,6" fill="currentColor"/></svg>
    case "review_changes_requested":
      return <svg className={cn(size, "text-red-500")} viewBox="0 0 16 16"><circle cx="8" cy="8" r="6" fill="currentColor"/><path d="M6 6h4M6 10h4" stroke="white" strokeWidth="1.5" strokeLinecap="round"/></svg>
    case "priority_changed":
      return <svg className={cn(size, "text-blue-400")} viewBox="0 0 16 16"><rect x="1.5" y="8" width="3" height="6" rx="1" fill="currentColor"/><rect x="6.5" y="5" width="3" height="9" rx="1" fill="currentColor"/><rect x="11.5" y="2" width="3" height="12" rx="1" fill="currentColor"/></svg>
    default:
      return <div className={cn("w-3.5 h-3.5 rounded-full bg-muted-foreground/20 shrink-0 mt-0.5")} />
  }
}

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

export function ActivityFeed({ activities }: { activities: IssueActivity[] }) {
  return (
    <div className="border-t border-white/[0.06] pt-3 px-4 pb-4">
      <div className="flex items-center justify-between mb-3">
        <span className="text-[11px] font-semibold text-foreground/80">Activity</span>
      </div>
      <div className="space-y-0">
        {activities.length === 0 ? (
          <p className="text-[11px] text-muted-foreground/40">No activity yet</p>
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
                    <span className="text-muted-foreground/50"> {a.details}</span>
                  )}
                </span>
                <span className="text-[10px] text-muted-foreground/30 ml-1.5">{timeAgo(a.created_at)}</span>
              </div>
            </div>
          ))
        )}
      </div>
    </div>
  )
}
