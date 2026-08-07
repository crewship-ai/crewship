// Build the /activity overview graph: issues → bound routines →
// last-run chains. Rendered when no specific run is selected — gives
// the user the workspace-level view of "which issue triggers which
// routine, and what's currently happening".

import { Graph as DagreGraph, layout as dagreLayout } from "@dagrejs/dagre"
import type { Edge, Node } from "@xyflow/react"
import type { Mission } from "@/lib/types/mission"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"

/**
 * Every node type this builder emits, and the canvas registers.
 *
 * The list is the contract between the two: `overviewNodeTypes` in
 * `components/features/activity/overview-nodes.tsx` is typed as a
 * `Record<OverviewNodeType, …>`, so a component registered without a
 * type here — or a type here without a width below — is a compile
 * error, and a test asserts the same at runtime.
 */
export const OVERVIEW_NODE_TYPES = [
  "overviewIssue",
  "overviewRoutine",
  "overviewRun",
] as const

export type OverviewNodeType = (typeof OVERVIEW_NODE_TYPES)[number]

/**
 * Layout width per node type, in the same pixels the node component
 * renders (`w-[200px]` etc.). Dagre is handed these and returns node
 * CENTRES, so the same number has to come back out when the centre is
 * converted to React Flow's top-left `position`.
 *
 * This used to be two parallel ternaries — one for the dagre pass, one
 * for the position pass — that both ended in `: RUN_W`. A node type
 * added to only the canvas therefore ranked correctly and rendered
 * half a card off its column, with nothing to catch it. One map, read
 * by both passes, cannot drift from itself.
 *
 * Keep a value in step with the width class on its component.
 */
export const OVERVIEW_NODE_WIDTH: Record<OverviewNodeType, number> = {
  overviewIssue: 200,
  overviewRoutine: 200,
  overviewRun: 180,
}

/** Shared layout height. Every overview card is a two-line card. */
export const OVERVIEW_NODE_HEIGHT = 64

export interface OverviewGraphData {
  nodes: Node[]
  edges: Edge[]
}

export interface OverviewIssueNodeData {
  identifier: string
  title: string
  status: string
  hasRoutine: boolean
  [key: string]: unknown
}

export interface OverviewRoutineNodeData {
  slug: string
  name: string
  authoredVia?: string
  invocationCount?: number
  [key: string]: unknown
}

export interface OverviewRunNodeData {
  runId: string
  status: string
  startedAt: string
  triggeredVia?: string
  pipelineSlug: string
  isWaitpoint?: boolean
  [key: string]: unknown
}

interface BuildOverviewInput {
  // Missions with routine_id are issues that bind a routine. We only
  // surface these — orphan issues without a routine binding aren't
  // "overview-worthy" because there's no execution chain to show.
  missions: Mission[]
  pipelines: Pipeline[]
  runs: PipelineRun[]
  // Optional: caller-provided "is this run currently in a paused
  // waitpoint state" predicate. Lets the canvas badge those
  // distinctively without re-deriving the rule everywhere.
  runsWithWaitpoint?: ReadonlySet<string>
}

