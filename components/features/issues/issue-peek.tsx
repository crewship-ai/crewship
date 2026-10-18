"use client"

import * as React from "react"
import { Bot, Link2, ListChecks, Maximize2, UserRound } from "lucide-react"

import { apiFetch } from "@/lib/api-fetch"
import { toast } from "@/lib/toast"
import { getIssueWorker } from "@/lib/issue-execution"
import { Button } from "@/components/ui/button"
import { Peek } from "@/components/ui/peek"
import { IssueCheckpointCard } from "./issue-checkpoint-card"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { UserAvatar } from "@/components/ui/user-avatar"
import { Skeleton } from "@/components/ui/skeleton"
import type { Mission } from "@/lib/types/mission"
import { MarkdownContent } from "./markdown-content"
import { PriorityIcon } from "./priority-icon"
import { StatusIcon } from "./status-icon"
import { IssueLabelChip } from "./issue-row"
import { PRIORITY_TEXT, STATUS_TEXT, dueLabel } from "./issues-view-model"

/** Who holds the work, in one line — the first thing a reader of an issue wants to know. */
export function workHolder(issue: Mission): { title: string; detail: string; kind: "agent" | "person" | "nobody" } {
  const w = getIssueWorker(issue)
  const rev = issue.work_revision ?? issue.revision
  const revision = rev != null ? ` · revision ${rev}` : ""
  if (w.id && w.isHuman) {
    return { kind: "person", title: `${w.name || "A person"} holds the work`, detail: `Agents can't run on this issue until it is handed off${revision}` }
  }
  if (w.id) {
    return { kind: "agent", title: `${w.name || "An agent"} holds the work`, detail: `${issue.crew_name ? `${issue.crew_name} crew` : "Agent"}${revision}` }
  }
  return { kind: "nobody", title: "Nobody holds the work", detail: "Delegate an agent or take it yourself" }
}

function Prop({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[7rem_minmax(0,1fr)] items-start gap-2 py-1.5">
      <dt className="pt-0.5 text-label text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-body text-foreground">
        <div className="flex min-w-0 flex-wrap items-center gap-1.5">{children}</div>
        {hint && <small className="block text-label text-muted-foreground-soft">{hint}</small>}
      </dd>
    </div>
  )
}

/**
 * Peek (480 px): an issue without leaving the list. J / K step through the
 * list, Enter or the title opens the full page, Esc closes. Everything here
 * is read from the issue; changes happen on the full page.
 */
