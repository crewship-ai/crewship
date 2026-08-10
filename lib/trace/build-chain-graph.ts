// The cross-feature topology, laid out from a walked chain.
//
// buildOverviewGraph answers "what does this workspace look like" — three
// columns of everything. This answers a different question: "how did THIS
// happen", walked outward from one anchor by GET /api/v1/chains/{anchor},
// which follows the links the schema actually carries and declares the ones
// it does not.
//
// Rendering reuses the canvas's existing node components rather than a
// second set. Two node vocabularies for one picture is how the run trace
// and the routine preview drifted apart before they were merged back.

import { type Edge, type Node } from "@xyflow/react"
import { Graph as DagreGraph, layout as dagreLayout } from "@dagrejs/dagre"

import {
  OVERVIEW_NODE_HEIGHT,
  OVERVIEW_NODE_WIDTH,
  type OverviewNodeType,
} from "./build-overview-graph"

/**
 * The inset fitView leaves around the graph, as a fraction of the frame per
 * side.
 *
 * It lives here rather than next to the `<ReactFlow>` that consumes it
 * because the CARD sizes its frame from `bounds` and has to know the
 * fraction that will then be taken out of it — and the card must not import
 * the canvas module, which pulls React Flow (~200 KB) out of the lazy chunk
 * it was split into. One number, in the module both sides already import.
 */
export const CHAIN_FIT_PADDING = 0.18

/* ------------------------------------------------------------------ *
 *  The wire shape — mirrors internal/chain.Graph
 * ------------------------------------------------------------------ */

export type ChainNodeKind =
  | "issue"
  | "routine"
  | "run"
  | "assignment"
  | "agent"
  | "inbox"
  | "automation"

export interface ChainNode {
  id: string
  kind: ChainNodeKind | string
  ref: string
  key?: string
  label: string
  status?: string
  depth: number
  anchor?: boolean
  partial?: boolean
  partial_reason?: string

  /**
   * When this node happened, and how long it took. All three are OPTIONAL and
   * their absence is load-bearing: the server withholds them from any kind
   * that cannot answer honestly, rather than sending a zero.
   *
   * Only `run`, `assignment` and `inbox` are events and carry them:
   * `pipeline_runs.started_at`/`ended_at`, `assignments.started_at`/
   * `finished_at`, and `inbox_items.created_at` (an instant with no span —
   * `resolved_at` measures how long a human took, not how long the work took).
   *
   * `issue`, `routine`, `agent` and `automation` are NOUNS and carry nothing.
   * Their tables all have a created_at, but "when the issue was filed" / "when
   * the routine was written" / "when the agent was hired" / "when the rule was
   * authored" is a different fact from when anything in this chain happened.
   * When a rule fired is the `occurred_at` of the run it caused.
   *
   * Never substitute 0 or "" for an absent value on the way to a chart:
   * `new Date(0)` is 1 January 1970 and sorts above everything real.
   * `occurred_at`/`ended_at` are normalised UTC RFC3339 (fixed-width, always
   * `Z`), so `new Date(s)` is safe. `duration_ms` is milliseconds and may
   * legitimately be 0 — a run that finished inside a millisecond — which is a
   * different answer from absent, so test for `undefined`, not falsiness.
   */
  occurred_at?: string
  ended_at?: string
  duration_ms?: number
}

export interface ChainEdge {
  from: string
  to: string
  kind: string
}

export interface ChainGap {
  from: string
  to: string
  reason: string
}

export interface ChainGraph {
  anchor: string
  nodes: ChainNode[]
  edges: ChainEdge[]
  gaps: ChainGap[]
  truncated: boolean
  truncated_by?: string
}

/* ------------------------------------------------------------------ *
 *  Kind → node type
 * ------------------------------------------------------------------ */

