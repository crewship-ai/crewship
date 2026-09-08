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
import { Bot, CircleDot, Clock, ListTree } from "lucide-react"

import { Appear, DetailCard, EmptyState, Pill, StatStrip, type StatItem } from "@/components/ui/detail"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { RoutineRunDetail } from "@/components/features/routines/routine-run-detail"
import { entityHref } from "@/lib/entity-links"
import { formatDurationMs } from "@/lib/activity-stream"
import { relTime } from "@/lib/time"
import { assignmentsOf } from "@/lib/activity-lenses"
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
  /**
   * `missions.id` — the key the chain index carries on every ChainIssueRef, and
   * therefore the only one that matches on every workspace.
   *
   * This took the DISPLAY LABEL before, under the name `identifier`, and the
   * shell obliged with `stop.label`. That label is `identifier || title || id`,
   * so on a workspace that does not use issue identifiers it is the TITLE: no
   * chain matched, the page rendered "Nothing reached it in this window" over an
   * issue with workflows on it, and the deep link pointed at a URL-encoded
   * sentence. The id is what the rail already held; taking it is the fix.
   */
  issueId: string
  /** What the row the reader clicked said. The heading falls back to it. */
  label: string
  /** Chains that touched it, for the strip and the "who did this" line. */
  chains: ChainSummary[]
  onOpenWorkflow: (origin: string) => void
}

/**
 * What HAPPENED to an issue. Not the issue.
 *
 * The first version of this embedded IssueDetailSurface — the whole /issues
 * page: description, comments, relations, sub-issues, pickers. That was an
 * over-correction from the version before it, which showed nothing at all, and
 * it is the wrong object. This is the Activity bar. It answers "what happened",
 * and the detail is one click away by a button that says so.
 *
 * Two people would also then be editing an issue from two places, which is how
 * they overwrite each other — but that is the second reason, not the first. The
 * first is that a reader who came here wanting the issue's body would have gone
 * to /issues.
 */
