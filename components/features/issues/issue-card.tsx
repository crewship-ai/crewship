"use client"

import { Card } from "@/components/ui/card"
import { PriorityIcon } from "./priority-icon"
import { LabelBadge } from "./label-badge"
import { Clock, FolderKanban } from "lucide-react"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import type { Mission } from "@/lib/types/mission"

function isOverdue(dueDate: string | null | undefined, status: string): boolean {
  if (!dueDate || status === "COMPLETED" || status === "DONE" || status === "CANCELLED") return false
  return new Date(dueDate) < new Date()
}

function formatShortDate(dateStr: string): string {
  const d = new Date(dateStr)
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" })
}

interface IssueCardProps {
  issue: Mission
  onClick: () => void
}

export function IssueCard({ issue, onClick }: IssueCardProps) {
  const overdue = isOverdue(issue.due_date, issue.status)

  return (
    <Card
      className={cn(
        "px-3 py-2.5 cursor-pointer hover:bg-accent/50 transition-colors border-border/60",
        overdue && "border-red-500/40"
      )}
      onClick={onClick}
    >
      {/* Top: identifier + priority + overdue */}
      <div className="flex items-center justify-between gap-2 mb-1">
        <div className="flex items-center gap-1.5">
          {issue.identifier && (
            <span className="text-[11px] font-mono text-muted-foreground">
              {issue.identifier}
            </span>
          )}
          {overdue && (
            <Clock className="h-3 w-3 text-red-500" />
          )}
        </div>
        <PriorityIcon
          priority={issue.priority || "none"}
          className="h-3.5 w-3.5 shrink-0"
        />
      </div>

      {/* Title — full, wrapping */}
      <p className="text-sm font-medium leading-snug text-foreground mb-2">
        {issue.title}
      </p>

      {/* Assignee */}
      {issue.assignee_name && (
        <div className="flex items-center gap-1.5 mb-2">
          <img
            src={getAgentAvatarUrl(issue.assignee_id || issue.assignee_name)}
            alt=""
            className="h-[18px] w-[18px] rounded-full shrink-0"
          />
          <span className="text-xs text-muted-foreground truncate">
            {issue.assignee_name}
          </span>
        </div>
      )}

      {/* Project */}
      {issue.project_name && (
        <div className="flex items-center gap-1.5 mb-2">
          <FolderKanban className="h-3 w-3 text-muted-foreground/50 shrink-0" />
          <span className="text-[11px] text-muted-foreground truncate">
            {issue.project_name}
          </span>
        </div>
      )}

      {/* Labels — as proper badges, max 3 */}
      {issue.labels && issue.labels.length > 0 && (
        <div className="flex items-center gap-1 flex-wrap mb-2">
          {issue.labels.slice(0, 3).map((label) => (
            <LabelBadge key={label.id} label={label} />
          ))}
          {issue.labels.length > 3 && (
            <span className="text-[10px] text-muted-foreground/50">
              +{issue.labels.length - 3}
            </span>
          )}
        </div>
      )}

      {/* Created date */}
      <div className="text-[10px] text-muted-foreground/40">
        Created {formatShortDate(issue.created_at)}
      </div>
    </Card>
  )
}