// The walker's kinds and the canvas's node types were built from two
// different bases and did not line up: the walk returned `assignment`, which
// had no component, and a component existed for `automation`, which the walk
// did not return. The walk returns automations now, so both sides are whole;
// the mapping stays explicit anyway, because a kind with no entry falls back
// to the run card rather than vanishing, and a missing node in a picture
// whose job is completeness is worse than an approximate one.
//
// `assignment` still borrows the run card. It IS a run of agent work — the
// mission engine's half of the substrate — and the label says which it is,
// so it does not mislead. Its own card is a want, not a correctness gap.
const NODE_TYPE_BY_KIND: Record<string, OverviewNodeType> = {
  issue: "overviewIssue",
  routine: "overviewRoutine",
  run: "overviewRun",
  assignment: "overviewRun",
  agent: "overviewAgent",
  inbox: "overviewInbox",
  automation: "overviewAutomation",
}

/** Edge stroke per relationship, as globals.css tokens. Never a literal. */
const EDGE_TOKEN: Record<string, string> = {
  triggers: "--gold",
  runs: "--info",
  executes: "--primary",
  produces: "--warn",
  relates: "--muted-foreground",
}

const DASHED_EDGE_KINDS = new Set(["triggers", "produces", "relates"])

/**
 * The timing fields, carried through under the names the cards already use.
 *
 * Spread onto every kind rather than only the two that can fill it, so a kind
 * that gains a time server-side does not need a second edit here — and so the
 * absent case is uniform: `startedAt` falls back to `""` because that is what
 * the run card has always been handed and what it already renders as "no time",
 * while `durationMs` stays UNDEFINED. Defaulting the duration to 0 would be the
 * bug the server went out of its way to avoid: 0 reads as "instant", and a run
 * that is still going is not instant.
 */
function timingFor(n: ChainNode): Record<string, unknown> {
  return {
    startedAt: n.occurred_at ?? "",
    endedAt: n.ended_at ?? "",
    durationMs: n.duration_ms,
  }
}

/**
 * Node data per kind.
 *
 * The chain response is deliberately thin — id, kind, label, status, and now
 * when the node happened — so the walker does not have to know what a card
 * wants to draw. That means some fields the components accept simply are not
 * available here, and this fills what it can rather than inventing the rest:
 * an invented invocation count looks like knowledge, and so does an invented
 * timestamp.
 */
function dataFor(n: ChainNode): Record<string, unknown> {
  const common = {
    isAnchor: n.anchor === true,
    partial: n.partial === true,
    partialReason: n.partial_reason ?? "",
    depth: n.depth,
    ...timingFor(n),
  }
  switch (n.kind) {
    case "issue":
      return { ...common, identifier: n.key ?? n.ref, title: n.label, status: n.status ?? "", hasRoutine: true }
    case "routine":
      return { ...common, slug: n.key ?? n.ref, name: n.label }
    case "run":
      return {
        ...common,
        runId: n.ref,
        status: n.status ?? "",
        pipelineSlug: n.key ?? "",
      }
    case "assignment":
      // An assignment IS a run of agent work — the mission engine's half of
      // the substrate. It borrows the run card until it earns its own; the
      // label says which it is, so the reader is not misled.
      return { ...common, runId: n.ref, status: n.status ?? "", pipelineSlug: n.label }
    case "agent":
      return { ...common, agentId: n.ref, slug: n.key ?? n.ref, name: n.label, avatarSeed: n.ref }
    case "inbox":
      return { ...common, itemId: n.ref, kind: n.key ?? "message", title: n.label }
    case "automation":
      // internal/chain spells three states into `status`: "enabled",
      // "disabled" and "deleted". Deriving one boolean from that with
      // `status !== "disabled"` made a tombstone read as ENABLED — a live
      // green badge on a rule that no longer exists. Deleted therefore gets
      // its own field, and `enabled` is derived positively so a spelling this
      // adapter has never heard of fails closed rather than open.
      return {
        ...common,
        automationId: n.ref,
        name: n.label,
        eventType: n.key ?? "",
        enabled: n.status === "enabled",
        deleted: n.status === "deleted",
        routineSlug: "",
      }
    default:
      return { ...common, runId: n.ref, status: n.status ?? "", pipelineSlug: n.label }
  }
}

