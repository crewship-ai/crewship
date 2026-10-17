"use client"

import * as React from "react"
import { Bot, ChevronRight, Gauge, Play, Send, User, UserRound } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import type { IssueWork } from "@/hooks/use-issue-work"
import type { Mission } from "@/lib/types/mission"
import { MAX_REVIEW_ROUNDS, isClosedStatus } from "./issue-session"
import { StopPopover } from "./issue-session-card"

export interface IssueWorkCardProps {
  issue: Mission
  work: IssueWork
  /** Gates each verb on the reader's rights: assign, change_workflow, start_work. */
  can: (action: string) => boolean
  meId?: string | null
  /** The Routine row's control (the routine picker), owned by the host. */
  routine?: React.ReactNode
  /** The ⋯ menu for this issue. */
  menu?: React.ReactNode
}

/** Client acceptance can change only before the work starts. */
export const POLICY_LOCK_REASON = "Changes only in Todo or Backlog with no active run."

/**
 * Work & control: who holds the work and at which revision, the verbs that
 * move it, and the review rules — Lead review, the client acceptance switch
 * and the bound routine.
 */
export function IssueWorkCard({ issue, work, can, meId, routine, menu }: IssueWorkCardProps) {
  const human = issue.work_mode === "human"
  const d = issue.delegate ?? (issue.assignee_type === "agent" && issue.assignee_id ? { id: issue.assignee_id, name: issue.assignee_name ?? undefined } : null)
  const running = !human && (issue.status === "IN_PROGRESS" || issue.execution?.stage === "working")
  const closed = isClosedStatus(issue.status)
  const busy = work.busy
  const stopping = Boolean(issue.work_stopping)
  const assign = can("assign")
  const flow = can("change_workflow")
  const canPolicy = ["TODO", "BACKLOG"].includes(issue.status) && !running
  const accept = issue.client_review_required ?? true
  const rev = issue.work_revision ?? 0

  let who: React.ReactNode
  if (human) {
    const mine = Boolean(meId && issue.worker_user_id === meId)
    who = (
      <>
        <span className="grid size-[26px] shrink-0 place-items-center rounded-full bg-muted text-muted-foreground"><UserRound className="size-3.5" aria-hidden /></span>
        <div className="min-w-0">
          <b className="block text-body font-semibold">{mine ? "You hold the work" : `${issue.worker_name || "A person"} holds the work`}</b>
          <small className="text-label text-muted-foreground">
            {stopping ? "Stopping the agent runs first" : "Agents can’t run on this issue until it is handed off"} · revision {rev}
          </small>
        </div>
      </>
    )
  } else if (d) {
    who = (
      <>
        <AgentAvatar seed={d.id} alt="" className="size-[26px] shrink-0 rounded-full" />
        <div className="min-w-0">
          <b className="block text-body font-semibold">{d.name ?? "An agent"} holds the work</b>
          <small className="text-label text-muted-foreground">
            {issue.crew_name ? `${issue.crew_name} crew · ` : ""}revision {rev}
          </small>
        </div>
      </>
    )
  } else {
    who = (
      <>
        <span className="grid size-[26px] shrink-0 place-items-center rounded-md border border-dashed border-line-strong text-muted-foreground-soft"><Bot className="size-3.5" aria-hidden /></span>
        <div className="min-w-0">
          <b className="block text-body font-semibold">Nobody holds the work</b>
          <small className="text-label text-muted-foreground">Delegate an agent or take it yourself · revision {rev}</small>
        </div>
      </>
    )
  }

  let actions: React.ReactNode = null
  if (!closed) {
    if (human) {
      actions = (
        <>
          {flow && stopping && <Button size="sm" variant="outline" disabled={busy} onClick={() => void work.retryStop()}>Retry stopping agents</Button>}
          {assign && <Button size="sm" variant="outline" disabled={busy || stopping} onClick={() => work.openDialog({ kind: "handoff" })}><Bot className="size-3.5" aria-hidden />Hand off…</Button>}
          {flow && <Button size="sm" disabled={busy || stopping} onClick={() => work.openDialog({ kind: "submit" })}><Send className="size-3.5" aria-hidden />Submit result</Button>}
        </>
      )
    } else if (d) {
      actions = (
        <>
          {assign && <Button size="sm" variant="outline" disabled={busy} onClick={() => void work.takeOver()}><User className="size-3.5" aria-hidden />Take over</Button>}
          {assign && <Button size="sm" variant="outline" disabled={busy} onClick={() => work.openDialog({ kind: "handoff" })}><ChevronRight className="size-3.5" aria-hidden />Hand off…</Button>}
          {flow && running && <StopPopover work={work} disabled={busy} />}
          {can("start_work") && ["BACKLOG", "TODO"].includes(issue.status) && (
            <Button size="sm" disabled={busy} onClick={() => void work.run("start")}><Play className="size-3.5" aria-hidden />Start work</Button>
          )}
        </>
      )
    } else if (assign) {
      actions = (
        <>
          <Button size="sm" variant="outline" disabled={busy} onClick={() => void work.takeOver()}><User className="size-3.5" aria-hidden />Take it myself</Button>
          <Button size="sm" disabled={busy} onClick={() => work.openDialog({ kind: "delegate" })}><Bot className="size-3.5" aria-hidden />Delegate…</Button>
        </>
      )
    }
  }

  return (
    <section aria-label="Work & control" data-slot="issue-work" className="overflow-hidden rounded-card border border-border bg-card">
      <header className="flex items-center gap-2 border-b border-border px-3.5 py-2.5">
        <Gauge className="size-3.5 text-muted-foreground" aria-hidden />
        <b className="text-label font-semibold uppercase tracking-wide text-muted-foreground">Work &amp; control</b>
        <span className="flex-1" />
        {menu}
      </header>
      <div className="flex items-center gap-2.5 px-3.5 pt-3">{who}</div>
      {issue.owner && <p className="px-3.5 pt-1.5 text-label text-muted-foreground">Accountable owner: {issue.owner.name || "Workspace member"}</p>}
      {actions && <div className="flex flex-wrap gap-2 px-3.5 pt-3">{actions}</div>}
      {work.error && !work.dialog && <p role="alert" className="px-3.5 pt-2 text-label text-destructive">{work.error}</p>}
      <dl className="mt-3 flex flex-col border-t border-border text-body">
        <div className="flex items-center justify-between gap-3 px-3.5 py-2">
          <dt className="text-muted-foreground">Lead review</dt>
          <dd className="text-right font-medium">{issue.execution?.reviewer ? `${issue.execution.reviewer} · ` : issue.crew_name ? `${issue.crew_name} lead · ` : ""}max {MAX_REVIEW_ROUNDS} rounds</dd>
        </div>
        <div className="flex flex-col gap-1 px-3.5 py-2">
          <div className="flex items-center justify-between gap-3">
            <dt className="text-muted-foreground">{flow ? <label htmlFor={`accept-${issue.id}`}>Client acceptance</label> : "Client acceptance"}</dt>
            <dd>
              {flow ? (
              <Switch
                id={`accept-${issue.id}`}
                checked={accept}
                disabled={busy || !canPolicy}
                onCheckedChange={(v) => void work.setClientAcceptance(v)}
                aria-label="Require client acceptance"
                aria-describedby={canPolicy ? undefined : `accept-lock-${issue.id}`}
              />
              ) : (
                <span className="font-medium">{accept ? "Required" : "Not required"}</span>
              )}
            </dd>
          </div>
          {canPolicy ? (
            <p className="text-label text-muted-foreground">
              {accept ? "Lead verifies the result, then a person approves completion." : "Lead can mark verified work Done. Uncertainty still comes back to a person."}
            </p>
          ) : (
            <p id={`accept-lock-${issue.id}`} className="text-label text-muted-foreground">{POLICY_LOCK_REASON}</p>
          )}
        </div>
        {routine && (
          <div className="flex items-center justify-between gap-3 px-3.5 py-2">
            <dt className="text-muted-foreground">Routine</dt>
            <dd className="min-w-0">{routine}</dd>
          </div>
        )}
      </dl>
      {issue.work_note && !closed && (
        <details className="border-t border-border px-3.5 py-2 text-label">
          <summary className="cursor-pointer text-muted-foreground">Latest handoff or result</summary>
          <p className="mt-2 whitespace-pre-wrap break-words text-foreground">{issue.work_note}</p>
        </details>
      )}
    </section>
  )
}
