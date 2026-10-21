"use client"

import { memo } from "react"
import { Bot, Calendar, ListChecks, UserRound } from "lucide-react"

import { cn } from "@/lib/utils"
import { getIssueWorker } from "@/lib/issue-execution"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Checkbox } from "@/components/ui/checkbox"
import { UserAvatar } from "@/components/ui/user-avatar"
import type { IssueLabel, Mission } from "@/lib/types/mission"
import { PriorityIcon, priorityLabel } from "./priority-icon"
import { StatusIcon } from "./status-icon"
import { DEFAULT_ROW_FIELDS, STATUS_TEXT, dueLabel, shortAgo, type RowFields } from "./issues-view-model"

/** A label as a chip with its colour as a dot. */
export function IssueLabelChip({ label }: { label: Pick<IssueLabel, "name" | "color"> }) {
  return (
    <span
      data-slot="issue-label"
      className="inline-flex h-5 shrink-0 items-center gap-1.5 rounded-full border border-border px-2 text-micro text-muted-foreground"
    >
      <span aria-hidden className="size-1.5 rounded-full" style={{ background: label.color || "var(--muted-foreground)" }} />
      {label.name}
    </span>
  )
}

/** Owner (a person) and delegate (an agent), overlapped; a dashed tile when nobody is delegated. */
export function OwnerDelegate({ issue, showOwner = true, size = 20 }: { issue: Mission; showOwner?: boolean; size?: number }) {
  const worker = getIssueWorker(issue)
  const delegate = issue.delegate?.id ? { id: issue.delegate.id, name: issue.delegate.name } : worker.id && !worker.isHuman ? { id: worker.id, name: worker.name } : null
  const px = { width: size, height: size }
  return (
    <span className="inline-flex shrink-0 items-center" data-slot="owner-delegate">
      {showOwner && issue.owner?.id && (
        <span title={`Owner · ${issue.owner.name ?? "a person"}`} className="relative z-[1] -mr-1 rounded-full ring-2 ring-background">
          <UserAvatar name={issue.owner.name} email="" className="size-5" />
        </span>
      )}
      {worker.isHuman && worker.id ? (
        <span title={`${worker.name ?? "A person"} holds the work`} style={px} className="grid place-items-center rounded-full bg-muted text-muted-foreground">
          <UserRound className="size-3" aria-label={worker.name ?? "A person"} />
        </span>
      ) : delegate ? (
        <AgentAvatar seed={delegate.id} alt={delegate.name ?? ""} title={`Delegate · ${delegate.name ?? "agent"}`} width={size} height={size} className="size-5 rounded-full" />
      ) : (
        <span title="No delegate" style={px} className="grid place-items-center rounded-md border border-dashed border-line-strong text-muted-foreground-soft">
          <Bot className="size-3" aria-label="No delegate" />
        </span>
      )}
    </span>
  )
}

export interface IssueRowProps {
  issue: Mission
  fields?: RowFields
  /** The peek is open on this issue. */
  active?: boolean
  checked?: boolean
  onCheckedChange?: (checked: boolean) => void
  onOpen: (issue: Mission) => void
}

/**
 * One issue in the list: 38 px, every field on one line. What it carries is
 * the reader's Display options; the title is the only thing that gives way.
 * On a phone the ID, labels, due date and updated time step aside.
 */
export const IssueRow = memo(function IssueRow({ issue, fields = DEFAULT_ROW_FIELDS, active = false, checked = false, onCheckedChange, onOpen }: IssueRowProps) {
  const due = fields.due ? dueLabel(issue.due_date, issue.status) : null
  const subs = issue.sub_issues_count ?? 0
  const status = STATUS_TEXT[issue.status] ?? issue.status
  return (
    <div
      data-slot="issue-row"
      data-issue={issue.identifier ?? issue.id}
      className={cn(
        "group/irow row-hover flex items-center border-b border-border pl-4 transition-colors",
        (active || checked) && "row-selected",
      )}
    >
      {onCheckedChange && (
        <Checkbox
          checked={checked}
          onCheckedChange={(v) => onCheckedChange(v === true)}
          aria-label={`Select ${issue.identifier ?? issue.title}`}
          className={cn("opacity-60 transition-opacity group-hover/irow:opacity-100", checked && "opacity-100")}
        />
      )}
      <button
        type="button"
        onClick={() => onOpen(issue)}
        aria-current={active || undefined}
        className="flex h-[38px] min-w-0 flex-1 items-center gap-2.5 pl-2.5 pr-3.5 text-left text-body outline-none focus-visible:bg-[var(--row-hover-bg)]"
      >
        <span title={priorityLabel[issue.priority || "none"]} className="grid shrink-0 place-items-center">
          <PriorityIcon priority={issue.priority || "none"} className="size-3.5" />
        </span>
        {fields.id && (
          <span className="w-[5.6rem] shrink-0 truncate font-mono text-label text-muted-foreground-soft max-sm:hidden">
            {issue.identifier || "—"}
          </span>
        )}
        <span title={status} className="grid shrink-0 place-items-center">
          <StatusIcon status={issue.status} className="size-3.5" />
          <span className="sr-only">{status}</span>
        </span>
        <span className="min-w-0 truncate font-medium text-foreground">{issue.title}</span>
        {fields.sub && subs > 0 && (
          <span title={`${subs} sub-issues`} className="inline-flex shrink-0 items-center gap-1 font-mono text-label text-muted-foreground-soft">
            <ListChecks className="size-3" aria-hidden />
            {subs}
          </span>
        )}
        {fields.labels && (issue.labels ?? []).length > 0 && (
          <span className="flex shrink-0 items-center gap-1 max-lg:hidden">
            {(issue.labels ?? []).slice(0, 3).map((l) => (
              <IssueLabelChip key={l.id} label={l} />
            ))}
          </span>
        )}
        <span className="flex-1" />
        {due && (
          <span className={cn("inline-flex shrink-0 items-center gap-1 text-label max-sm:hidden", due.late ? "text-destructive" : "text-muted-foreground-soft")}>
            <Calendar className="size-3" aria-hidden />
            {due.text}
          </span>
        )}
        {fields.updated && (
          <time dateTime={issue.updated_at} className="w-9 shrink-0 text-right font-mono text-label text-muted-foreground-soft max-lg:hidden">
            {shortAgo(issue.updated_at)}
          </time>
        )}
        <OwnerDelegate issue={issue} showOwner={fields.owner} />
      </button>
    </div>
  )
})
