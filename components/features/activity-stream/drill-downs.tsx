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
import { Bot, CircleDot, Clock, ListTree, MessageSquare, type LucideIcon } from "lucide-react"

import { Appear, DetailCard, EmptyState, Pill, StatStrip, type StatItem } from "@/components/ui/detail"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { TypedRunDetail } from "@/components/features/activity/typed-run-detail"
import { entityHref } from "@/lib/entity-links"
import { formatDurationMs } from "@/lib/activity-stream"
import { relTime } from "@/lib/time"
import { assignmentsOf } from "@/lib/activity-lenses"
import type { ChainSummary } from "@/hooks/use-chains"
import { humanAction, useIssueTimeline } from "@/hooks/use-issue-timeline"
import { runStatusLabel } from "@/lib/activity-run"
import type { TimelineItem } from "@/lib/issue-timeline"
import { cn } from "@/lib/utils"

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
  /** Open a run from the history (#2983). */
  onOpenRun?: (runId: string) => void
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
export function IssueDrillDown({ workspaceId, issueId, label, chains, onOpenWorkflow, onOpenRun }: IssueDrillDownProps) {
  // The issue's own history, read by issue with its own paging (#2983). It
  // used to be derived from the loaded page of chains and their five capped
  // issue refs, so older history and the sixth issue simply were not there.
  const history = useIssueTimeline(workspaceId, issueId)

  // Which chains of the loaded window reached this issue — context, labelled
  // as such, and no longer the history. Matched on the id alone.
  const touching = React.useMemo(
    () => chains.filter((c) => (c.issues ?? []).some((i) => i.id === issueId)),
    [chains, issueId],
  )
  const created = touching.some((c) => (c.issues ?? []).some((i) => i.id === issueId && i.created))
  // The human handle, read off the issue itself and only then off a chain ref.
  // Absent on a workspace that does not use identifiers, which is a fact about
  // the workspace and not a lookup failure — see the link below.
  const identifier =
    history.issue?.identifier ||
    touching.flatMap((c) => c.issues ?? []).find((i) => i.id === issueId && i.identifier)?.identifier ||
    undefined
  const heading = identifier || history.issue?.title || label || issueId
  const agents = React.useMemo(
    () => [...new Set(touching.flatMap((c) => (c.agents ?? []).map((a) => a.name || a.slug || a.id)))],
    [touching],
  )

  const stats: StatItem[] = [
    { label: "Status", value: history.issue?.status ? humanAction(history.issue.status.toLowerCase()) : "—" },
    { label: "Origin", value: created ? "created by a run" : "—", tone: created ? "success" : "default" },
    { label: "Agents", value: agents.length > 0 ? agents.join(", ") : "—" },
    { label: "Last activity", value: history.items[0] ? relTime(history.items[0].at) : "—" },
  ]

  return (
    <Shell>
      <Appear order={0}>
        <div className="flex flex-wrap items-center gap-2">
          <CircleDot className="h-4 w-4 shrink-0 text-muted-foreground" />
          <h1 className="min-w-0 font-mono text-base font-semibold tracking-tight">{heading}</h1>
          {identifier && history.issue?.title && (
            <span className="min-w-0 truncate text-sm text-muted-foreground">{history.issue.title}</span>
          )}
          {created && <Pill tone="success">created by a run</Pill>}
          {/* Rendered only when there is an identifier to render it with.
              /issues/[identifier] resolves an identifier and nothing else, so a
              workspace that does not use them has no URL for this issue — and a
              button that leads to a 404 is worse than an absent one. */}
          {identifier && (
            <a
              href={`/issues/${encodeURIComponent(identifier)}`}
              className="ml-auto rounded-md border border-foreground/[0.08] px-2 py-1 text-[11px] text-primary transition-colors hover:bg-foreground/[0.04]"
            >
              Open issue ↗
            </a>
          )}
        </div>
      </Appear>

      <Appear order={1}>
        <StatStrip items={stats} />
      </Appear>

      <Appear order={2}>
        <section role="region" aria-label="History">
          <DetailCard title="History" subtitle="Events, comments and runs, newest first">
            {history.error ? (
              <EmptyState icon={CircleDot} title={history.error} description="The issue's own record could not be read." />
            ) : history.loading ? (
              <p role="status" className="py-6 text-center text-xs text-muted-foreground">
                Loading the issue's history…
              </p>
            ) : history.items.length === 0 && !history.canLoadOlder ? (
              <EmptyState icon={CircleDot} title="Nothing recorded yet" description="No event, comment or run on this issue." />
            ) : (
              <div className="flex flex-col">
                <ol className="flex flex-col">
                  {history.items.map((i) => (
                    <HistoryRow key={i.key} item={i} onOpenRun={onOpenRun} />
                  ))}
                </ol>
                {history.canLoadOlder && (
                  <button
                    type="button"
                    disabled={history.busy}
                    onClick={() => void history.loadOlder()}
                    className="mt-2 self-center rounded-md border border-foreground/[0.08] px-3 py-1 text-[11px] text-muted-foreground transition-colors hover:bg-foreground/[0.04] hover:text-foreground disabled:opacity-50"
                  >
                    {history.busy ? "Loading…" : "Load older"}
                  </button>
                )}
              </div>
            )}
          </DetailCard>
        </section>
      </Appear>

      <Appear order={3}>
        <DetailCard
          title="Workflows in the loaded window"
          subtitle={`${touching.length} ${touching.length === 1 ? "workflow" : "workflows"} · the rail's chains only, not the issue's history`}
        >
          {touching.length === 0 ? (
            <p className="text-xs text-muted-foreground">No chain in the loaded window reached this issue.</p>
          ) : (
            <div className="flex flex-col">
              {touching.map((c) => {
                const mine = (c.issues ?? []).find((i) => i.id === issueId)
                const who = (c.agents ?? []).map((a) => a.name || a.slug || a.id)
                return (
                  <button
                    key={c.origin}
                    type="button"
                    onClick={() => onOpenWorkflow(c.origin)}
                    className="grid grid-cols-[8px_1fr_auto_auto] items-center gap-3 rounded-md px-1.5 py-2 text-left text-[11.5px] transition-colors hover:bg-foreground/[0.03]"
                  >
                    <span
                      aria-hidden
                      className="h-1.5 w-1.5 rounded-full"
                      style={{ background: `var(${c.failed ? "--destructive" : "--success"})` }}
                    />
                    <span className="min-w-0 truncate">
                      {c.routine_slug || c.started_by}
                      {who.length > 0 && <span className="text-muted-foreground-soft"> → {who.join(", ")}</span>}
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

const HISTORY_ICON: Record<TimelineItem["kind"], LucideIcon> = {
  event: Clock,
  comment: MessageSquare,
  run: Bot,
}

function HistoryRow({ item, onOpenRun }: { item: TimelineItem; onOpenRun?: (runId: string) => void }) {
  const Icon = HISTORY_ICON[item.kind]
  const openable = item.kind === "run" && item.runId && onOpenRun
  const body = (
    <>
      <Icon className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
      <span className="min-w-0 flex-1">
        <span className="flex items-baseline gap-1.5 text-xs">
          <span className={cn("font-medium", openable && "group-hover:underline")}>{item.title}</span>
          {item.kind === "run" && item.action && (
            <span className="text-[10.5px] text-muted-foreground">{runStatusLabel(item.action)}</span>
          )}
          {item.actor && <span className="truncate text-[10.5px] text-muted-foreground-soft">· {item.actor}</span>}
        </span>
        {item.detail && <span className="mt-0.5 line-clamp-2 block text-[11.5px] text-muted-foreground">{item.detail}</span>}
      </span>
      <span className="w-16 shrink-0 text-right font-mono text-[10px] text-muted-foreground-soft" title={item.at}>
        {item.at ? relTime(item.at) : "—"}
      </span>
    </>
  )
  return (
    <li>
      {openable ? (
        <button
          type="button"
          onClick={() => onOpenRun!(item.runId!)}
          className="group flex w-full items-start gap-2.5 rounded-md px-1.5 py-2 text-left transition-colors hover:bg-foreground/[0.03]"
        >
          {body}
        </button>
      ) : (
        <div className="flex items-start gap-2.5 px-1.5 py-2">{body}</div>
      )}
    </li>
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
  return <TypedRunDetail key={`${workspaceId}:${runID}`} workspaceId={workspaceId} runId={runID} />
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
                    className="grid grid-cols-[8px_1fr_auto_auto] items-center gap-3 rounded-md px-1.5 py-2 text-left text-[11.5px] transition-colors hover:bg-foreground/[0.03]"
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
