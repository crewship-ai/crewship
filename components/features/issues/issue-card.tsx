"use client"

import { memo } from "react"
import { Bot, ListChecks, UserRound } from "lucide-react"

import { cn } from "@/lib/utils"
import { getIssueWorker } from "@/lib/issue-execution"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import type { Mission } from "@/lib/types/mission"
import { PriorityIcon } from "./priority-icon"
import { IssueLabelChip } from "./issue-row"
import { dueLabel } from "./issues-view-model"

interface IssueCardProps {
  issue: Mission
  onClick: () => void
  /** Board drag source. */
  draggable?: boolean
  onDragStart?: (e: React.DragEvent) => void
  active?: boolean
}

/**
 * A board card: the ID and who does the work on top, the title, then
 * priority, sub-issues and labels. The person or agent holding the work wins
 * over the legacy assignee.
 */
export const IssueCard = memo(function IssueCard({ issue, onClick, draggable = false, onDragStart, active = false }: IssueCardProps) {
  const worker = getIssueWorker(issue)
  const subs = issue.sub_issues_count ?? 0
  const due = dueLabel(issue.due_date, issue.status)
  return (
    <button
      type="button"
      data-slot="issue-card"
      aria-label={`Issue ${issue.identifier || ""}: ${issue.title}`}
      aria-current={active || undefined}
      draggable={draggable}
      onDragStart={onDragStart}
      onClick={onClick}
      className={cn(
        "grid w-full gap-1.5 rounded-card border border-border bg-card px-3 py-2.5 text-left transition-colors hover:border-line-strong focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        active && "border-primary/50 ring-1 ring-primary/40",
        draggable && "cursor-grab active:cursor-grabbing",
      )}
    >
      <span className="flex items-center gap-1.5">
        {issue.identifier && <span className="font-mono text-label text-muted-foreground-soft">{issue.identifier}</span>}
        <span className="flex-1" />
        {worker.id ? (
          worker.isHuman ? (
            <span
              data-testid="assignee-avatar-user"
              title={worker.name || ""}
              className="grid size-[18px] place-items-center rounded-full bg-muted text-muted-foreground"
            >
              <UserRound aria-label={worker.name || "A person"} className="size-3" />
            </span>
          ) : (
            <AgentAvatar
              data-testid="assignee-avatar-agent"
              seed={worker.id}
              alt={worker.name || ""}
              title={worker.name || ""}
              className="size-[18px] rounded-full"
            />
          )
        ) : (
          <span title="No delegate" className="grid size-[18px] place-items-center rounded-md border border-dashed border-line-strong text-muted-foreground-soft">
            <Bot className="size-3" aria-label="No delegate" />
          </span>
        )}
      </span>
      <span className="line-clamp-3 text-body font-semibold text-foreground">{issue.title}</span>
      <span className="flex flex-wrap items-center gap-1.5">
        <PriorityIcon priority={issue.priority || "none"} className="size-3.5 shrink-0" />
        {subs > 0 && (
          <span title={`${subs} sub-issues`} className="inline-flex items-center gap-1 font-mono text-label text-muted-foreground-soft">
            <ListChecks className="size-3" aria-hidden />
            {subs}
          </span>
        )}
        {(issue.labels ?? []).slice(0, 3).map((l) => (
          <IssueLabelChip key={l.id} label={l} />
        ))}
        {due?.late && <span className="text-label text-destructive">{due.text}</span>}
        {issue.work_mode === "human" && (
          <span className="text-label text-warn">{issue.work_stopping ? "Taking over…" : "With a person"}</span>
        )}
      </span>
    </button>
  )
})
