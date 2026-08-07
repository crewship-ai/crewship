import { describe, it, expect } from "vitest"
import type { Node } from "@xyflow/react"
import {
  buildOverviewGraph,
  OVERVIEW_NODE_WIDTH,
  type BuildOverviewInput,
  type OverviewNodeType,
} from "@/lib/trace/build-overview-graph"
import { overviewNodeTypes } from "@/components/features/activity/overview-nodes"
import type { Mission } from "@/lib/types/mission"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"

// ── fixtures ───────────────────────────────────────────────────────
//
// Only the fields the builder reads are spelled out; the rest is cast
// away so the fixture stays readable and a field added to the wire
// shape doesn't churn this file.

function mission(overrides: Partial<Mission> = {}): Mission {
  return {
    id: "m1",
    identifier: "ENG-1",
    title: "Ship the thing",
    status: "IN_PROGRESS",
    routine_id: "p1",
    routine_slug: "daily-etl",
    routine_name: "Daily ETL",
    ...overrides,
  } as unknown as Mission
}

function pipeline(overrides: Partial<Pipeline> = {}): Pipeline {
  return {
    id: "p1",
    slug: "daily-etl",
    name: "Daily ETL",
    authored_via: "user_api",
    invocation_count: 3,
    ...overrides,
  } as unknown as Pipeline
}

function run(overrides: Partial<PipelineRun> = {}): PipelineRun {
  return {
    id: "prn_1",
    pipeline_id: "p1",
    pipeline_slug: "daily-etl",
    pipeline_name: "Daily ETL",
    status: "completed",
    started_at: "2026-08-01T10:00:00.000Z",
    triggered_via: "schedule",
    invoking_agent_id: "",
    ...overrides,
  } as unknown as PipelineRun
}

function build(overrides: Partial<BuildOverviewInput> = {}) {
  return buildOverviewGraph({
    missions: [],
    pipelines: [],
    runs: [],
    ...overrides,
  })
}

function nodeById(graph: ReturnType<typeof build>, id: string): Node {
  const n = graph.nodes.find((x) => x.id === id)
  if (!n) throw new Error(`node ${id} not found — have: ${graph.nodes.map((x) => x.id).join(", ")}`)
  return n
}

function edgeBetween(graph: ReturnType<typeof build>, source: string, target: string) {
  const e = graph.edges.find((x) => x.source === source && x.target === target)
  if (!e) {
    throw new Error(
      `edge ${source}->${target} not found — have: ${graph.edges.map((x) => `${x.source}->${x.target}`).join(", ")}`,
    )
  }
  return e
}

function isDashed(style: Record<string, unknown> | undefined): boolean {
  return typeof style?.strokeDasharray === "string" && style.strokeDasharray.length > 0
}

/** Right edge of a node, from its registered width. */
function rightEdge(graph: ReturnType<typeof build>, id: string): number {
  const n = nodeById(graph, id)
  return n.position.x + OVERVIEW_NODE_WIDTH[n.type as OverviewNodeType]
}

// ── the width registry ─────────────────────────────────────────────
//
// This is the safety net the builder did not have: widths used to be
// picked by two parallel ternaries that both fell back to the run
// width, so a new node type ranked correctly and rendered at the wrong
// x-offset, silently. One map, and these tests, is the fix.

describe("buildOverviewGraph — node width registry", () => {
  it("registers a width for every node type the canvas renders", () => {
    for (const type of Object.keys(overviewNodeTypes)) {
      expect(
        OVERVIEW_NODE_WIDTH[type as OverviewNodeType],
        `no dagre width registered for node type "${type}"`,
      ).toBeTypeOf("number")
    }
  })

  it("registers no width for a node type the canvas cannot render", () => {
    expect(Object.keys(OVERVIEW_NODE_WIDTH).sort()).toEqual(Object.keys(overviewNodeTypes).sort())
  })

  it("lays out every column one rank-separation apart, whatever its width", () => {
    // The layout pass hands dagre a width and the position pass
    // subtracts half of it from the centre dagre returns. If the two
    // disagree — which is what two parallel ternaries invite — the node
    // slides by half the difference and the gap between columns stops
    // being the rank separation. Reading the gap is how that shows up
    // in a number rather than in a screenshot nobody takes.
    const graph = build({ missions: [mission()], pipelines: [pipeline()], runs: [run()] })
    const RANKSEP = 90
    expect(nodeById(graph, "rt:daily-etl").position.x - rightEdge(graph, "iss:ENG-1")).toBe(RANKSEP)
    expect(nodeById(graph, "run:prn_1").position.x - rightEdge(graph, "rt:daily-etl")).toBe(RANKSEP)
  })
})

// ── the chain itself ───────────────────────────────────────────────

describe("buildOverviewGraph — issue → routine → run", () => {
  it("draws the bound issue, its routine, and the latest run of that routine", () => {
    const graph = build({
      missions: [mission()],
      pipelines: [pipeline()],
      runs: [
        run({ id: "prn_old", started_at: "2026-07-01T10:00:00.000Z" }),
        run({ id: "prn_new", started_at: "2026-08-01T10:00:00.000Z" }),
      ],
    })
    expect(nodeById(graph, "iss:ENG-1").type).toBe("overviewIssue")
    expect(nodeById(graph, "rt:daily-etl").type).toBe("overviewRoutine")
    expect(nodeById(graph, "run:prn_new").type).toBe("overviewRun")
    expect(graph.nodes.some((n) => n.id === "run:prn_old")).toBe(false)
    expect(isDashed(edgeBetween(graph, "iss:ENG-1", "rt:daily-etl").style)).toBe(true)
    expect(isDashed(edgeBetween(graph, "rt:daily-etl", "run:prn_new").style)).toBe(false)
  })

  it("skips an issue that binds no routine", () => {
    const graph = build({ missions: [mission({ routine_id: null, routine_slug: null })] })
    expect(graph.nodes).toHaveLength(0)
  })

  it("lays every node out — a node dagre never saw would stack at the origin", () => {
    const graph = build({ missions: [mission()], pipelines: [pipeline()], runs: [run()] })
    expect(graph.nodes).toHaveLength(3)
    expect(graph.nodes.filter((n) => n.position.x === 0 && n.position.y === 0)).toHaveLength(0)
  })
})
