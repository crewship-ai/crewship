// The workflow page's spine: a walked chain turned into rows you read
// downwards.
//
// GET /api/v1/chains/{anchor} returns a GRAPH — nodes and edges, in breadth-
// first discovery order from wherever you anchored. That is the right shape
// for a canvas and the wrong shape for the question the page was asked:
// "what happened, one under the other, in order". This module is the
// translation, and it is a pure function on purpose — the ordering, the
// indentation and the duration rules are the part that can be wrong, and a
// test that mounts a card and greps for text proves none of them.
//
// Two absences it must not paper over:
//
//   · Most nodes carry no time and never will. internal/chain stamps
//     `occurred_at` on the three kinds that are EVENTS — run, assignment,
//     inbox — and withholds it from the four that are NOUNS: an issue, a
//     routine, an agent and a rule all have a created_at, and none of them
//     answers "when did this happen in this chain". A timeline that filled the
//     gap from created_at would put a routine written in June at the top of a
//     chain that ran in August.
//   · An ABSENT duration is never 0. 0ms asserts the work was instant, which
//     is a different and stronger claim than "there is no span to derive".
//     The same rule settled chainElapsedMs client-side and chainElapsedMS /
//     spanMS server-side. A duration the server DID send is honoured exactly,
//     0 included — spanMS returns 0 only when both stamps parsed and the
//     interval genuinely rounded under a millisecond, and second-guessing that
//     would throw away the one case the pointer type exists to express.

import type { ChainEdge, ChainNode } from "@/lib/trace/build-chain-graph"
import type { ChainSummary } from "@/hooks/use-chains"
import { formatDurationMs } from "@/lib/activity-stream"

/* ------------------------------------------------------------------ *
 *  Wire shape
 * ------------------------------------------------------------------ */

/**
 * What the timeline needs out of GET /api/v1/chains/{anchor}.
 *
 * A structural subset of ChainGraph rather than the whole thing: the gaps and
 * the truncation notice belong to the picture above, and taking only what is
 * read here keeps a caller free to hand over any walk-shaped object.
 */
export interface TimelineSource {
  nodes: ChainNode[]
  edges: ChainEdge[]
}

/* ------------------------------------------------------------------ *
 *  Row shape
 * ------------------------------------------------------------------ */

/**
 * How long a row took, or the honest reason there is no number.
 *
 * Three states rather than `number | null`, because "still going" and "never
 * recorded" render differently and mean different things — collapsing them
 * is how a finished run ends up with a spinner next to it.
 */
export type RowTiming =
  | { state: "measured"; ms: number }
  | { state: "running" }
  | { state: "unknown" }

export interface TimelineRow {
  /** Graph-wide node id, "kind:ref". */
  id: string
  kind: string
  /** The row's primary key — what onOpenNode is called with. */
  ref: string
  /** Human handle: issue identifier, routine slug, agent slug. */
  key?: string
  /** User- and agent-written in several kinds. Render, never inject. */
  label: string
  status?: string
  /**
   * Indentation depth in the CAUSAL tree — 0 for a root.
   *
   * Deliberately not ChainNode.depth, which is hops from the anchor and so
   * reports the automation that STARTED a run as one step away from it in
   * either direction. Indenting by that draws the cause below the effect.
   */
  indent: number
  anchor: boolean
  partial: boolean
  partialReason?: string
  parentId?: string
  /** The edge kind that put this row under its parent. Undefined at a root. */
  via?: string
  /**
   * When this row happened — `occurred_at`, normalised UTC by the server.
   *
   * Undefined on every kind that is a noun rather than an event, which is
   * most of a chain. The row still renders; it just does not claim a time.
   */
  occurredAt?: string
  timing: RowTiming
  /**
   * The agent carrying this work out, when an `executes` edge names one and
   * something else is the causal parent. It rides on the row rather than
   * owning it — see the parent ranking below.
   */
  executedBy?: { id: string; ref: string; label: string }
}

