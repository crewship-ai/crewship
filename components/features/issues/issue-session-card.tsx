"use client"

import * as React from "react"
import Link from "next/link"
import { Bot, Check, Clock, Inbox, MessageSquare, Pause, Play, RefreshCw, Send, User, UserRound, X } from "lucide-react"

import { cn } from "@/lib/utils"
import { entityHref } from "@/lib/entity-links"
import { Button } from "@/components/ui/button"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { StatusPill } from "@/components/ui/status-pill"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import type { IssueWork } from "@/hooks/use-issue-work"
import type { Mission } from "@/lib/types/mission"
import { MAX_REVIEW_ROUNDS, SESSION_CHIP, type SessionState } from "./issue-session"

export interface IssueSessionCardProps {
  issue: Mission
  state: SessionState
  work: IssueWork
  /** Gates each verb on the reader's rights: assign, change_workflow, start_work. */
  can: (action: string) => boolean
  /** The signed-in person, to say "You" when they hold the work. */
  meId?: string | null
}

/**
 * Who is working on this issue and what the reader can do next — one block
 * per state (issue-session.ts). It replaces the row of workflow buttons in
 * the header: each state offers the verbs that make sense in it, and no
 * others.
 */
export function IssueSessionCard({ issue, state, work, can, meId }: IssueSessionCardProps) {
  const d = issue.delegate ?? (issue.assignee_type === "agent" && issue.assignee_id ? { id: issue.assignee_id, name: issue.assignee_name ?? undefined } : null)
  const agentName = d?.name ?? "The agent"
  const chip = SESSION_CHIP[state]
  const busy = work.busy || Boolean(issue.work_stopping)
  const assign = can("assign")
  const flow = can("change_workflow")

  let head: React.ReactNode
  let body: React.ReactNode = null
  let actions: React.ReactNode = null

  switch (state) {
    case "human": {
      const mine = Boolean(meId && issue.worker_user_id === meId)
      head = (
        <>
          <PersonTile />
          <b className="text-body font-semibold">{mine ? "You’re working on this" : `${issue.worker_name || "A person"} is working on this`}</b>
        </>
      )
      body = <p>Agents can’t run here until the work is handed back. Submit the result with a note, or hand off to an agent or a person.</p>
      actions = (
        <>
          {assign && <Button size="sm" variant="outline" disabled={busy} onClick={() => work.openDialog({ kind: "handoff" })}><Bot className="size-3.5" aria-hidden />Hand off…</Button>}
          {flow && <Button size="sm" disabled={busy} onClick={() => work.openDialog({ kind: "submit" })}><Send className="size-3.5" aria-hidden />Submit result</Button>}
        </>
      )
      break
    }
    case "none":
      head = (
        <>
          <span className="grid size-6 place-items-center rounded-md border border-dashed border-line-strong text-muted-foreground-soft"><Bot className="size-3.5" aria-hidden /></span>
          <b className="text-body font-semibold">Nobody is working on this</b>
        </>
      )
      body = <p>Delegate it to an agent and start, or take it yourself. You stay the owner either way.</p>
      actions = assign && (
        <>
          <Button size="sm" variant="outline" disabled={busy} onClick={() => void work.takeOver()}><User className="size-3.5" aria-hidden />Take it myself</Button>
          <Button size="sm" disabled={busy} onClick={() => work.openDialog({ kind: "delegate" })}><Bot className="size-3.5" aria-hidden />Delegate…</Button>
        </>
      )
      break
    case "delegated":
      head = (
        <>
          <AgentAvatar seed={d?.id ?? agentName} alt="" className="size-6 rounded-full" />
          <b className="text-body font-semibold">{agentName} is delegated</b>
          <span className="text-label text-muted-foreground">not started</span>
        </>
      )
      body = <p>Delegating does not start a run. Start work, or @mention {agentName} in a comment.</p>
      actions = (
        <>
          {assign && <Button size="sm" variant="outline" disabled={busy} onClick={() => void work.takeOver()}><User className="size-3.5" aria-hidden />Take it myself</Button>}
          {can("start_work") && <Button size="sm" disabled={busy} onClick={() => void work.run("start")}><Play className="size-3.5" aria-hidden />Start work</Button>}
        </>
      )
      break
    default: {
      head = (
        <>
          <AgentAvatar seed={d?.id ?? agentName} alt="" className="size-6 rounded-full" />
          <b className="text-body font-semibold">{agentName}</b>
          {chip && <StatusPill tone={chip.tone} label={chip.label} live={state === "active"} />}
          <span className="min-w-0 truncate text-label text-muted-foreground">{hintFor(state, issue)}</span>
          <span className="flex-1" />
          {issue.assignee_slug && (
            <Button asChild size="xs" variant="ghost">
              <Link href={entityHref({ kind: "chat", agentSlug: issue.assignee_slug })}><MessageSquare className="size-3.5" aria-hidden />Open chat</Link>
            </Button>
          )}
        </>
      )
      if (state === "active") {
        body = issue.execution ? <ReviewSteps issue={issue} running /> : null
        actions = (
          <>
            {assign && <Button size="sm" variant="ghost" disabled={busy} onClick={() => void work.takeOver()}><User className="size-3.5" aria-hidden />Take over</Button>}
            {flow && <StopPopover work={work} disabled={busy} />}
          </>
        )
      } else if (state === "awaiting_input") {
        body = <p>{agentName} stopped to ask a person. Answer the request in Inbox, and it continues from where it stopped.</p>
        actions = (
          <>
            {assign && <Button size="sm" variant="outline" disabled={busy} onClick={() => void work.takeOver()}><User className="size-3.5" aria-hidden />Take over</Button>}
            <Button asChild size="sm"><Link href="/inbox"><Inbox className="size-3.5" aria-hidden />Open request in Inbox</Link></Button>
          </>
        )
      } else if (state === "complete") {
        const accept = issue.client_review_required ?? true
        body = (
          <>
            <ReviewSteps issue={issue} />
            {issue.work_note && <Result note={issue.work_note} />}
            {!accept && <p className="text-label text-muted-foreground">No client acceptance: the Lead moves it to Done.</p>}
          </>
        )
        actions = accept && issue.status === "REVIEW" && flow && (
          <>
            <Button size="sm" variant="outline" disabled={busy} onClick={() => work.openDialog({ kind: "request_changes" })}>Request changes</Button>
            <Button size="sm" disabled={busy} onClick={() => void work.run("approve")}><Check className="size-3.5" aria-hidden />Approve</Button>
          </>
        )
      } else if (state === "error") {
        body = (
          <>
            {issue.execution && <ReviewSteps issue={issue} />}
            <p>
              {issue.status === "FAILED"
                ? "The run stopped and the issue is Failed until a person decides."
                : issue.execution?.note || "The Lead could not settle this. A person decides what happens next."}
            </p>
          </>
        )
        actions = (
          <>
            {flow && issue.status === "FAILED" && <Button size="sm" variant="outline" disabled={busy} onClick={() => void work.run("reopen_todo")}><RefreshCw className="size-3.5" aria-hidden />Reopen to Todo</Button>}
            {assign && <Button size="sm" variant="outline" disabled={busy} onClick={() => void work.takeOver()}><User className="size-3.5" aria-hidden />Take over</Button>}
            {assign && <Button size="sm" disabled={busy} onClick={() => work.openDialog({ kind: "handoff", to: "agent", retry: true })}><Play className="size-3.5" aria-hidden />Retry with a note…</Button>}
          </>
        )
      } else if (state === "closed") {
        body = issue.work_note ? <Result note={issue.work_note} /> : null
        actions = flow && (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => void work.run("reopen")}><RefreshCw className="size-3.5" aria-hidden />Reopen</Button>
        )
      }
    }
  }

  return (
    <section
      aria-label="Session"
      data-slot="issue-session"
      data-state={state}
      className={cn(
        "flex flex-col gap-2.5 rounded-card border bg-card p-3.5 text-body",
        state === "error" ? "border-destructive/30" : state === "awaiting_input" ? "border-warn/40" : "border-border",
      )}
    >
      <div className="flex min-w-0 flex-wrap items-center gap-2">{head}</div>
      {body && <div className="flex flex-col gap-2 text-muted-foreground">{body}</div>}
      {actions && <div className="flex flex-wrap justify-end gap-2">{actions}</div>}
    </section>
  )
}

