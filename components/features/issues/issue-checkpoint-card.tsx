"use client"

import * as React from "react"

import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { relTime } from "@/lib/time"
import { hasIssueAgentDelegate } from "@/lib/issue-execution"
import type { Mission } from "@/lib/types/mission"

/** One row of `GET /crews/{crew}/issues/{identifier}/sessions/{session}/checkpoints` (issue_checkpoints.go). */
export interface IssueCheckpoint {
  id: string
  session_id: string
  done?: string
  plan?: string
  facts?: string
  blockers?: string
  next_step?: string
  confidence?: string
  parsed?: boolean
  created_at: string
}

interface IssueSession {
  id: string
  agent_id: string
}

/**
 * The newest checkpoint (the endpoint is newest-first), or null when it has
 * nothing a person could read. The agent's plan is free text; there is no
 * structured "step 3 of 5", so none is invented here.
 */
export function latestCheckpoint(list: IssueCheckpoint[]): IssueCheckpoint | null {
  const c = list[0]
  if (!c) return null
  return [c.done, c.plan, c.facts, c.blockers, c.next_step].some((s) => s?.trim()) ? c : null
}

const asList = <T,>(v: unknown): T[] => (Array.isArray(v) ? (v as T[]) : [])

/**
 * What the agent last wrote down about its own work: the next step, what
 * blocks it and what is done. Hidden for an issue with no agent session or
 * no checkpoint yet. `compact` (the peek, a phone) keeps next step and
 * blockers only.
 */
export function IssueCheckpointCard({ issue, workspaceId, compact = false, className }: { issue: Mission; workspaceId: string; compact?: boolean; className?: string }) {
  const [cp, setCp] = React.useState<IssueCheckpoint | null>(null)
  const agentId = issue.delegate?.id ?? (issue.assignee_type === "agent" ? issue.assignee_id : null)
  const eligible = hasIssueAgentDelegate(issue) && Boolean(issue.crew_id && workspaceId)
  const ident = issue.identifier ?? issue.id

  React.useEffect(() => {
    if (!eligible) {
      setCp(null)
      return
    }
    let live = true
    const base = `/api/v1/crews/${encodeURIComponent(issue.crew_id)}/issues/${encodeURIComponent(ident)}/sessions`
    const qs = `workspace_id=${encodeURIComponent(workspaceId)}`
    const read = async (url: string): Promise<unknown> => {
      const res = await apiFetch(url)
      return res.ok ? res.json() : null
    }
    void (async () => {
      try {
        const sessions = asList<IssueSession>(await read(`${base}?${qs}`))
        const mine = sessions.find((s) => agentId && s.agent_id === agentId) ?? sessions[0]
        if (!mine) return live && setCp(null)
        const list = asList<IssueCheckpoint>(await read(`${base}/${encodeURIComponent(mine.id)}/checkpoints?${qs}`))
        if (live) setCp(latestCheckpoint(list))
      } catch {
        // Supplementary: the session card above already says what is happening.
        if (live) setCp(null)
      }
    })()
    return () => {
      live = false
    }
  }, [eligible, issue.crew_id, workspaceId, ident, agentId, issue.status, issue.updated_at])

  if (!eligible || !cp) return null
  const who = issue.delegate?.name ?? issue.assignee_name ?? "The agent"
  const text = (label: string, value: string | undefined, tone?: "warn") =>
    value?.trim() ? (
      <div className="grid gap-0.5">
        <dt className={cn("text-label font-semibold", tone === "warn" ? "text-warn" : "text-muted-foreground")}>{label}</dt>
        <dd className="whitespace-pre-wrap break-words text-body text-foreground">{value.trim()}</dd>
      </div>
    ) : null

  return (
    <section
      aria-label="Agent progress"
      data-slot="issue-checkpoint"
      className={cn("flex flex-col gap-2 rounded-card border border-border bg-card p-3.5", className)}
    >
      <div className="flex flex-wrap items-baseline gap-2">
        <b className="text-body font-semibold">{who}’s latest checkpoint</b>
        <time dateTime={cp.created_at} className="text-label text-muted-foreground-soft">
          {relTime(cp.created_at)}
        </time>
        {cp.confidence?.trim() && <span className="text-label text-muted-foreground">confidence: {cp.confidence.trim()}</span>}
      </div>
      <dl className="grid gap-2">
        {text("Next step", cp.next_step)}
        {text("Blockers", cp.blockers, "warn")}
        {!compact && text("Done so far", cp.done)}
        {!compact && text("Plan", cp.plan)}
      </dl>
    </section>
  )
}