export interface WorkflowTimeline {
  rows: TimelineRow[]
  /** True when at least one node carried a parseable timestamp. */
  timed: boolean
  /**
   * Rows with no `occurred_at` — the ones that cannot be placed on an axis.
   *
   * Surfaced rather than swallowed because it is normally most of the list:
   * every real walk taken on 2026-08-10 had exactly one or two datable nodes
   * among four. A reader who cannot see that will read the row order as a
   * sequence in time, and for those rows it is not one.
   */
  untimedCount: number
  /**
   * Wall clock across every datable moment in the chain, or null.
   *
   * Same rule as lib/activity-stream's chainElapsedMs and the server's
   * chainElapsedMS: first to last, NOT the sum of the rows' own durations,
   * which reads 0 for agentless work and double-counts a nested span.
   */
  elapsedMs: number | null
}

/* ------------------------------------------------------------------ *
 *  Parent selection
 * ------------------------------------------------------------------ */

/**
 * Which incoming edge decides where a node is indented, lowest wins.
 *
 * `runs` before `triggers` looks backwards for a module about causation, and
 * is the whole design:
 *
 *   The walk gives a rule-fired run TWO parents — the routine that defines it
 *   (`runs`) and the automation that fired it (`triggers`). Choosing the
 *   automation flattens every run of that routine into one fan of siblings
 *   and deletes the routine→run relation from the picture. Choosing the
 *   routine keeps the automation as an ANCESTOR regardless, because
 *   automation→routine is an edge in its own right — so the causal story
 *   survives and the structural one is gained.
 *
 * `executes` sits below `triggers` for the opposite reason: an agent carries
 * work out but did not cause it, and nesting assignments under agents turns a
 * sequence into a grouping by actor. The agent rides on the row instead
 * (TimelineRow.executedBy).
 *
 * `relates` is last because it is author-declared and has no direction at
 * all — it should never win a parent it can avoid.
 */
const PARENT_RANK: Record<string, number> = {
  runs: 0,
  triggers: 1,
  executes: 2,
  produces: 3,
  relates: 4,
}

/** An edge kind this module has never heard of still ranks — below all of them. */
const UNRANKED = 9

/* ------------------------------------------------------------------ *
 *  Time
 * ------------------------------------------------------------------ */

/**
 * Statuses that mean the work is in flight.
 *
 * Assignments spell theirs in upper case (prisma AssignmentStatus) and runs
 * in lower (pipeline.RunStatus), so everything is compared lowercased.
 * `interrupted` is deliberately absent: it is the boot-recovery marker for a
 * run the previous lifetime did not terminate — not finished, but certainly
 * not running.
 */
const IN_FLIGHT = new Set(["running", "queued", "pending", "waiting"])

function parseMs(ts: string | null | undefined): number | null {
  if (!ts) return null
  const t = Date.parse(ts)
  return Number.isFinite(t) ? t : null
}

/**
 * How long one row took.
 *
 * `duration_ms` is taken at face value, 0 included, and NOT recomputed from
 * the two stamps. The server already derived it as wall clock (chain.spanMS),
 * deliberately in preference to the pipeline_runs.duration_ms column, which is
 * NOT NULL DEFAULT 0 and rewritten at every step boundary; and it withheld the
 * field entirely wherever the span was underivable. Recomputing here would
 * only reintroduce the divergence between the two answers, and treating a
 * server-sent 0 as "missing" would erase the one case the pointer type exists
 * to carry: a run that finished inside a millisecond.
 *
 * `running` is the narrow claim it sounds like — it needs a start, no end, AND
 * an in-flight status. "No end recorded" alone is not evidence of life, and a
 * completed run with a spinner beside it is a worse error than a dash.
 *
 * Everything else is `unknown`. That is the majority case and not a defect:
 * four of the seven node kinds are nouns the server never dates.
 */
export function rowTiming(n: ChainNode): RowTiming {
  if (typeof n.duration_ms === "number" && Number.isFinite(n.duration_ms) && n.duration_ms >= 0) {
    return { state: "measured", ms: n.duration_ms }
  }

  const start = parseMs(n.occurred_at)
  if (start != null && !n.ended_at && IN_FLIGHT.has((n.status ?? "").toLowerCase())) {
    return { state: "running" }
  }

  return { state: "unknown" }
}

/**
 * The row's duration as a reader sees it.
 *
 * "0ms" appears only when the server said 0. An absent duration is an em dash
 * and work in flight says so in words; neither is ever rendered as a number.
 */