// buildOverviewGraph wires three columns:
//   col 1: Issues (one node per issue with routine_id)
//   col 2: Routines (one node per pipeline that's bound to ≥1 issue
//          or has been recently invoked)
//   col 3: Latest runs (one node per (pipeline, most recent run))
//
// Edges: issue ─dashed─▶ routine ─solid─▶ runRef. Click handlers wire
// at the canvas level (selectStep / openIssue / selectRun), so the
// builder stays purely structural.
export function buildOverviewGraph(input: BuildOverviewInput): OverviewGraphData {
  const { missions, pipelines, runs, runsWithWaitpoint } = input

  // ── 1. Bound issues ─────────────────────────────────────────────
  const boundIssues = missions.filter(
    (m) => m.routine_id && (m.routine_slug || m.routine_name),
  )

  // ── 2. Routines ────────────────────────────────────────────────
  // A routine surfaces in the overview if (a) it's bound to an
  // issue, (b) there's a recent run for it, OR (c) it's a saved
  // pipeline. We dedupe by slug.
  const routineSlugs = new Set<string>()
  for (const m of boundIssues) if (m.routine_slug) routineSlugs.add(m.routine_slug)
  for (const r of runs) routineSlugs.add(r.pipeline_slug)
  // Resolve metadata via pipelines list (preferred) then fall back
  // to anything we know from runs.
  const routineByslug = new Map<string, OverviewRoutineNodeData>()
  for (const slug of routineSlugs) {
    const p = pipelines.find((pp) => pp.slug === slug)
    if (p) {
      routineByslug.set(slug, {
        slug: p.slug,
        name: p.name,
        authoredVia: p.authored_via,
        invocationCount: p.invocation_count,
      })
    } else {
      const r = runs.find((rr) => rr.pipeline_slug === slug)
      routineByslug.set(slug, {
        slug,
        name: r?.pipeline_name || slug,
      })
    }
  }

  // ── 3. Last run per routine ─────────────────────────────────────
  const latestRunBySlug = new Map<string, PipelineRun>()
  for (const r of runs) {
    const existing = latestRunBySlug.get(r.pipeline_slug)
    if (!existing || (r.started_at ?? "") > (existing.started_at ?? "")) {
      latestRunBySlug.set(r.pipeline_slug, r)
    }
  }

  // ── 4. Build nodes ─────────────────────────────────────────────
  const nodes: Node[] = []
  const edges: Edge[] = []

  for (const m of boundIssues) {
    const data: OverviewIssueNodeData = {
      identifier: m.identifier ?? m.id,
      title: m.title,
      status: m.status,
      hasRoutine: true,
    }
    nodes.push({
      id: `iss:${m.identifier ?? m.id}`,
      type: "overviewIssue",
      data: data as unknown as Record<string, unknown>,
      position: { x: 0, y: 0 },
    })
    if (m.routine_slug) {
      edges.push({
        id: `e:iss:${m.identifier ?? m.id}->rt:${m.routine_slug}`,
        source: `iss:${m.identifier ?? m.id}`,
        target: `rt:${m.routine_slug}`,
        type: "default",
        animated: false,
        style: {
          stroke: "rgb(96, 165, 250)",
          strokeWidth: 1.5,
          strokeDasharray: "5 4",
        },
      })
    }
  }

  for (const [slug, data] of routineByslug) {
    nodes.push({
      id: `rt:${slug}`,
      type: "overviewRoutine",
      data: data as unknown as Record<string, unknown>,
      position: { x: 0, y: 0 },
    })
    const latest = latestRunBySlug.get(slug)
    if (latest) {
      const runData: OverviewRunNodeData = {
        runId: latest.id,
        status: latest.status,
        startedAt: latest.started_at,
        triggeredVia: latest.triggered_via,
        pipelineSlug: slug,
        isWaitpoint: runsWithWaitpoint?.has(latest.id) ?? false,
      }
      nodes.push({
        id: `run:${latest.id}`,
        type: "overviewRun",
        data: runData as unknown as Record<string, unknown>,
        position: { x: 0, y: 0 },
      })
      const sourceStatus = latest.status
      edges.push({
        id: `e:rt:${slug}->run:${latest.id}`,
        source: `rt:${slug}`,
        target: `run:${latest.id}`,
        type: "default",
        animated: sourceStatus === "running" || sourceStatus === "queued",
        style: {
          stroke: "rgba(148, 163, 184, 0.5)",
          strokeWidth: 1.5,
        },
      })
    }
  }

  // ── 5. Layout via dagre LR ─────────────────────────────────────
  const g = new DagreGraph({ multigraph: false, compound: false })
  g.setGraph({ rankdir: "LR", nodesep: 30, ranksep: 90, marginx: 20, marginy: 20 })
  g.setDefaultEdgeLabel(() => ({}))

  for (const n of nodes) {
    g.setNode(n.id, { width: widthFor(n.type), height: OVERVIEW_NODE_HEIGHT })
  }
  for (const e of edges) g.setEdge(e.source, e.target)
  dagreLayout(g)

  for (const n of nodes) {
    const pos = g.node(n.id)
    if (pos) {
      n.position = {
        x: pos.x - widthFor(n.type) / 2,
        y: pos.y - OVERVIEW_NODE_HEIGHT / 2,
      }
    }
  }

  return { nodes, edges }
}

/**
 * Width for a node type, or a loud failure.
 *
 * Unreachable for anything this builder emits — every node it pushes
 * carries one of the three registered types, and the width map is
 * total over that union — so the throw fires only for a type someone
 * added without registering a width, at the moment they add it, which
 * is the whole point. The alternative was the silent `: RUN_W` that
 * would have shipped a node type rendering half a card off its column.
 */
function widthFor(type: string | undefined): number {
  const w = type === undefined ? undefined : OVERVIEW_NODE_WIDTH[type as OverviewNodeType]
  if (typeof w !== "number") {
    throw new Error(`buildOverviewGraph: no width registered for node type "${type}"`)
  }
  return w
}
