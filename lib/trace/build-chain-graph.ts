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
// different bases, so they do not line up by accident: the walk returns
// `assignment`, which had no component, and a component exists for
// `automation`, which the walk does not yet return. Both are mapped here so
// neither side has to know about the other's gaps, and a kind with no entry
// falls back to the run card rather than vanishing — a missing node in a
// picture whose job is completeness is worse than an approximate one.
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
 * Node data per kind.
 *
 * The chain response is deliberately thin — id, kind, label, status — so the
 * walker does not have to know what a card wants to draw. That means some
 * fields the components accept simply are not available here, and this fills
 * what it can rather than inventing the rest: an invented invocation count
 * looks like knowledge.
 */
function dataFor(n: ChainNode): Record<string, unknown> {
  const common = {
    isAnchor: n.anchor === true,
    partial: n.partial === true,
    partialReason: n.partial_reason ?? "",
    depth: n.depth,
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
        startedAt: "",
        pipelineSlug: n.key ?? "",
      }
    case "assignment":
      // An assignment IS a run of agent work — the mission engine's half of
      // the substrate. It borrows the run card until it earns its own; the
      // label says which it is, so the reader is not misled.
      return { ...common, runId: n.ref, status: n.status ?? "", startedAt: "", pipelineSlug: n.label }
    case "agent":
      return { ...common, agentId: n.ref, slug: n.key ?? n.ref, name: n.label, avatarSeed: n.ref }
    case "inbox":
      return { ...common, itemId: n.ref, kind: n.key ?? "message", title: n.label }
    case "automation":
      return {
        ...common,
        automationId: n.ref,
        name: n.label,
        eventType: n.key ?? "",
        enabled: n.status !== "disabled",
        routineSlug: "",
      }
    default:
      return { ...common, runId: n.ref, status: n.status ?? "", startedAt: "", pipelineSlug: n.label }
  }
}

export interface ChainGraphData {
  nodes: Node[]
  edges: Edge[]
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

  if (nodes.length === 0) return { nodes, edges }

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

  for (const n of nodes) {
    const pos = g.node(n.id)
    if (!pos) continue
    const w = OVERVIEW_NODE_WIDTH[n.type as OverviewNodeType]
    n.position = { x: pos.x - w / 2, y: pos.y - OVERVIEW_NODE_HEIGHT / 2 }
  }

  return { nodes, edges }
}