export function formatRowDuration(t: RowTiming): string {
  switch (t.state) {
    case "measured":
      return formatDurationMs(t.ms)
    case "running":
      return "running"
    default:
      return "—"
  }
}

/* ------------------------------------------------------------------ *
 *  The build
 * ------------------------------------------------------------------ */

/**
 * Orders one group of siblings.
 *
 * Time wins ONLY when every member of the group has one. The tempting
 * alternative — sort the datable ones and push the rest to an end — places a
 * row whose position is a guess, and nothing on the screen distinguishes that
 * guess from a fact. One undatable sibling therefore returns the whole group
 * to the order the walk returned it in, which is deterministic and claims
 * nothing about when.
 */
function orderGroup(group: string[], occurredOf: Map<string, number>): string[] {
  if (group.length < 2) return group
  if (!group.every((id) => occurredOf.has(id))) return group
  return [...group].sort((a, b) => {
    const d = (occurredOf.get(a) as number) - (occurredOf.get(b) as number)
    // Stable on a tie: two rows stamped in the same millisecond keep the
    // walk's order rather than swapping between renders.
    return d !== 0 ? d : group.indexOf(a) - group.indexOf(b)
  })
}

export function buildWorkflowTimeline(graph: TimelineSource): WorkflowTimeline {
  const nodes = graph.nodes ?? []
  const byId = new Map<string, ChainNode>()
  const order = new Map<string, number>()
  nodes.forEach((n, i) => {
    if (!byId.has(n.id)) {
      byId.set(n.id, n)
      order.set(n.id, i)
    }
  })

  // A dangling edge is a node the client failed to draw. The walker promises
  // not to emit one; this refuses to act on one either way.
  const edges = (graph.edges ?? []).filter((e) => byId.has(e.from) && byId.has(e.to))

  // ── parent per node, by edge rank then by discovery order ──────────
  const parent = new Map<string, { from: string; kind: string; rank: number }>()
  const executor = new Map<string, string>()
  for (const e of edges) {
    if (e.kind === "executes" && byId.get(e.from)?.kind === "agent") {
      if (!executor.has(e.to)) executor.set(e.to, e.from)
    }
    const rank = PARENT_RANK[e.kind] ?? UNRANKED
    const held = parent.get(e.to)
    if (
      !held ||
      rank < held.rank ||
      (rank === held.rank && (order.get(e.from) ?? 0) < (order.get(held.from) ?? 0))
    ) {
      parent.set(e.to, { from: e.from, kind: e.kind, rank })
    }
  }

  // ── children, in the walk's discovery order ────────────────────────
  const children = new Map<string, string[]>()
  const rootIds: string[] = []
  for (const n of nodes) {
    const p = parent.get(n.id)
    if (!p) {
      rootIds.push(n.id)
      continue
    }
    const list = children.get(p.from)
    if (list) list.push(n.id)
    else children.set(p.from, [n.id])
  }

  // ── time, read once ────────────────────────────────────────────────
  const occurredOf = new Map<string, number>()
  const moments: number[] = []
  for (const n of nodes) {
    const s = parseMs(n.occurred_at)
    const e = parseMs(n.ended_at)
    if (s != null) {
      occurredOf.set(n.id, s)
      moments.push(s)
    }
    if (e != null) moments.push(e)
  }

  // ── preorder walk ──────────────────────────────────────────────────
  const rows: TimelineRow[] = []
  const emitted = new Set<string>()

  const visit = (id: string, indent: number) => {
    if (emitted.has(id)) return
    const n = byId.get(id)
    if (!n) return
    emitted.add(id)

    const p = parent.get(id)
    const ex = executor.get(id)
    const exNode = ex && ex !== p?.from ? byId.get(ex) : undefined

    rows.push({
      id: n.id,
      kind: String(n.kind),
      ref: n.ref,
      key: n.key,
      label: n.label,
      status: n.status,
      indent,
      anchor: n.anchor === true,
      partial: n.partial === true,
      partialReason: n.partial_reason || undefined,
      parentId: p?.from,
      via: p?.kind,
      occurredAt: occurredOf.has(id) ? n.occurred_at : undefined,
      timing: rowTiming(n),
      executedBy: exNode
        ? { id: exNode.id, ref: exNode.ref, label: exNode.label }
        : undefined,
    })

    for (const child of orderGroup(children.get(id) ?? [], occurredOf)) visit(child, indent + 1)
  }

  for (const id of orderGroup(rootIds, occurredOf)) visit(id, 0)

  // A cycle among nodes no root reaches would otherwise vanish from a page
  // whose whole job is completeness. The earliest-discovered survivor is
  // promoted to a root instead, and the pass repeats until nothing is left.
  for (const n of nodes) {
    if (!emitted.has(n.id)) visit(n.id, 0)
  }

  const untimedCount = rows.filter((r) => !r.occurredAt).length

  // Null, not 0, when there is nothing to measure between — the rule
  // chainElapsedMs settled and chainElapsedMS repeats. Two identical moments
  // are one moment counted twice, not an instant workflow.
  let elapsedMs: number | null = null
  if (moments.length >= 2) {
    const span = Math.max(...moments) - Math.min(...moments)
    elapsedMs = span > 0 ? span : null
  }

  return { rows, timed: moments.length > 0, untimedCount, elapsedMs }
}

