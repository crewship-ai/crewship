import { describe, it, expect } from "vitest"
import type { Node } from "@xyflow/react"
import {
  buildOverviewGraph,
  OVERVIEW_NODE_HEIGHT,
  OVERVIEW_NODE_WIDTH,
  type BuildOverviewInput,
  type OverviewAgentNodeData,
  type OverviewAutomationNodeData,
  type OverviewInboxNodeData,
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
  if (!e) throw new Error(`edge ${source}->${target} not found — have: ${graph.edges.map((x) => `${x.source}->${x.target}`).join(", ")}`)
  return e
}

function isDashed(style: Record<string, unknown> | undefined): boolean {
  return typeof style?.strokeDasharray === "string" && style.strokeDasharray.length > 0
}

/** Centre of a node on the layout axis, from its registered width. */
function centreX(graph: ReturnType<typeof build>, id: string): number {
  const n = nodeById(graph, id)
  return n.position.x + OVERVIEW_NODE_WIDTH[n.type as OverviewNodeType] / 2
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
// x-offset, silently. One map, and these two tests, is the fix.

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

  it("centres every node of a rank on one axis, whatever its width", () => {
    // agent (narrow) and inbox (wide) are both successors of the same
    // run, so dagre ranks them together and gives them one centre. If
    // either is laid out at a width other than its registered one, the
    // card lands off-centre — the exact regression the two ternaries
    // shipped, and the one no test could see.
    const graph = build({
      pipelines: [pipeline()],
      runs: [run()],
      agents: [{ id: "ag1", slug: "morgan", name: "Morgan" }],
      agentIdByRunId: new Map([["prn_1", "ag1"]]),
      inboxItems: [{ id: "ibx1", kind: "waitpoint", title: "Approve deploy", run_id: "prn_1" }],
    })
    expect(OVERVIEW_NODE_WIDTH.overviewAgent).not.toBe(OVERVIEW_NODE_WIDTH.overviewInbox)
    expect(centreX(graph, "agt:ag1")).toBeCloseTo(centreX(graph, "ibx:ibx1"), 5)
  })

  it("keeps two nodes of one rank at least a node-height apart", () => {
    const graph = build({
      pipelines: [pipeline()],
      runs: [run()],
      agents: [{ id: "ag1", slug: "morgan", name: "Morgan" }],
      agentIdByRunId: new Map([["prn_1", "ag1"]]),
      inboxItems: [{ id: "ibx1", kind: "waitpoint", title: "Approve deploy", run_id: "prn_1" }],
    })
    const gap = Math.abs(nodeById(graph, "agt:ag1").position.y - nodeById(graph, "ibx:ibx1").position.y)
    expect(gap).toBeGreaterThanOrEqual(OVERVIEW_NODE_HEIGHT)
  })
})

// ── the chain that already existed ─────────────────────────────────

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

// ── automations ────────────────────────────────────────────────────

describe("buildOverviewGraph — automations", () => {
  const automation = {
    id: "au1",
    name: "On issue created",
    event_type: "issue.created",
    enabled: true,
    routine_slug: "daily-etl",
  }

  it("draws the rule that fired, dashed into the routine it fires", () => {
    const graph = build({ pipelines: [pipeline()], automations: [automation] })
    const node = nodeById(graph, "auto:au1")
    expect(node.type).toBe("overviewAutomation")
    const data = node.data as unknown as OverviewAutomationNodeData
    expect(data).toMatchObject({
      automationId: "au1",
      name: "On issue created",
      eventType: "issue.created",
      enabled: true,
    })
    expect(isDashed(edgeBetween(graph, "auto:au1", "rt:daily-etl").style)).toBe(true)
  })

  it("carries a disabled rule through as disabled rather than dropping it", () => {
    const graph = build({
      pipelines: [pipeline()],
      automations: [{ ...automation, enabled: false }],
    })
    const data = nodeById(graph, "auto:au1").data as unknown as OverviewAutomationNodeData
    expect(data.enabled).toBe(false)
  })

  it("surfaces a routine that only an automation references", () => {
    // No issue binds it and it has never run — the automation alone is
    // reason enough for the routine to be on the graph.
    const graph = build({ automations: [{ ...automation, routine_slug: "unrun" }] })
    expect(nodeById(graph, "rt:unrun").type).toBe("overviewRoutine")
    expect(edgeBetween(graph, "auto:au1", "rt:unrun")).toBeTruthy()
  })

  it("skips an automation that names no routine", () => {
    const graph = build({ automations: [{ ...automation, routine_slug: undefined }] })
    expect(graph.nodes).toHaveLength(0)
  })
})