function hintFor(state: SessionState, issue: Mission): string {
  const stage = issue.execution?.stage
  if (state === "active") return stage === "changes_requested" ? "revising after changes were requested" : stage === "reviewing" ? "Lead review" : "running"
  if (state === "awaiting_input") return "waiting for an answer"
  if (state === "complete") return stage === "reviewing" ? "Lead is checking the result" : "waiting for your acceptance"
  if (state === "error") return issue.status === "FAILED" ? "the run failed" : "both correction rounds are used"
  if (state === "closed") return ["DONE", "COMPLETED"].includes(issue.status) ? "done" : "closed"
  return ""
}

function PersonTile() {
  return (
    <span className="grid size-6 place-items-center rounded-full bg-muted text-muted-foreground">
      <UserRound className="size-3.5" aria-hidden />
    </span>
  )
}

function Result({ note }: { note: string }) {
  return (
    <div className="rounded-lg border border-border bg-surface-subtle px-3 py-2">
      <b className="block text-label font-semibold text-foreground">Result</b>
      <p className="whitespace-pre-wrap break-words text-body text-foreground">{note}</p>
    </div>
  )
}

/** Worker → Lead review → your acceptance, as three steps. */
function ReviewSteps({ issue, running = false }: { issue: Mission; running?: boolean }) {
  const ex = issue.execution
  const round = Math.min((ex?.attempt ?? 0) + 1, MAX_REVIEW_ROUNDS)
  const lead =
    ex?.stage === "accepted"
      ? { cls: "text-success", icon: Check, text: `Lead approved · round ${round} of ${MAX_REVIEW_ROUNDS}` }
      : ex?.stage === "changes_requested"
        ? { cls: "text-warn", icon: RefreshCw, text: `Lead requested changes · round ${round} of ${MAX_REVIEW_ROUNDS}` }
        : ex?.stage === "needs_human"
          ? { cls: "text-destructive", icon: X, text: "Lead: needs a person" }
          : ex?.stage === "reviewing"
            ? { cls: "text-primary-hover", icon: Clock, text: "Lead reviewing" }
            : { cls: "text-muted-foreground", icon: Clock, text: "Lead review" }
  const accept = issue.client_review_required ?? true
  const you = ["DONE", "COMPLETED"].includes(issue.status)
    ? { cls: "text-success", icon: Check, text: "Accepted" }
    : accept
      ? { cls: issue.status === "REVIEW" && ex?.stage !== "reviewing" ? "text-primary-hover" : "text-muted-foreground", icon: User, text: "Your acceptance" }
      : { cls: "text-muted-foreground", icon: X, text: "No client acceptance" }
  const steps = [
    running ? { cls: "text-primary-hover", icon: Play, text: "Worker running" } : { cls: "text-success", icon: Check, text: "Worker done" },
    lead,
    you,
  ]
  return (
    <ol className="flex flex-wrap items-center gap-x-2 gap-y-1 text-label" aria-label="Review steps">
      {steps.map((s, i) => (
        <li key={s.text} className={cn("inline-flex items-center gap-1", s.cls)}>
          {i > 0 && <span className="mr-1 h-px w-3 bg-border" aria-hidden />}
          <s.icon className="size-3" aria-hidden />
          {s.text}
        </li>
      ))}
      {ex?.note && ex.stage !== "needs_human" && <li className="basis-full text-muted-foreground">Lead: “{ex.note}”</li>}
    </ol>
  )
}