/* ------------------------------------------------------------------ *
 *  Header helpers
 * ------------------------------------------------------------------ */

/**
 * The headline duration, and the caveat that goes under it.
 *
 * `duration_ms` is null when the server found no span between the chain's
 * first and last activity — which, per chainElapsedMS, is the single run that
 * has not ended yet, because last_activity falls back to started_at. On a
 * chain nothing has failed in, that is "running".
 *
 * It is NOT running once something in it has failed: a failed run has
 * terminated, so this reports the missing span as missing rather than
 * inventing a live state for finished work. Both branches refuse 0.
 */
export function chainHeaderDuration(chain: ChainSummary): { text: string; note: string } {
  if (chain.duration_ms != null && Number.isFinite(chain.duration_ms) && chain.duration_ms > 0) {
    return { text: formatDurationMs(chain.duration_ms), note: "wall clock, first to last" }
  }
  if (chain.failed) return { text: "—", note: "no span recorded" }
  return { text: "running", note: "no end recorded yet" }
}

/** What kind of thing set the chain off, in words a reader recognises. */
const STARTER_NOUN: Record<string, string> = {
  automation: "Rule",
  user: "Person",
  schedule: "Schedule",
  webhook: "Webhook",
  issue: "Issue",
  run: "Run",
}

export function startedByPhrase(chain: ChainSummary): string {
  const noun = STARTER_NOUN[chain.started_by_kind]
  const who = (chain.started_by ?? "").trim()
  // "unknown" is a value the server sends deliberately — the trigger was not
  // recorded — so it is reported as an absence rather than dressed up.
  if (!noun || !who) return who || "Unrecorded"
  return `${noun} · ${who}`
}

/**
 * Status → a globals.css token, never a literal.
 *
 * The vocabulary is the rail's (activity-sidebar.tsx): completed is success,
 * failed is destructive, running is primary. A status this map has not heard
 * of gets the neutral token rather than a guessed colour.
 */
const STATUS_TOKEN: Record<string, string> = {
  completed: "--success",
  dry_run: "--success",
  active: "--success",
  enabled: "--success",
  failed: "--destructive",
  timeout: "--destructive",
  cancelled: "--destructive",
  deleted: "--destructive",
  running: "--primary",
  queued: "--primary",
  pending: "--primary",
  waiting: "--warn",
  interrupted: "--warn",
  disabled: "--muted-foreground",
}

export function rowStatusToken(status: string | undefined | null): string {
  if (!status) return "--muted-foreground-soft"
  return STATUS_TOKEN[status.toLowerCase()] ?? "--muted-foreground-soft"
}

/** How a row's placement is justified, for the row's own tooltip. */
const VIA_PHRASE: Record<string, string> = {
  runs: "run of",
  triggers: "triggered by",
  executes: "executed by",
  produces: "produced by",
  relates: "linked to",
}

export function viaPhrase(via: string | undefined): string | undefined {
  if (!via) return undefined
  return VIA_PHRASE[via] ?? via
}