export function IssuePeek({
  open,
  onOpenChange,
  identifier,
  row,
  workspaceId,
  onPrev,
  onNext,
  onOpenFull,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  identifier: string | null
  /** The list's row, shown at once while the fresh copy loads. */
  row: Mission | null
  workspaceId: string
  onPrev?: () => void
  onNext?: () => void
  onOpenFull: () => void
}) {
  const [fresh, setFresh] = React.useState<{ id: string; issue: Mission } | null>(null)
  const [missing, setMissing] = React.useState<string | null>(null)

  React.useEffect(() => {
    if (!open || !identifier || !workspaceId) return
    let live = true
    setMissing(null)
    apiFetch(`/api/v1/issues/${encodeURIComponent(identifier)}?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then(async (res) => {
        if (!live) return
        if (res.status === 404) setMissing(identifier)
        else if (res.ok) setFresh({ id: identifier, issue: (await res.json()) as Mission })
      })
      .catch(() => {
        /* the row stays on screen; the full page reports its own errors */
      })
    return () => {
      live = false
    }
  }, [open, identifier, workspaceId, row?.updated_at])

  const issue = fresh?.id === identifier ? fresh.issue : row
  const holder = issue ? workHolder(issue) : null
  const due = issue ? dueLabel(issue.due_date, issue.status) : null
  const subs = issue?.sub_issues_count ?? 0

  const copyLink = () => {
    if (!identifier) return
    const url = `${window.location.origin}/issues/${encodeURIComponent(identifier)}`
    navigator.clipboard?.writeText(url).then(() => toast.success("Link copied"), () => toast.error("Could not copy the link"))
  }

  return (
    <Peek
      open={open}
      onOpenChange={onOpenChange}
      title={<span className="font-mono">{identifier}</span>}
      onPrev={onPrev}
      onNext={onNext}
      onOpenFull={onOpenFull}
      // Focus the sheet, not its first button: Enter must open the full page,
      // never press Previous (POP-04). The kit returns focus to the opener on close.
      initialFocus="content"
      returnFocus
      footer={
        <div className="flex items-center gap-2 text-label text-muted-foreground-soft">
          <span className="max-sm:hidden">
            <kbd className="font-mono">↵</kbd> open · <kbd className="font-mono">J</kbd> <kbd className="font-mono">K</kbd> next · <kbd className="font-mono">Esc</kbd> close
          </span>
          <span className="flex-1" />
          <Button variant="ghost" size="sm" onClick={copyLink} className="gap-1.5">
            <Link2 className="size-3.5" aria-hidden />
            Copy link
          </Button>
        </div>
      }
    >
      {missing === identifier ? (
        <div className="grid gap-2 py-8 text-center">
          <b className="text-body">Issue not found</b>
          <p className="text-label text-muted-foreground">No issue is called {identifier} in this workspace. It may have been deleted.</p>
        </div>
      ) : !issue ? (
        <div className="grid gap-3" aria-busy="true">
          <Skeleton className="h-4 w-1/3" />
          <Skeleton className="h-6 w-4/5" />
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-40 w-full" />
        </div>
      ) : (
        <div className="grid gap-4" data-slot="issue-peek">
          <div className="flex items-center gap-1.5 text-label text-muted-foreground">
            <StatusIcon status={issue.status} className="size-3.5" />
            <span className="text-foreground">{STATUS_TEXT[issue.status] ?? issue.status}</span>
            {issue.project_name && <span className="truncate">· {issue.project_name}</span>}
          </div>
          <button type="button" onClick={onOpenFull} title="Open full page" className="group/pt -mt-2 flex items-start gap-2 text-left">
            <h2 className="min-w-0 flex-1 text-heading font-semibold leading-snug text-foreground group-hover/pt:underline">{issue.title}</h2>
            <Maximize2 className="mt-1 size-3.5 shrink-0 text-muted-foreground" aria-hidden />
          </button>

          {holder && (
            <div className="flex items-center gap-2.5 rounded-card border border-border bg-surface-subtle px-3 py-2.5" data-slot="work-holder">
              {holder.kind === "agent" ? (
                <AgentAvatar seed={getIssueWorker(issue).id ?? ""} className="size-[26px] rounded-full" alt="" />
              ) : holder.kind === "person" ? (
                <span className="grid size-[26px] place-items-center rounded-full bg-muted text-muted-foreground"><UserRound className="size-3.5" aria-hidden /></span>
              ) : (
                <span className="grid size-[26px] place-items-center rounded-md border border-dashed border-line-strong text-muted-foreground-soft"><Bot className="size-3.5" aria-hidden /></span>
              )}
              <div className="min-w-0">
                <b className="block truncate text-body font-semibold">{holder.title}</b>
                <small className="block truncate text-label text-muted-foreground">{holder.detail}</small>
              </div>
            </div>
          )}

          <dl className="divide-y divide-border/60">
            <Prop label="Status">
              <StatusIcon status={issue.status} className="size-3.5" /> {STATUS_TEXT[issue.status] ?? issue.status}
            </Prop>
            <Prop label="Priority">
              <PriorityIcon priority={issue.priority || "none"} className="size-3.5" /> {PRIORITY_TEXT[issue.priority || "none"]}
            </Prop>
            <Prop label="Owner" hint="accountable person">
              {issue.owner?.id ? (
                <>
                  <UserAvatar name={issue.owner.name} email="" className="size-5" /> {issue.owner.name || "A person"}
                </>
              ) : (
                <span className="text-muted-foreground">Nobody</span>
              )}
            </Prop>
            <Prop label="Delegate" hint={issue.delegate?.id ? `agent doing the work${issue.crew_name ? ` · ${issue.crew_name} crew` : ""}` : undefined}>
              {issue.delegate?.id ? (
                <>
                  <AgentAvatar seed={issue.delegate.id} className="size-5 rounded-full" alt="" /> {issue.delegate.name || "Agent"}
                </>
              ) : (
                <span className="text-muted-foreground">No delegate</span>
              )}
            </Prop>
            <Prop label="Project">{issue.project_name || <span className="text-muted-foreground">No project</span>}</Prop>
            {(issue.labels ?? []).length > 0 && (
              <Prop label="Labels">
                {(issue.labels ?? []).map((l) => (
                  <IssueLabelChip key={l.id} label={l} />
                ))}
              </Prop>
            )}
            <Prop label="Due">
              {due ? <span className={due.late ? "text-destructive" : undefined}>{due.text}</span> : <span className="text-muted-foreground">—</span>}
            </Prop>
            {issue.estimate != null && <Prop label="Estimate"><span className="font-mono">{issue.estimate}</span></Prop>}
          </dl>

          <IssueCheckpointCard issue={issue} workspaceId={workspaceId} compact />

          <section className="grid gap-1.5" aria-label="Brief">
            <div className="flex items-baseline gap-2">
              <b className="text-body font-semibold">Brief</b>
              <span className="text-label text-muted-foreground-soft">what the agent works from</span>
            </div>
            {issue.description?.trim() ? (
              <MarkdownContent compact className="text-body">{issue.description}</MarkdownContent>
            ) : (
              <p className="text-label text-muted-foreground">No brief yet. Open the full page to write one.</p>
            )}
          </section>

          {subs > 0 && (
            <p className="flex items-center gap-1.5 text-label text-muted-foreground">
              <ListChecks className="size-3.5" aria-hidden /> {subs} sub-issues · open the full page to see them
            </p>
          )}
        </div>
      )}
    </Peek>
  )
}
