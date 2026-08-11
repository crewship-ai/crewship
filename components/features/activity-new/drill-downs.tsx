"use client"

// Where a lens row leads.
//
// Every row in every lens landed on the same thing: the journal feed, narrowed.
// For an issue that meant a card headed "Activity · 0 in past 24 hours" and the
// words "Nothing here" — a click that promised the issue and delivered an empty
// list, because a mission's own events do not carry the entry types the feed
// queries. The issue was right there in the database with a title, a status,
// four comments and two runs, and the page showed none of it.
//
// So each kind gets the surface that kind already has elsewhere, rather than a
// fifth rendering of the feed:
//
//   issue   IssueDetailSurface — the same component /issues opens, with the
//           description, comments, relations, sub-issues and runs
//   run     the run's own steps and what they cost, plus its journal
//   agent   what the agent took on, and its sessions
//
// Nothing here is new UI vocabulary. StatStrip, DetailCard, Appear and Pill are
// the kit /routines is built from, and reusing them is the whole point: the
// complaint was "každý pes jiná ves", and a fourth private card style would be
// one more village.

import * as React from "react"
import { Bot, CircleDot, Clock, ListTree, Terminal } from "lucide-react"

import { Appear, DetailCard, EmptyState, Pill, StatStrip, type StatItem } from "@/components/ui/detail"
import { Spinner } from "@/components/ui/spinner"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { IssueDetailSurface } from "@/components/features/issues/issue-detail-surface"
import { RunActivityTimeline } from "@/components/features/activity/run-activity-timeline"
import { usePipelineRunRecords } from "@/hooks/use-pipeline-run-records"
import { formatDurationMs } from "@/lib/activity-stream"
import { relTime } from "@/lib/time"
import { runHeadline } from "@/lib/run-digest"
import type { ChainSummary } from "@/hooks/use-chains"

/** The page shell every drill-down shares — one width, one rhythm. */
function Shell({ children }: { children: React.ReactNode }) {
  return <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">{children}</div>
}

/* ------------------------------------------------------------------ *
 *  Issue
 * ------------------------------------------------------------------ */

export interface IssueDrillDownProps {
  workspaceId: string
  /** ENG-7 — what both URLs carry and what the surface resolves by. */
  identifier: string
  /** Chains that touched it, for the strip and the "who did this" line. */
  chains: ChainSummary[]
}

/**
 * One issue, in full.
 *
 * IssueDetailSurface is the component /issues renders, unchanged and read-only
 * here. Rendering a smaller copy would be a second place for an issue to be
 * drawn, and the two would drift the first time somebody adds a field.
 *
 * Read-only on purpose: this is an observability surface. A reader who wants to
 * change the issue should be on the page that owns it, and the surface links
 * there. Editing in two places is how two people overwrite each other.
 */
export function IssueDrillDown({ workspaceId, identifier, chains }: IssueDrillDownProps) {
  // Which chains reached this issue. The strip's numbers come from the same
  // ChainSummary[] the rail lists, so they cannot disagree with the row that
  // led here.
  const touching = React.useMemo(
    () => chains.filter((c) => (c.issues ?? []).some((i) => i.identifier === identifier || i.id === identifier)),
    [chains, identifier],
  )
  const created = touching.some((c) =>
    (c.issues ?? []).some((i) => (i.identifier === identifier || i.id === identifier) && i.created),
  )
  const agents = React.useMemo(
    () => [...new Set(touching.flatMap((c) => (c.agents ?? []).map((a) => a.name || a.slug || a.id)))],
    [touching],
  )

  const stats: StatItem[] = [
    { label: "Workflows", value: String(touching.length) },
    { label: "Origin", value: created ? "created here" : "existed before", tone: created ? "success" : "default" },
    { label: "Agents", value: agents.length > 0 ? agents.join(", ") : "—" },
    {
      label: "Last touched",
      value: touching[0] ? relTime(touching[0].last_activity) : "—",
    },
  ]

  return (
    <Shell>
      {/* What Activity knows that /issues does not: which processes reached
          this issue, and which agent did the reaching. */}
      <Appear order={0}>
        <StatStrip items={stats} />
      </Appear>
      <Appear order={1}>
        <IssueDetailSurface workspaceId={workspaceId} identifier={identifier} editable={false} />
      </Appear>
    </Shell>
  )
}