// ── agents ─────────────────────────────────────────────────────────

describe("buildOverviewGraph — agents", () => {
  const morgan = {
    id: "ag1",
    slug: "morgan",
    name: "Morgan",
    crew_name: "Platform",
    avatar_seed: "morgan",
    avatar_style: "thumbs",
  }

  it("draws the agent that executed the run, solid, off the run", () => {
    const graph = build({
      pipelines: [pipeline()],
      runs: [run()],
      agents: [morgan],
      agentIdByRunId: new Map([["prn_1", "ag1"]]),
    })
    const node = nodeById(graph, "agt:ag1")
    expect(node.type).toBe("overviewAgent")
    const data = node.data as unknown as OverviewAgentNodeData
    expect(data).toMatchObject({
      agentId: "ag1",
      slug: "morgan",
      name: "Morgan",
      crewName: "Platform",
      avatarSeed: "morgan",
      avatarStyle: "thumbs",
    })
    expect(isDashed(edgeBetween(graph, "run:prn_1", "agt:ag1").style)).toBe(false)
  })

  it("falls back to the run's invoking agent when the caller maps nothing", () => {
    const graph = build({
      pipelines: [pipeline()],
      runs: [run({ invoking_agent_id: "ag1" })],
      agents: [morgan],
    })
    expect(nodeById(graph, "agt:ag1")).toBeTruthy()
  })

  it("resolves the executing agent by slug as well as by id", () => {
    const graph = build({
      pipelines: [pipeline()],
      runs: [run()],
      agents: [morgan],
      agentIdByRunId: new Map([["prn_1", "morgan"]]),
    })
    expect(nodeById(graph, "agt:ag1")).toBeTruthy()
  })

  it("draws one agent node for two runs it executed", () => {
    const graph = build({
      pipelines: [pipeline(), pipeline({ id: "p2", slug: "nightly", name: "Nightly" })],
      runs: [
        run({ invoking_agent_id: "ag1" }),
        run({ id: "prn_2", pipeline_id: "p2", pipeline_slug: "nightly", invoking_agent_id: "ag1" }),
      ],
      agents: [morgan],
    })
    expect(graph.nodes.filter((n) => n.type === "overviewAgent")).toHaveLength(1)
    expect(edgeBetween(graph, "run:prn_1", "agt:ag1")).toBeTruthy()
    expect(edgeBetween(graph, "run:prn_2", "agt:ag1")).toBeTruthy()
  })

  it("draws no agent node when the executing agent is unknown", () => {
    const graph = build({
      pipelines: [pipeline()],
      runs: [run({ invoking_agent_id: "ghost" })],
      agents: [morgan],
    })
    expect(graph.nodes.some((n) => n.type === "overviewAgent")).toBe(false)
  })
})

// ── inbox items ────────────────────────────────────────────────────

