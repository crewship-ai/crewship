"use client"

// One workflow, opened as its own page.
//
// This is what a row in the Activity rail leads to. A chain is not an event —
// it is a rule firing, a routine running, agents being dispatched and issues
// changing under all of it — and the question a reader arrives with is the
// plainest one there is: what happened, one under the other, in the order it
// went, and let me click into any of it.
//
// So the page answers five questions, in this order, and nothing else:
//
//   1. What was this?          the header — who started it, when, how long
//   2. Which runs?             the Runs card — the list, and a way into each
//   3. How did it happen?      the causal graph (TopologyCard, already built)
//   4. What happened, then?    the timeline — the part that did not exist
//   5. What did it touch?      the issues and agents, clickable
//
// Runs sits above the picture because it is the shape a reader arrives already
// knowing: /routines opens a RUNS dock under every routine and each line there
// is a link into that run. The picture is what a workflow LOOKS like; the list
// is what you can do with it.
//
// The timeline's shaping — ordering, indentation, durations, and the
// running-versus-zero rule — lives in lib/workflow-timeline as a pure
// function. That is on purpose: those are the rules that can be wrong, and
// asserting on rendered text proves none of them.

import * as React from "react"
import {
  AlertTriangle,
  BookOpen,
  Bot,
  ChevronRight,
  CircleDot,
  Clock,
  Inbox,
  ListTree,
  ScrollText,
  TriangleAlert,
  Zap,
  type LucideIcon,
} from "lucide-react"

import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { Appear } from "@/components/ui/detail"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { apiFetch } from "@/lib/api-fetch"
import { formatDurationMs } from "@/lib/activity-stream"
import { statusIcon } from "@/lib/activity/run-status"
import { relTime } from "@/lib/time"
import type { ChainSummary } from "@/hooks/use-chains"
import { workflowName } from "@/lib/activity-lenses"
import {
  buildWorkflowTimeline,
  chainHeaderDuration,
  workflowRuns,
  formatRowDuration,
  rowStatusToken,
  startedByPhrase,
  viaPhrase,
  type TimelineSource,
  type TimelineRow,
} from "@/lib/workflow-timeline"

import { TopologyCard } from "./topology-card"

export interface WorkflowPageProps {
  workspaceId: string
  chain: ChainSummary
  onBack: () => void
  /**
   * The routine's human name, resolved by the shell from the loaded pipelines
   * list. Passed rather than looked up here so the rail row and this heading
   * cannot name one workflow two ways — which is what happened while the rail
   * read the name and the page read the slug.
   */
  routineName?: string
  /** Drill down: "issue" | "run" | "agent" | "assignment" | … plus the ref. */
  onOpenNode: (kind: string, ref: string) => void
}

/**
 * Icon per chain node kind.
 *
 * Taken from lib/concept-icons where the concept already has one (issues is a
 * CircleDot, routines a ScrollText, runs a BookOpen) so the timeline does not
 * introduce a second face for a thing the nav has already named. The three
 * kinds with no entry there — agent, assignment, automation — reuse the
 * glyphs the chain canvas already draws them with.
 */
const KIND_ICON: Record<string, LucideIcon> = {
  issue: CircleDot,
  routine: ScrollText,
  run: BookOpen,
  assignment: Bot,
  agent: Bot,
  inbox: Inbox,
  automation: Zap,
}

/** Left inset per level of causal depth, in pixels. */
const INDENT_STEP = 18