/* ------------------------------------------------------------------ *
 *  Run
 * ------------------------------------------------------------------ */

export interface RunDrillDownProps {
  workspaceId: string
  runID: string
  /** The routine it belongs to, when the caller knows — enables the steps card. */
  routineSlug?: string
}

/**
 * One run: what it did, step by step.
 *
 * This replaced a page whose whole content was "3 events mentioning this run" —
 * three journal lines saying started, step completed, completed. True, and not
 * what somebody clicking a run is asking. They want the picture the routine
 * shows, but of THIS execution: which step, when, how long, what it cost.
 *
 * The steps come from the routine's run-records list rather than a per-run
 * endpoint, because the record for this run carries its own output, duration
 * and error and the list is one indexed query the page beside this already
 * makes.
 */
export function RunDrillDown({ workspaceId, runID, routineSlug }: RunDrillDownProps) {
  const { records, loading } = usePipelineRunRecords(workspaceId, routineSlug ?? null)
  const run = React.useMemo(() => records.find((r) => r.id === runID), [records, runID])

  const head = run
    ? runHeadline({
        id: run.id,
        status: run.status,
        started_at: run.started_at,
        duration_ms: run.duration_ms,
        output: run.output,
        error_message: run.error_message,
      })
    : null

  const stats: StatItem[] = run
    ? [
        { label: "Status", value: run.status, tone: run.status === "failed" ? "destructive" : "success" },
        { label: "Started", value: new Date(run.started_at).toLocaleTimeString(undefined, { hour12: false }), mono: true },
        { label: "Duration", value: formatDurationMs(run.duration_ms), mono: true },
        { label: "Cost", value: run.cost_usd > 0 ? `$${run.cost_usd.toFixed(4)}` : "—", mono: true },
        { label: "Trigger", value: run.triggered_via ?? "—" },
        // Depth 0 is the overwhelming majority, so naming it "started by hand"
        // says more than the number does.
        { label: "Composed", value: (run.chain_depth ?? 0) > 0 ? `depth ${run.chain_depth}` : "root" },
      ]
    : []

  return (
    <Shell>
      <Appear order={0}>
        <div className="flex flex-wrap items-center gap-2">
          <h1 className="min-w-0 font-mono text-base font-semibold tracking-tight">{runID}</h1>
          {run && <Pill tone={run.status === "failed" ? "destructive" : "success"}>{run.status}</Pill>}
          {routineSlug && <Pill tone="default">{routineSlug}</Pill>}
        </div>
      </Appear>

      {run && (
        <Appear order={1}>
          <StatStrip items={stats} />
        </Appear>
      )}

      {/* What it produced, before what it emitted. The output IS the answer;
          the journal lines are the evidence. */}
      {head?.text && (
        <Appear order={2}>
          <DetailCard title="Result" subtitle={run?.failed_at_step ? `failed at ${run.failed_at_step}` : undefined}>
            {/* Model- and author-written; React escapes it and nothing here
                renders HTML from the server. */}
            <pre className="max-h-[240px] overflow-auto whitespace-pre-wrap break-words font-mono text-[11px] leading-relaxed text-muted-foreground">
              {run?.status === "failed" ? run?.error_message : run?.output}
            </pre>
          </DetailCard>
        </Appear>
      )}

      {loading && !run && (
        <div className="flex items-center justify-center gap-2 py-16 text-xs text-muted-foreground">
          <Spinner className="h-3.5 w-3.5" /> Loading the run…
        </div>
      )}

      {!loading && !run && !routineSlug && (
        <Appear order={2}>
          <EmptyState
            icon={Terminal}
            title="This run's record is not loaded"
            description="The run's own row is fetched per routine, and this run was opened without one. Its journal is below."
          />
        </Appear>
      )}

      {/* The steps, live. Same component the trace page uses, so a run reads
          the same wherever it is opened. */}
      <Appear order={3}>
        <RunActivityTimeline
          workspaceId={workspaceId}
          params={{ run_id: runID }}
          title="Steps"
          card
          hideWhenEmpty={false}
        />
      </Appear>
    </Shell>
  )
}

