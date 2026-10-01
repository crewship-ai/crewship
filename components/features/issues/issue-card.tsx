"use client"

import { memo } from "react"
import { Card } from "@/components/ui/card"
import { StatusIcon } from "./status-icon"
import { PriorityIcon } from "./priority-icon"
import { LabelBadge } from "./label-badge"
import { Clock, UserRound } from "lucide-react"
import { formatShortDate, timeAgo } from "@/lib/time"
import { cn } from "@/lib/utils"
import { getIssueWorker } from "@/lib/issue-execution"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import type { Mission } from "@/lib/types/mission"

const TERMINAL_STATUSES = new Set(["COMPLETED", "DONE", "CANCELLED", "FAILED", "DUPLICATE"])

function isOverdue(dueDate: string | null | undefined, status: string): boolean {
  if (!dueDate || TERMINAL_STATUSES.has(status)) return false
  return new Date(dueDate) < new Date()
}

interface IssueCardProps {
  issue: Mission
  onClick: () => void
}

export const IssueCard = memo(function IssueCard({ issue, onClick }: IssueCardProps) {
  // Current work wins over the legacy owner/delegate projection on the board.
  const worker = getIssueWorker(issue)
  issue = { ...issue, assignee_type: worker.isHuman ? "user" : "agent", assignee_id: worker.id, assignee_name: worker.name }
  const overdue = isOverdue(issue.due_date, issue.status)
  const isUpdated = issue.updated_at && issue.updated_at !== issue.created_at
  const dateLabel = isUpdated ? "Updated" : "Created"
  const dateValue = isUpdated ? issue.updated_at! : issue.created_at

  return (
    <Card
      role="button"
      tabIndex={0}
      data-slot="issue-card"
      aria-label={`Issue ${issue.identifier || ""}: ${issue.title}`}
      className={cn(
        // Harbor board card: 16px radius, hairline border, no resting
        // shadow; the lift on hover is the only depth.
        "card-interactive card-hover gap-0 rounded-2xl px-3 py-2.5 shadow-none",
        overdue && "border-destructive/40",
        issue.status === "IN_PROGRESS" && "agent-active-card",
      )}
      onClick={onClick}
      onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onClick() } }}
    >
      {/* Row 1: the machine line — status, id, priority, when; the worker on the right. */}
      <div className="mb-1 flex items-center gap-1.5">
        <StatusIcon status={issue.status} className="h-3.5 w-3.5 shrink-0" />
        {issue.identifier && (
          <span className="font-mono text-[11px] text-muted-foreground">{issue.identifier}</span>
        )}
        <PriorityIcon priority={issue.priority || "none"} className="h-3.5 w-3.5 shrink-0" />
        {overdue && <Clock aria-label="Overdue" className="h-3 w-3 shrink-0 text-destructive" />}
        <time
          dateTime={dateValue}
          title={`${dateLabel} ${formatShortDate(dateValue)}`}
          className="ml-auto truncate font-mono text-[11px] text-muted-foreground-soft"
        >
          {timeAgo(dateValue)}
        </time>
        {issue.assignee_id && (
          <div className="relative shrink-0">
            {issue.assignee_type === "user" ? (
              // Human assignee: neutral user glyph — the DiceBear agent
              // avatar would misrepresent a person as an agent.
              <div
                data-testid="assignee-avatar-user"
                title={issue.assignee_name || ""}
                className="flex h-5 w-5 items-center justify-center rounded-full bg-muted"
              >
                <UserRound aria-label={issue.assignee_name || "User assignee"} className="h-3 w-3 text-foreground/60" />
              </div>
            ) : (
              <AgentAvatar
                data-testid="assignee-avatar-agent"
                seed={issue.assignee_id}
                alt={issue.assignee_name || ""}
                title={issue.assignee_name || ""}
                className="h-5 w-5 rounded-full"
              />
            )}
            {issue.status === "IN_PROGRESS" && (
              <span className="absolute -bottom-0.5 -right-0.5 h-2 w-2 rounded-full bg-success ring-1 ring-card agent-active-dot" />
            )}
          </div>
        )}
      </div>

      {/* Row 2: the title, in the UI font, at most three lines. */}
      <p className="line-clamp-3 text-[13px] font-medium leading-[1.4] text-foreground">{issue.title}</p>

      {/* Row 3, only when there is something to say: labels, a person working it. */}
      {((issue.labels && issue.labels.length > 0) || issue.work_mode === "human") && (
        <div className="mt-1.5 flex flex-wrap items-center gap-1">
          {issue.labels && issue.labels.slice(0, 3).map((label) => (
            <LabelBadge key={label.id} label={label} />
          ))}
          {issue.labels && issue.labels.length > 3 && (
            <span className="font-mono text-[10px] text-muted-foreground-soft">+{issue.labels.length - 3}</span>
          )}
          {issue.work_mode === "human" && (
            <span className="text-[11px] text-warn">{issue.work_stopping ? "Taking over…" : "With a person"}</span>
          )}
        </div>
      )}
    </Card>
  )
})