export function WorkflowPage({ workspaceId, chain, routineName, onBack, onOpenNode }: WorkflowPageProps) {
  // The walk, for the sequence below. TopologyCard fetches the same anchor for
  // the picture; it owns its own request and takes no data prop, and reaching
  // into it to share one would be a change to a file another workstream owns.
  // Two GETs of one cacheable read is the smaller cost.
  const [graph, setGraph] = React.useState<TimelineSource | null>(null)
  const [loading, setLoading] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)
  const [attempt, setAttempt] = React.useState(0)

  const anchor = chain.origin

  React.useEffect(() => {
    if (!anchor) return
    let cancelled = false
    setLoading(true)
    setError(null)
    apiFetch(
      `/api/v1/chains/${encodeURIComponent(anchor)}?workspace_id=${encodeURIComponent(workspaceId)}`,
    )
      .then(async (r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return (await r.json()) as TimelineSource
      })
      .then((d) => {
        if (!cancelled) setGraph(d)
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : "could not load the sequence")
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [anchor, workspaceId, attempt])

  const timeline = React.useMemo(
    () => (graph ? buildWorkflowTimeline(graph) : null),
    [graph],
  )

  // The runs, out of the same walk. See workflowRuns for why this is not a
  // second request against the endpoint the sequence already read.
  const runs = React.useMemo(() => (graph ? workflowRuns(graph) : []), [graph])

  const headline = workflowName(chain, routineName)
  const duration = chainHeaderDuration(chain)
  const issues = chain.issues ?? []
  const agents = chain.agents ?? []
  const touchedNothing = chain.issue_count === 0 && chain.agent_count === 0

  return (
    <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">
      {/* ── 1. What was this? ─────────────────────────────────────── */}
      <Appear order={0}>
        {/* No Back button here. The shell renders the activity trail directly
            above this page — with its own Back and the whole path the reader
            came through — so a second one two rows down is two controls for one
            action, and the one that only goes up a single level is the weaker
            of the two. /routines carries a back-bar because it has no trail;
            this has one. `onBack` is still taken, for the empty state below
            where the trail is not the answer. */}
        <div className="flex flex-col gap-2">
          <div className="flex flex-wrap items-center gap-2">
            <span
              className="rounded border border-white/[0.08] px-1.5 py-px font-mono text-[10px] uppercase tracking-wider"
              style={{ color: `var(${chain.failed ? "--destructive" : "--primary"})` }}
            >
              {chain.failed ? "failed" : "workflow"}
            </span>
            {/* Routine slugs and rule names are author-written; React escapes
                them, and nothing on this page renders HTML from the server. */}
            <h1 className="min-w-0 text-lg font-semibold tracking-tight">{headline}</h1>
          </div>

          <p className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            <span>{startedByPhrase(chain)}</span>
            {chain.triggered_via && (
              <>
                <span>·</span>
                <span className="font-mono">{chain.triggered_via}</span>
              </>
            )}
            <span>·</span>
            <span>{new Date(chain.first_activity).toLocaleString(undefined, { hour12: false })}</span>
            <span>·</span>
            <span>{relTime(chain.first_activity)}</span>
            <span>·</span>
            <span className="font-mono text-muted-foreground-soft">{chain.origin}</span>
          </p>
        </div>
      </Appear>

      <Appear order={1}>
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          {/* Wall clock, first activity to last — the same measure
              chainElapsedMs settled and the server repeats. Never 0: an
              unmeasurable span says so in words. */}
          <KpiCard label="Duration" value={duration.text} subtitle={duration.note} />
          <KpiCard
            label="Runs"
            value={chain.runs}
            subtitle={
              chain.failed_runs > 0
                ? `${chain.failed_runs} failed`
                : chain.max_chain_depth > 0
                  ? `composed, depth ${chain.max_chain_depth}`
                  : "none failed"
            }
          />
          <KpiCard
            label="Issues touched"
            value={chain.issue_count}
            subtitle={chain.issue_count === 0 ? "none" : "created or changed"}
          />
          <KpiCard
            label="Agents"
            value={chain.agent_count}
            subtitle={chain.agent_count === 0 ? "agentless" : "dispatched"}
          />
        </div>
      </Appear>

      {/* ── 2. Which runs, and open one ───────────────────────────────
          The first card under the header, because it is the one a reader
          arrives already knowing how to use: /routines opens a RUNS dock
          at the bottom of every routine and each line there is a link
          straight into that run. This page began with the picture and
          did not list the runs at all — they were dissolved into the
          sequence below, between routines and agents, where "which runs
          were there" cannot be read off at a glance.

          Derived from the walk already fetched for the sequence; see
          workflowRuns for why this is not a second request. */}
      <Appear order={2}>
        <DashboardCard
          // A real landmark, not a test hook: the page is four stacked cards of
          // near-identical rows, and without a name a screen reader announces
          // four unlabelled groups. It also lets an assertion say "this many
          // dashes IN THE SEQUENCE" instead of counting the whole page, which
          // is what made adding this card break two tests about another one.
          role="region"
          aria-label="Runs"
          title="Runs"
          icon={BookOpen}
          hint={
            runs.length > 0
              ? `${runs.length} · open one to see its steps`
              : loading
                ? "walking…"
                : undefined
          }
        >
          {runs.length === 0 ? (
            <div className="flex flex-col items-center gap-1.5 py-8 text-center">
              <BookOpen className="h-4 w-4 text-muted-foreground-soft" />
              <p className="max-w-[380px] text-[11px] leading-relaxed text-muted-foreground-soft">
                {loading
                  ? "Walking the chain…"
                  : // Reachable and not an error: a chain rooted at agent work
                    // holds assignments and no runs at all.
                    "No routine ran in this workflow. The work it holds is agent work — it is in the sequence below."}
              </p>
            </div>
          ) : (
            <div className="flex flex-col">
              {runs.map((run) => (
                <button
                  key={run.id}
                  type="button"
                  onClick={() => onOpenNode("run", run.id)}
                  className="group grid grid-cols-[auto_1fr_auto] items-center gap-3 rounded-md px-1.5 py-2 text-left transition-colors hover:bg-white/[0.03] md:grid-cols-[auto_1fr_auto_auto_auto]"
                >
                  <span
                    aria-hidden
                    className="h-1.5 w-1.5 shrink-0 rounded-full"
                    style={{ background: `var(${rowStatusToken(run.status)})` }}
                  />
                  <span className="min-w-0">
                    <span className="block truncate font-mono text-[11px] text-foreground/85">{run.id}</span>
                    <span className="block truncate text-[10px] uppercase tracking-wide text-muted-foreground-soft">
                      {run.status || "—"}
                    </span>
                  </span>
                  <span className="hidden font-mono text-[10.5px] tabular-nums text-muted-foreground-soft md:block">
                    {run.occurredAt
                      ? new Date(run.occurredAt).toLocaleTimeString(undefined, { hour12: false })
                      : "—"}
                  </span>
                  <span className="w-14 text-right font-mono text-[10.5px] tabular-nums text-muted-foreground">
                    {formatRowDuration(run.timing)}
                  </span>
                  <ChevronRight className="hidden h-3.5 w-3.5 shrink-0 text-muted-foreground-soft transition-colors group-hover:text-foreground md:block" />
                </button>
              ))}
            </div>
          )}
        </DashboardCard>
      </Appear>

      {/* ── 3. How it happened ────────────────────────────────────── */}
      <Appear order={3}>
        <TopologyCard
          workspaceId={workspaceId}
          anchor={anchor}
          anchorLabel={headline}
          onOpenNode={onOpenNode}
        />
      </Appear>

      {/* ── 4. What happened, in sequence ─────────────────────────── */}
      <Appear order={4}>
        <DashboardCard
          role="region"
          aria-label="What happened, in sequence"
          title="What happened, in sequence"
          icon={ListTree}
          hint={
            loading && !timeline
              ? "walking…"
              : timeline
                ? `${timeline.rows.length} ${timeline.rows.length === 1 ? "step" : "steps"}${
                    timeline.elapsedMs != null ? ` · ${formatDurationMs(timeline.elapsedMs)}` : ""
                  }`
                : ""
          }
        >
          {loading && !timeline && (
            <div className="flex items-center justify-center gap-2 py-10 text-xs text-muted-foreground">
              <Spinner className="h-3.5 w-3.5" /> Walking the chain…
            </div>
          )}

          {error && (
            <div className="flex flex-col items-center gap-2 py-10 text-center">
              <TriangleAlert className="h-4 w-4 text-destructive" />
              <p className="text-[11px] text-muted-foreground">
                Could not load the sequence: {error}
              </p>
              <Button size="sm" variant="outline" onClick={() => setAttempt((a) => a + 1)}>
                Try again
              </Button>
            </div>
          )}

          {timeline && timeline.rows.length === 0 && !loading && (
            <div className="flex flex-col items-center gap-1.5 py-10 text-center">
              <Clock className="h-4 w-4 text-muted-foreground-soft" />
              <p className="max-w-[340px] text-[11px] text-muted-foreground-soft">
                The walk returned nothing to sequence. A chain appears here once a rule, a routine
                or an agent has touched this workflow.
              </p>
            </div>
          )}

          {timeline && timeline.rows.length > 0 && (
            <div className="flex flex-col">
              {timeline.rows.map((row) => (
                <TimelineRowView key={row.id} row={row} onOpenNode={onOpenNode} />
              ))}

              {/* The honest edge of this list. Same principle as the
                  topology card's gaps: a reader who cannot tell an ordering
                  from a guess will read the guess as fact. */}
              {timeline.untimedCount > 0 && (
                <p className="mt-2.5 flex items-start gap-1.5 border-t border-white/[0.06] pt-2.5 text-[10.5px] text-muted-foreground-soft">
                  <AlertTriangle className="mt-px h-2.5 w-2.5 shrink-0 text-warn" />
                  <span>
                    {timeline.untimedCount === timeline.rows.length
                      ? "Nothing in this chain is dated, so these rows are in causal order only: the indentation is what caused what, and the order within a level is the walk's, not a claim about when."
                      : `${timeline.untimedCount} of ${timeline.rows.length} steps are not dated — the server times the events (runs, agent work, inbox items) and not the routines, rules and agents behind them, whose creation date is a different fact from when this happened. A level containing one keeps the walk's order rather than being placed by a time nobody recorded.`}
                  </span>
                </p>
              )}
            </div>
          )}
        </DashboardCard>
      </Appear>

      {/* ── 4. What it touched ────────────────────────────────────── */}
      <Appear order={4}>
        <DashboardCard
          title="What it touched"
          icon={CircleDot}
          hint={`${chain.issue_count} ${chain.issue_count === 1 ? "issue" : "issues"} · ${
            chain.agent_count
          } ${chain.agent_count === 1 ? "agent" : "agents"}`}
        >
          {touchedNothing ? (
            <div className="flex flex-col items-center gap-1.5 py-8 text-center">
              <CircleDot className="h-4 w-4 text-muted-foreground-soft" />
              <p className="max-w-[340px] text-[11px] text-muted-foreground-soft">
                This workflow changed no issue and dispatched no agent. That is a fact about the
                run, not a gap in the record.
              </p>
            </div>
          ) : (
            <div className="flex flex-col gap-3">
              {chain.issue_count > 0 && (
                <TouchedGroup
                  label={`Issues (${chain.issue_count})`}
                  hidden={chain.issue_count - issues.length}
                >
                  {issues.map((i) => (
                    <button
                      key={i.id}
                      type="button"
                      // The row's primary key, the same value a timeline row
                      // hands over, so a caller gets one kind of reference
                      // from both halves of the page.
                      onClick={() => onOpenNode("issue", i.id)}
                      className="inline-flex max-w-full items-center gap-1.5 rounded bg-white/[0.05] px-1.5 py-1 text-left text-[11px] transition-colors hover:bg-white/[0.1]"
                    >
                      <CircleDot
                        className="h-3 w-3 shrink-0"
                        style={{ color: `var(${i.created ? "--success" : "--muted-foreground"})` }}
                      />
                      {i.identifier && (
                        <span className="font-mono text-[10.5px] text-muted-foreground-soft">
                          {i.identifier}
                        </span>
                      )}
                      <span className="truncate">{i.title || i.identifier || i.id}</span>
                      <span className="shrink-0 text-[10px] text-muted-foreground-soft">
                        {i.created ? "created" : "changed"}
                      </span>
                    </button>
                  ))}
                </TouchedGroup>
              )}

              {chain.agent_count > 0 && (
                <TouchedGroup
                  label={`Agents (${chain.agent_count})`}
                  hidden={chain.agent_count - agents.length}
                >
                  {agents.map((a) => (
                    <button
                      key={a.id}
                      type="button"
                      onClick={() => onOpenNode("agent", a.id)}
                      className="inline-flex max-w-full items-center gap-1.5 rounded bg-white/[0.05] px-1.5 py-1 text-left text-[11px] transition-colors hover:bg-white/[0.1]"
                    >
                      <Bot className="h-3 w-3 shrink-0 text-muted-foreground" />
                      <span className="truncate">{a.name || a.slug || a.id}</span>
                      {a.assignments > 1 && (
                        <span className="shrink-0 font-mono text-[10px] text-muted-foreground-soft">
                          ×{a.assignments}
                        </span>
                      )}
                    </button>
                  ))}
                </TouchedGroup>
              )}
            </div>
          )}
        </DashboardCard>
      </Appear>

      <Appear order={5}>
        <div className="flex justify-center">
          <Button size="sm" variant="outline" onClick={onBack}>
            Back to activity
          </Button>
        </div>
      </Appear>
    </div>
  )
}

/* ------------------------------------------------------------------ *
 *  Pieces
 * ------------------------------------------------------------------ */

function TouchedGroup({
  label,
  hidden,
  children,
}: {
  label: string
  /** issue_count/agent_count minus what the row actually carries. */
  hidden: number
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground-soft">
        {label}
      </span>
      <div className="flex flex-wrap gap-1.5">
        {children}
        {/* The list is capped server-side at MaxChainSummaryRefs. Saying so
            is the difference between a short list and a wrong one. */}
        {hidden > 0 && (
          <span className="inline-flex items-center rounded px-1.5 py-1 text-[11px] text-muted-foreground-soft">
            +{hidden} more, not carried on this row
          </span>
        )}
      </div>
    </div>
  )
}

function TimelineRowView({
  row,
  onOpenNode,
}: {
  row: TimelineRow
  onOpenNode: (kind: string, ref: string) => void
}) {
  const Icon = KIND_ICON[row.kind] ?? ListTree
  const StatusIcon = statusIcon((row.status ?? "").toLowerCase())
  const token = rowStatusToken(row.status)
  const via = viaPhrase(row.via)
  const executedBy = row.executedBy

  return (
    // A div, not a button, because the executing agent inside it is its own
    // target and an interactive element nested in an interactive element is
    // neither valid nor keyboard-navigable.
    <div
      className="group flex items-center gap-2 border-l-2 pl-2 transition-colors hover:bg-white/[0.03]"
      // The indent goes on the CONTENT, not on the row: indenting the row
      // drags the time column right with it, and a timeline whose clock
      // staircases down the page cannot be read as a clock at all. Time on the
      // left, duration on the right, both in fixed gutters; only the middle
      // moves.
      style={{ borderColor: row.anchor ? "var(--primary)" : "transparent" }}
    >
      {/* When it happened. An em dash where a timestamp is missing — a blank
          cell reads as a rendering fault, and a fabricated time reads as
          knowledge. */}
      <span
        className="w-[68px] shrink-0 text-right font-mono text-[10.5px] tabular-nums text-muted-foreground-soft"
        title={
          row.occurredAt
            ? new Date(row.occurredAt).toLocaleString(undefined, { hour12: false })
            : "the server does not date this kind of node"
        }
      >
        {row.occurredAt
          ? new Date(row.occurredAt).toLocaleTimeString(undefined, { hour12: false })
          : "—"}
      </span>

      <button
        type="button"
        onClick={() => onOpenNode(row.kind, row.ref)}
        className="flex min-w-0 flex-1 items-center gap-2 rounded py-1.5 pr-1 text-left"
        style={{ paddingLeft: 4 + row.indent * INDENT_STEP }}
      >
        <Icon className="h-3.5 w-3.5 shrink-0" style={{ color: `var(${token})` }} />
        <span
          data-testid="timeline-kind"
          className="shrink-0 font-mono text-[9.5px] uppercase tracking-wider text-muted-foreground-soft"
        >
          {row.kind}
        </span>
        {/* Labels are user- and agent-written (issue titles, assignment
            tasks, rule names). Rendered as text, never as markup. */}
        <span className="truncate text-[12px] text-foreground/90 group-hover:underline">
          {row.label}
        </span>
        {row.anchor && (
          <span className="shrink-0 rounded px-1 text-[9.5px] uppercase tracking-wider" style={{ color: "var(--primary)" }}>
            you came from here
          </span>
        )}
        {via && (
          <span className="shrink-0 text-[10px] text-muted-foreground-soft">{via}</span>
        )}
        {row.partial && (
          // The walker's own words for why it could not expand this node.
          <span
            className="shrink-0"
            title={row.partialReason || "this node's expansion is incomplete"}
          >
            <TriangleAlert className="h-2.5 w-2.5 text-warn" aria-label="incomplete" />
          </span>
        )}
      </button>

      {executedBy && (
        <button
          type="button"
          onClick={() => onOpenNode("agent", executedBy.ref)}
          className="hidden shrink-0 items-center gap-1 rounded bg-white/[0.05] px-1.5 py-0.5 text-[10.5px] text-muted-foreground transition-colors hover:bg-white/[0.1] sm:inline-flex"
        >
          <Bot className="h-2.5 w-2.5" />
          {executedBy.label}
        </button>
      )}

      {row.status && (
        <span
          className="hidden shrink-0 items-center gap-1 text-[10px] sm:inline-flex"
          style={{ color: `var(${token})` }}
        >
          <StatusIcon className="h-2.5 w-2.5" />
          {row.status.toLowerCase()}
        </span>
      )}

      {/* How long. "running" and "—" are different claims and neither is 0. */}
      <span className="w-[62px] shrink-0 text-right font-mono text-[10.5px] tabular-nums text-muted-foreground">
        {formatRowDuration(row.timing)}
      </span>
    </div>
  )
}