/** The box the laid-out nodes occupy, in unscaled layout pixels. */
export interface ChainBounds {
  width: number
  height: number
}

export interface ChainGraphData {
  nodes: Node[]
  edges: Edge[]
  /**
   * How big the picture actually is.
   *
   * The card used to give every chain the same 380px box, so a two-node
   * chain sat in a third of it and the rest was dot background — while a
   * branching one was still clipped. Only the layout pass knows the answer,
   * so it says.
   */
  bounds: ChainBounds
}

/**
 * Lays a walked chain out left-to-right.
 *
 * LR rather than the run trace's TB: a chain is read as causation — this
 * caused that — and causation reads along the line people already read
 * along. The run trace is TB because a 15-step routine laid out LR becomes a
 * 3500px noodle; a chain is a handful of nodes deep and does not.
 */
export function buildChainGraph(chain: ChainGraph): ChainGraphData {
  const present = new Set(chain.nodes.map((n) => n.id))

  const nodes: Node[] = chain.nodes.map((n) => ({
    id: n.id,
    type: NODE_TYPE_BY_KIND[n.kind] ?? "overviewRun",
    position: { x: 0, y: 0 },
    data: dataFor(n) as Record<string, unknown>,
  }))

  // A dangling edge renders as a node the client failed to draw. The walker
  // promises not to emit one; this refuses to draw one either way.
  const edges: Edge[] = chain.edges
    .filter((e) => present.has(e.from) && present.has(e.to))
    .map((e) => ({
      id: `${e.from}->${e.to}:${e.kind}`,
      source: e.from,
      target: e.to,
      type: "default",
      animated: false,
      style: {
        stroke: `var(${EDGE_TOKEN[e.kind] ?? "--muted-foreground"})`,
        strokeWidth: 1.5,
        ...(DASHED_EDGE_KINDS.has(e.kind) ? { strokeDasharray: "5 4" } : {}),
      },
    }))

  // Spelled, not derived: Math.min over nothing is Infinity, and a frame
  // sized from that renders nothing at all.
  if (nodes.length === 0) return { nodes, edges, bounds: { width: 0, height: 0 } }

  const g = new DagreGraph({ multigraph: false, compound: false })
  g.setGraph({ rankdir: "LR", nodesep: 30, ranksep: 90, marginx: 20, marginy: 20 })
  g.setDefaultEdgeLabel(() => ({}))
  for (const n of nodes) {
    g.setNode(n.id, {
      width: OVERVIEW_NODE_WIDTH[n.type as OverviewNodeType],
      height: OVERVIEW_NODE_HEIGHT,
    })
  }
  for (const e of edges) g.setEdge(e.source, e.target)
  dagreLayout(g)

  let left = Infinity
  let right = -Infinity
  let top = Infinity
  let bottom = -Infinity

  for (const n of nodes) {
    const pos = g.node(n.id)
    if (!pos) continue
    const w = OVERVIEW_NODE_WIDTH[n.type as OverviewNodeType]
    n.position = { x: pos.x - w / 2, y: pos.y - OVERVIEW_NODE_HEIGHT / 2 }
    left = Math.min(left, n.position.x)
    right = Math.max(right, n.position.x + w)
    top = Math.min(top, n.position.y)
    bottom = Math.max(bottom, n.position.y + OVERVIEW_NODE_HEIGHT)
  }

  // Every node above came from `nodes`, but a node dagre never saw is
  // skipped, and all of them being skipped would leave the sentinels in
  // place. An unmeasurable graph reports no size rather than -Infinity.
  const bounds: ChainBounds =
    left === Infinity ? { width: 0, height: 0 } : { width: right - left, height: bottom - top }

  return { nodes, edges, bounds }
}