/* ------------------------------------------------------------------ *
 *  Agent
 * ------------------------------------------------------------------ */

export interface AgentDrillDownProps {
  workspaceId: string
  agentID: string
  name: string
  /** Chains this agent worked in — its history, from what the rail holds. */
  chains: ChainSummary[]
  onOpenWorkflow: (origin: string) => void
}

/**
 * One agent's work.
 *
 * The question is "what has this one been up to", and until now the answer was
 * the journal narrowed to its id — a flat list where a delegation, a message
 * and a container metric all read as one row.
 *
 * Here it is the work: which processes it took part in, how many pieces of work
 * in each, and what those reached. Its sessions and messages live on the agent's
 * own page, which the header links to rather than reproducing — the same rule
 * the issue surface follows.
 */
export function AgentDrillDown({ workspaceId, agentID, name, chains, onOpenWorkflow }: AgentDrillDownProps) {
  void workspaceId

  const worked = React.useMemo(
    () => chains.filter((c) => (c.agents ?? []).some((a) => a.id === agentID)),
    [chains, agentID],
  )
  const assignments = worked.reduce(
    (n, c) => n + ((c.agents ?? []).find((a) => a.id === agentID)?.assignments ?? 0),
    0,
  )
  const issues = [...new Set(worked.flatMap((c) => (c.issues ?? []).map((i) => i.identifier || i.id)))]
  // Wall clock of the chains it worked in. Named as such on the strip — it is
  // not billed agent time, which the index does not carry.
  const spanMs = worked.reduce((n, c) => n + (c.duration_ms ?? 0), 0)

  const stats: StatItem[] = [
    { label: "Workflows", value: String(worked.length) },
    { label: "Assignments", value: String(assignments) },
    { label: "Issues reached", value: issues.length > 0 ? issues.join(", ") : "—" },
    { label: "In workflows", value: spanMs > 0 ? formatDurationMs(spanMs) : "—", mono: true },
  ]

  return (
    <Shell>
      <Appear order={0}>
        <div className="flex flex-wrap items-center gap-2">
          <AgentAvatar seed={agentID} alt="" className="h-6 w-6 shrink-0 rounded-full" />
          <h1 className="min-w-0 text-lg font-semibold tracking-tight">{name}</h1>
          <a href={`/agents/${encodeURIComponent(agentID)}`} className="text-[11px] text-primary hover:underline">
            Agent page ↗
          </a>
        </div>
      </Appear>

      <Appear order={1}>
        <StatStrip items={stats} />
      </Appear>

      <Appear order={2}>
        <DetailCard title="What it worked on" subtitle={`${worked.length} ${worked.length === 1 ? "workflow" : "workflows"}`}>
          {worked.length === 0 ? (
            <EmptyState
              icon={Bot}
              title="No work in this window"
              description="Work this agent did outside a routine's dispatch is not indexed yet — it has no chain to belong to."
            />
          ) : (
            <div className="flex flex-col">
              {worked.map((c) => {
                const mine = (c.agents ?? []).find((a) => a.id === agentID)
                return (
                  <button
                    key={c.origin}
                    type="button"
                    onClick={() => onOpenWorkflow(c.origin)}
                    className="grid grid-cols-[8px_1fr_auto_auto] items-center gap-3 rounded-md px-1.5 py-2 text-left text-[11.5px] transition-colors hover:bg-white/[0.03]"
                  >
                    <span
                      aria-hidden
                      className="h-1.5 w-1.5 rounded-full"
                      style={{ background: `var(${c.failed ? "--destructive" : "--success"})` }}
                    />
                    <span className="min-w-0 truncate">{c.routine_slug || c.started_by}</span>
                    <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
                      ×{mine?.assignments ?? 1}
                    </span>
                    <span className="w-16 shrink-0 text-right font-mono text-[10px] text-muted-foreground-soft">
                      {relTime(c.last_activity)}
                    </span>
                  </button>
                )
              })}
            </div>
          )}
        </DetailCard>
      </Appear>
    </Shell>
  )
}

/** Icons re-exported for the shell's branch table. */
export { CircleDot, Clock, ListTree }
