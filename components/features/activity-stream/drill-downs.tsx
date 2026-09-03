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
import { RunActivityTimeline } from "@/components/features/activity/run-activity-timeline"
import { usePipelineRunRecords } from "@/hooks/use-pipeline-run-records"
import { apiFetch } from "@/lib/api-fetch"
import { entityHref } from "@/lib/entity-links"
import { formatDurationMs } from "@/lib/activity-stream"
import { relTime } from "@/lib/time"
import { runHeadline } from "@/lib/run-digest"
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

/**
 * Which routine a run belongs to, asked of the journal.
 *
 * `?run=<id>` is the commonest inbound link in the product (the inbox, the
 * bell, the dashboard, a routine's run rows) and it carries no routine. The
 * run's own row lives in the routine's run-records list, so without the slug
 * the page could only say "this run's record is not loaded" over a run the
 * journal can name. Every entry a routine run emits carries the slug in its
 * payload; one small read resolves it. Null while asking, "" when the journal
 * holds nothing for that id.
 */
function useRoutineSlugOfRun(workspaceId: string, runID: string, known?: string): string | null {
  const [slug, setSlug] = React.useState<string | null>(known ?? null)
  React.useEffect(() => {
    if (known) {
      setSlug(known)
      return
    }
    let cancelled = false
    setSlug(null)
    const qs = new URLSearchParams({ workspace_id: workspaceId, run_id: runID, limit: "20" })
    apiFetch(`/api/v1/journal?${qs.toString()}`)
      .then(async (r) => (r.ok ? await r.json() : null))
      .then((body: { entries?: Array<{ payload?: Record<string, unknown>; refs?: Record<string, unknown> }> } | null) => {
        if (cancelled) return
        for (const e of body?.entries ?? []) {
          const bag = { ...(e.payload ?? {}), ...(e.refs ?? {}) }
          const found = bag["pipeline_slug"] ?? bag["routine_slug"]
          if (typeof found === "string" && found) {
            setSlug(found)
            return
          }
        }
        setSlug("")
      })
      .catch(() => {
        if (!cancelled) setSlug("")
      })
    return () => {
      cancelled = true
    }
  }, [workspaceId, runID, known])
  return slug
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
/**
 * A run's status as a tone, for the strip and the pill.
 *
 * `failed ? destructive : success` was the whole rule, so a run that was
 * cancelled, interrupted or still going rendered GREEN beside the word naming
 * its state — the strip's one job is to be readable at a glance, and a green
 * "cancelled" is read as fine. An unknown status gets the neutral tone rather
 * than a guess, the same rule rowStatusToken follows for colours.
 */
function runStatusTone(status: string): StatItem["tone"] {
  switch (status.toLowerCase()) {
    case "completed":
      return "success"
    case "failed":
    case "timeout":
      return "destructive"
    case "cancelled":
    case "interrupted":
    case "waiting":
      return "warn"
    default:
      return "default"
  }
}

/**
 * What the journal knows about a run as an AGENT run: who ran it, for which
 * crew, on which issue. GET /api/v1/runs/{id} answers for runs the mission
 * engine dispatched (they carry `mission_identifier`) and 404s for a routine
 * run, which has no run.* entries — so the links are drawn from what comes
 * back and nothing is invented for the other kind.
 */
interface RunMeta {
  agent_slug?: string
  agent_name?: string
  crew_name?: string
  crew_slug?: string
  mission_id?: string
  mission_identifier?: string
}

function useRunMeta(runID: string): RunMeta | null {
  const [meta, setMeta] = React.useState<RunMeta | null>(null)
  React.useEffect(() => {
    let cancelled = false
    setMeta(null)
    apiFetch(`/api/v1/runs/${encodeURIComponent(runID)}`)
      .then(async (r) => (r.ok ? ((await r.json()) as RunMeta) : null))
      .then((m) => {
        if (!cancelled) setMeta(m)
      })
      .catch(() => {
        if (!cancelled) setMeta(null)
      })
    return () => {
      cancelled = true
    }
  }, [runID])
  return meta
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

export function RunDrillDown({ workspaceId, runID, routineSlug: knownSlug }: RunDrillDownProps) {
  const meta = useRunMeta(runID)
  const resolved = useRoutineSlugOfRun(workspaceId, runID, knownSlug)
  const routineSlug = resolved || undefined
  const resolving = resolved === null
  const { records, loading: recordsLoading } = usePipelineRunRecords(workspaceId, routineSlug ?? null)
  const loading = resolving || recordsLoading
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
        { label: "Status", value: run.status, tone: runStatusTone(run.status) },
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
          {run && <Pill tone={runStatusTone(run.status)}>{run.status}</Pill>}
          {routineSlug && <Pill tone="default">{routineSlug}</Pill>}
        </div>
        {/* Where this run leads — the second leg of the one timeline. A run
            used to be a dead end: nothing on this page linked its issue, its
            agent or its journal. */}
        <div className="mt-2 flex flex-wrap items-center gap-1.5" data-testid="run-related-links">
          {runRelatedLinks(runID, routineSlug, meta).map((l) => (
            <a
              key={l.href}
              href={l.href}
              className="inline-flex items-center gap-1 rounded-md border border-border/60 px-2 py-0.5 text-[11px] text-foreground hover:border-primary hover:no-underline"
            >
              {l.label}
              <span aria-hidden className="text-muted-foreground">→</span>
            </a>
          ))}
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

      {!loading && !run && (
        <Appear order={2}>
          <EmptyState
            icon={Terminal}
            title={routineSlug ? "This run is not in its routine's records" : "No routine claims this run"}
            description={
              routineSlug
                ? `The routine “${routineSlug}” keeps its newest runs on file and this one is older than that window. Its steps, as the journal recorded them, are below.`
                : "The journal holds no routine entries for this id, so there is no run record to show. Whatever it did record is below."
            }
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