describe("buildOverviewGraph — inbox items", () => {
  it("draws the item waiting on a human, dashed off the run that produced it", () => {
    const graph = build({
      pipelines: [pipeline()],
      runs: [run({ status: "paused" })],
      inboxItems: [
        {
          id: "ibx1",
          kind: "escalation",
          title: "Grant AWS prod key",
          run_id: "prn_1",
          priority: "urgent",
          blocking: true,
        },
      ],
    })
    const node = nodeById(graph, "ibx:ibx1")
    expect(node.type).toBe("overviewInbox")
    const data = node.data as unknown as OverviewInboxNodeData
    expect(data).toMatchObject({
      itemId: "ibx1",
      kind: "escalation",
      title: "Grant AWS prod key",
      priority: "urgent",
      blocking: true,
    })
    expect(isDashed(edgeBetween(graph, "run:prn_1", "ibx:ibx1").style)).toBe(true)
  })

  it("drops an item whose run is not on the graph", () => {
    const graph = build({
      pipelines: [pipeline()],
      runs: [run()],
      inboxItems: [{ id: "ibx1", kind: "message", title: "Orphan", run_id: "prn_missing" }],
    })
    expect(graph.nodes.some((n) => n.type === "overviewInbox")).toBe(false)
  })

  it("drops an item that names no run", () => {
    const graph = build({
      pipelines: [pipeline()],
      runs: [run()],
      inboxItems: [{ id: "ibx1", kind: "message", title: "Unattached" }],
    })
    expect(graph.nodes.some((n) => n.type === "overviewInbox")).toBe(false)
  })
})

// ── the whole cross-feature chain ──────────────────────────────────

describe("buildOverviewGraph — cross-feature chain", () => {
  it("draws automation → routine → run → agent, with the issue and the inbox item hanging off it", () => {
    const graph = build({
      missions: [mission()],
      pipelines: [pipeline()],
      runs: [run({ status: "waiting", invoking_agent_id: "ag1" })],
      automations: [
        { id: "au1", name: "On issue created", event_type: "issue.created", enabled: true, routine_slug: "daily-etl" },
      ],
      agents: [{ id: "ag1", slug: "morgan", name: "Morgan" }],
      inboxItems: [{ id: "ibx1", kind: "waitpoint", title: "Approve deploy", run_id: "prn_1" }],
      runsWithWaitpoint: new Set(["prn_1"]),
    })
    expect(graph.nodes.map((n) => n.id).sort()).toEqual(
      ["agt:ag1", "auto:au1", "ibx:ibx1", "iss:ENG-1", "rt:daily-etl", "run:prn_1"].sort(),
    )
    expect(graph.edges.map((e) => `${e.source}->${e.target}`).sort()).toEqual(
      [
        "auto:au1->rt:daily-etl",
        "iss:ENG-1->rt:daily-etl",
        "rt:daily-etl->run:prn_1",
        "run:prn_1->agt:ag1",
        "run:prn_1->ibx:ibx1",
      ].sort(),
    )
    // Every node laid out — a node dagre never saw keeps {0,0} and
    // stacks on top of its neighbours.
    expect(graph.nodes.filter((n) => n.position.x === 0 && n.position.y === 0)).toHaveLength(0)
    // Four ranks: (issue, automation) → routine → run → (agent, inbox).
    const centres = graph.nodes.map((n) => Math.round(centreX(graph, n.id)))
    expect(new Set(centres).size).toBe(4)
  })

  it("never names a colour a literal — every stroke resolves a globals.css token", () => {
    const graph = build({
      missions: [mission()],
      pipelines: [pipeline()],
      runs: [run({ invoking_agent_id: "ag1" })],
      automations: [
        { id: "au1", name: "On issue created", event_type: "issue.created", enabled: true, routine_slug: "daily-etl" },
      ],
      agents: [{ id: "ag1", slug: "morgan", name: "Morgan" }],
      inboxItems: [{ id: "ibx1", kind: "waitpoint", title: "Approve deploy", run_id: "prn_1" }],
    })
    expect(graph.edges.length).toBeGreaterThan(0)
    for (const e of graph.edges) {
      const stroke = (e.style as Record<string, unknown> | undefined)?.stroke
      expect(String(stroke), `edge ${e.id} paints a colour the palette does not own`).toMatch(
        /^var\(--[a-z-]+\)$/,
      )
    }
  })
})