/** Stop after the current step (cooperative) or now (kills the run). */
export function StopPopover({ work, disabled, size = "sm" }: { work: IssueWork; disabled?: boolean; size?: "sm" | "xs" }) {
  const [open, setOpen] = React.useState(false)
  const pick = (action: "stop" | "stop_now") => {
    setOpen(false)
    void work.run(action)
  }
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button size={size} variant="outline" disabled={disabled}>
          <Pause className="size-3.5" aria-hidden />
          Stop
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-64 p-1" aria-label="Stop the run">
        <p className="px-2 pb-1 pt-1.5 text-label font-semibold text-muted-foreground">Stop the run</p>
        <button type="button" onClick={() => pick("stop")} className="row-hover flex w-full flex-col items-start rounded-md px-2 py-1.5 text-left">
          <b className="text-body font-medium">Stop after this step</b>
          <small className="text-label text-muted-foreground">Cooperative; keeps the work consistent</small>
        </button>
        <button type="button" onClick={() => pick("stop_now")} className="row-hover flex w-full flex-col items-start rounded-md px-2 py-1.5 text-left">
          <b className="text-body font-medium text-destructive">Stop now</b>
          <small className="text-label text-muted-foreground">Kills the running agent processes</small>
        </button>
      </PopoverContent>
    </Popover>
  )
}