export function IssueDrillDown({ workspaceId, issueId, label, chains, onOpenWorkflow }: IssueDrillDownProps) {
  // Which chains reached this issue. The strip's numbers come from the same
  // ChainSummary[] the rail lists, so they cannot disagree with the row that
  // led here. Matched on the id alone: an identifier-or-id predicate looks
  // forgiving and is how the wrong key went unnoticed.
  const touching = React.useMemo(
    () => chains.filter((c) => (c.issues ?? []).some((i) => i.id === issueId)),
    [chains, issueId],
  )
  const created = touching.some((c) => (c.issues ?? []).some((i) => i.id === issueId && i.created))
  // The human handle, read off the refs rather than taken from the caller: the
  // chain index carries it, and it is what /issues/[identifier] resolves by.
  // Absent on a workspace that does not use identifiers, which is a fact about
  // the workspace and not a lookup failure — see the link below.
  const identifier = React.useMemo(
    () =>
      touching
        .flatMap((c) => c.issues ?? [])
        .find((i) => i.id === issueId && i.identifier)?.identifier,
    [touching, issueId],
  )
  const heading = identifier || label || issueId
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

  void workspaceId

  return (
    <Shell>
      <Appear order={0}>
        <div className="flex flex-wrap items-center gap-2">
          <CircleDot className="h-4 w-4 shrink-0 text-muted-foreground" />
          <h1 className="min-w-0 font-mono text-base font-semibold tracking-tight">{heading}</h1>
          {created && <Pill tone="success">created here</Pill>}
          {/* Rendered only when there is an identifier to render it with.
              /issues/[identifier] resolves an identifier and nothing else, so a
              workspace that does not use them has no URL for this issue — and a
              button that leads to a 404 is worse than an absent one. */}
          {identifier && (
            <a
              href={`/issues/${encodeURIComponent(identifier)}`}
              className="ml-auto rounded-md border border-white/[0.08] px-2 py-1 text-[11px] text-primary transition-colors hover:bg-white/[0.04]"
            >
              Open issue ↗
            </a>
          )}
        </div>
      </Appear>

      {/* What Activity knows that /issues does not: which processes reached
          this issue, and which agent did the reaching. */}
      <Appear order={1}>
        <StatStrip items={stats} />
      </Appear>

      <Appear order={2}>
        <DetailCard
          title="What reached it"
          subtitle={`${touching.length} ${touching.length === 1 ? "workflow" : "workflows"}`}
        >
          {touching.length === 0 ? (
            <EmptyState
              icon={CircleDot}
              title="Nothing reached it in this window"
              description="Widen the range, or open the issue for its own history."
            />
          ) : (
            <div className="flex flex-col">
              {touching.map((c) => {
                const mine = (c.issues ?? []).find((i) => i.identifier === identifier || i.id === identifier)
                const who = (c.agents ?? []).map((a) => a.name || a.slug || a.id)
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
                    <span className="min-w-0 truncate">
                      {c.routine_slug || c.started_by}
                      {who.length > 0 && (
                        <span className="text-muted-foreground-soft"> → {who.join(", ")}</span>
                      )}
                    </span>
                    <span className="shrink-0 font-mono text-[10px] text-muted-foreground-soft">
                      {mine?.created ? "created" : "changed"}
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

/* ------------------------------------------------------------------ *
 *  Run
 * ------------------------------------------------------------------ */

export interface RunDrillDownProps {
  workspaceId: string
  runID: string
  /** The routine it belongs to, when the caller knows — enables the steps card. */
  routineSlug?: string
}

interface RunMeta {
  agent_slug?: string
  agent_name?: string
  crew_name?: string
  crew_slug?: string
  mission_id?: string
  mission_identifier?: string
}

/** The §5 links of a run — routine, issue, agent, crew, journal — each through entityHref. */
export function runRelatedLinks(runID: string, routineSlug: string | undefined, meta: RunMeta | null) {
  const out: { label: string; href: string }[] = []
  if (routineSlug) out.push({ label: `Routine · ${routineSlug}`, href: entityHref({ kind: "routine", slug: routineSlug }) })
  if (meta?.mission_identifier) out.push({ label: `Issue · ${meta.mission_identifier}`, href: entityHref({ kind: "issue", identifier: meta.mission_identifier }) })
  if (meta?.agent_slug) out.push({ label: `Agent · ${meta.agent_name ?? meta.agent_slug}`, href: entityHref({ kind: "agent", slug: meta.agent_slug }) })
  if (meta?.crew_slug) out.push({ label: `Crew · ${meta.crew_name ?? meta.crew_slug}`, href: entityHref({ kind: "crew", slug: meta.crew_slug }) })
  out.push({ label: "Journal · this run", href: entityHref({ kind: "journal", traceId: runID }) })
  return out
}

export function RunDrillDown({ workspaceId, runID }: RunDrillDownProps) {
  return <RoutineRunDetail workspaceId={workspaceId} runId={runID} />
}

/* ------------------------------------------------------------------ *
 *  Agent
 * ------------------------------------------------------------------ */

export interface AgentDrillDownProps {
  workspaceId: string
  agentID: string
  /** The agent's slug — what its page is keyed on. Without it there is no page to link. */
  agentSlug?: string
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
export function AgentDrillDown({ workspaceId, agentID, agentSlug, name, chains, onOpenWorkflow }: AgentDrillDownProps) {
  void workspaceId

  const worked = React.useMemo(
    () => chains.filter((c) => (c.agents ?? []).some((a) => a.id === agentID)),
    [chains, agentID],
  )
  // assignmentsOf, not a local `?? 0`: a ref that arrived without a count still
  // means one piece of work. Three files decided this separately and two of them
  // decided differently, so the rail's row read "×1" while this page's strip
  // read "0 assignments" for the same agent in the same window.
  const assignments = worked.reduce((n, c) => {
    const mine = (c.agents ?? []).find((a) => a.id === agentID)
    return n + (mine ? assignmentsOf(mine) : 0)
  }, 0)
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
          {/* /agents/<id> was a dead route; the agent's page is keyed on its
              slug under /crews, and a link is only drawn when there is one. */}
          {agentSlug && (
            <a href={entityHref({ kind: "agent", slug: agentSlug })} className="text-[11px] text-primary hover:underline">
              Agent page ↗
            </a>
          )}
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
                      ×{mine ? assignmentsOf(mine) : 1}
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
