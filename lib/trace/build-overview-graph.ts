// Build the /activity overview graph: issues → bound routines →
// last-run chains. Rendered when no specific run is selected — gives
// the user the workspace-level view of "which issue triggers which
// routine, and what's currently happening".

import { Graph as DagreGraph, layout as dagreLayout } from "@dagrejs/dagre"
import type { Edge, Node } from "@xyflow/react"
import type { Mission } from "@/lib/types/mission"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"

const ISSUE_W = 200
const ROUTINE_W = 200
const RUN_W = 180
const NODE_H = 64

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
    const w =
      n.type === "overviewIssue"
        ? ISSUE_W
        : n.type === "overviewRoutine"
          ? ROUTINE_W
          : RUN_W
    g.setNode(n.id, { width: w, height: NODE_H })
  }
  for (const e of edges) g.setEdge(e.source, e.target)
  dagreLayout(g)

  for (const n of nodes) {
    const pos = g.node(n.id)
    if (pos) {
      const w =
        n.type === "overviewIssue"
          ? ISSUE_W
          : n.type === "overviewRoutine"
            ? ROUTINE_W
            : RUN_W
      n.position = { x: pos.x - w / 2, y: pos.y - NODE_H / 2 }
    }
  }

  return { nodes, edges }
}
