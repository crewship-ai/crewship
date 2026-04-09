"use client"

import { Card } from "@/components/ui/card"
import { StatusIcon } from "./status-icon"
import { PriorityIcon } from "./priority-icon"
import { LabelBadge } from "./label-badge"
import { Clock } from "lucide-react"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import type { Mission } from "@/lib/types/mission"

function isOverdue(dueDate: string | null | undefined, status: string): boolean {
  const TERMINAL_STATUSES = new Set(["COMPLETED", "DONE", "CANCELLED", "FAILED", "DUPLICATE"])
  if (!dueDate || TERMINAL_STATUSES.has(status)) return false
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
  const isUpdated = issue.updated_at && issue.updated_at !== issue.created_at
  const dateLabel = isUpdated ? "Updated" : "Created"
  const dateValue = isUpdated ? issue.updated_at! : issue.created_at

  return (
    <Card
      role="button"
      tabIndex={0}
      aria-label={`Issue ${issue.identifier || ""}: ${issue.title}`}
      className={cn(
        "px-2.5 py-2 cursor-pointer hover:bg-accent/50 transition-colors border-border/60 gap-0",
        overdue && "border-red-500/40"
      )}
      onClick={onClick}
      onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onClick() } }}
    >
      {/* Row 1: identifier + agent name + avatar */}
      <div className="flex items-center justify-between gap-2 mb-0.5">
        <div className="flex items-center gap-1">
          {issue.identifier && (
            <span className="text-[10px] font-mono text-foreground/50">{issue.identifier}</span>
          )}
          {overdue && <Clock className="h-2.5 w-2.5 text-red-500" />}
        </div>
        {issue.assignee_id && (
          <div className="flex items-center gap-1 shrink-0">
            <span className="text-[10px] text-foreground/50 truncate max-w-[80px]">{issue.assignee_name}</span>
            <img
              src={getAgentAvatarUrl(issue.assignee_id)}
              alt={issue.assignee_name || ""}
              title={issue.assignee_name || ""}
              className="h-4.5 w-4.5 rounded-full"
            />
          </div>
        )}
      </div>

      {/* Row 2: status icon + title */}
      <div className="flex gap-1.5 mb-1">
        <StatusIcon status={issue.status} className="h-3.5 w-3.5 shrink-0 mt-[1px]" />
        <p className="text-[12.5px] font-medium leading-[1.35] text-foreground">{issue.title}</p>
      </div>

      {/* Row 3: priority + label badges */}
      {(issue.priority !== "none" || (issue.labels && issue.labels.length > 0)) && (
        <div className="flex items-center gap-1 flex-wrap mb-1">
          <PriorityIcon priority={issue.priority || "none"} className="h-3.5 w-3.5 shrink-0" />
          {issue.labels && issue.labels.slice(0, 3).map((label) => (
            <LabelBadge key={label.id} label={label} />
          ))}
          {issue.labels && issue.labels.length > 3 && (
            <span className="text-[9px] text-foreground/40">+{issue.labels.length - 3}</span>
          )}
        </div>
      )}

      {/* Row 4: date */}
      <div className="text-[10px] text-foreground/40">
        {dateLabel} {formatShortDate(dateValue)}
      </div>
    </Card>
  )
}
