"use client"

import { Card } from "@/components/ui/card"
import { StatusIcon } from "./status-icon"
import { PriorityIcon } from "./priority-icon"
import { LabelBadge } from "./label-badge"
import { Clock } from "lucide-react"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { getCrewIconDef, getCrewDotColor } from "@/lib/crew-icon"
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

  // Show "Updated" if updated_at is different from created_at, otherwise "Created"
  const isUpdated = issue.updated_at && issue.updated_at !== issue.created_at
  const dateLabel = isUpdated ? "Updated" : "Created"
  const dateValue = isUpdated ? issue.updated_at! : issue.created_at

  return (
    <Card
      className={cn(
        "px-3 py-2 cursor-pointer hover:bg-accent/50 transition-colors border-border/60",
        overdue && "border-red-500/40"
      )}
      onClick={onClick}
    >
      {/* Row 1: identifier + assignee avatar */}
      <div className="flex items-center justify-between gap-2 mb-0.5">
        <div className="flex items-center gap-1.5">
          {issue.identifier && (
            <span className="text-[11px] font-mono text-muted-foreground/60">
              {issue.identifier}
            </span>
          )}
          {overdue && <Clock className="h-3 w-3 text-red-500" />}
        </div>
        {issue.assignee_id && (
          <img
            src={getAgentAvatarUrl(issue.assignee_id)}
            alt={issue.assignee_name || ""}
            title={issue.assignee_name || ""}
            className="h-5 w-5 rounded-full shrink-0"
          />
        )}
      </div>

      {/* Row 2: status icon + title */}
      <div className="flex gap-1.5 mb-1.5">
        <StatusIcon status={issue.status} className="h-4 w-4 shrink-0 mt-0.5" />
        <p className="text-[13px] font-medium leading-snug text-foreground">
          {issue.title}
        </p>
      </div>

      {/* Row 3: priority + project badge + label badges */}
      <div className="flex items-center gap-1.5 flex-wrap mb-1.5">
        <PriorityIcon priority={issue.priority || "none"} className="h-3.5 w-3.5 shrink-0" />

        {issue.project_name && (
          <ProjectBadge name={issue.project_name} />
        )}

        {issue.labels && issue.labels.length > 0 && (
          <>
            {issue.labels.slice(0, 3).map((label) => (
              <LabelBadge key={label.id} label={label} />
            ))}
            {issue.labels.length > 3 && (
              <span className="text-[10px] text-muted-foreground/50">+{issue.labels.length - 3}</span>
            )}
          </>
        )}
      </div>

      {/* Row 4: date */}
      <div className="text-[10px] text-muted-foreground/40">
        {dateLabel} {formatShortDate(dateValue)}
      </div>
    </Card>
  )
}

/** Small project badge matching Linear style */
function ProjectBadge({ name }: { name: string }) {
  return (
    <span className="inline-flex items-center gap-1 rounded-full bg-white/[0.06] border border-white/[0.08] px-1.5 py-0.5 text-[10px] text-muted-foreground max-w-[140px]">
      <span className="truncate">{name}</span>
    </span>
  )
}
