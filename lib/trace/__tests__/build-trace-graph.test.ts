import { describe, it, expect } from "vitest"
import { buildTraceGraph } from "@/lib/trace/build-trace-graph"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import type { PipelineDSL, TraceStepNodeData } from "@/lib/trace/types"

// Minimal PipelineRun factory — only the fields buildTraceGraph reads
// matter; the rest are filled with inert defaults so the cast is honest.
function makeRun(overrides: Partial<PipelineRun> = {}): PipelineRun {
  return {
    id: "run_1",
    pipeline_id: "p1",
    pipeline_slug: "demo",
    pipeline_name: "Demo",
    status: "completed",
    mode: "live",
    started_at: "2026-06-30T14:57:00.000Z",
    ended_at: "2026-06-30T14:57:19.000Z",
    current_step_id: "",
    step_outputs: { s1: "ok", s2: "ok" },
    cost_usd: 0,
    duration_ms: 18800,
    triggered_via: "manual",
    triggered_by_id: "u1",
    invoking_crew_id: "",
    invoking_agent_id: "",
    invoking_user_id: "u1",
    error_message: "",
    failed_at_step: "",
    issue_identifier: "",
    ...overrides,
  }
}

const dsl: PipelineDSL = {
  steps: [
    { id: "s1", type: "agent_run", agent_slug: "morgan" },
    { id: "s2", type: "http" },
  ],
}

function stepData(graph: ReturnType<typeof buildTraceGraph>, id: string): TraceStepNodeData {
  const node = graph.nodes.find((n) => n.id === id)
  if (!node) throw new Error(`node ${id} not found`)
  return node.data as unknown as TraceStepNodeData
}

describe("buildTraceGraph — sub_spans consumption", () => {
  it("attaches a step's mapped + ordered sub-spans to its node", () => {
    const run = makeRun({
      sub_spans: {
        s1: [
          { kind: "bash", name: "third", seq: 2, status: "ok" },
          { kind: "write", name: "first", seq: 0, status: "ok", attributes: { artifact_path: "sysfacts.yml" } },
          { kind: "think", name: "second", seq: 1, status: "ok" },
        ],
      },
    })
    const graph = buildTraceGraph(run, dsl)
    const s1 = stepData(graph, "s1")
    expect(s1.subSpans?.map((s) => s.name)).toEqual(["first", "second", "third"])
    expect(s1.subSpans?.[0].attributes.artifact_path).toBe("sysfacts.yml")
  })

  it("derives the node model from the first sub-span carrying attributes.model", () => {
    const run = makeRun({
      sub_spans: {
        s1: [
          { kind: "bash", name: "a", status: "ok" },
          { kind: "tool", name: "b", status: "ok", attributes: { model: "opus-4-8" } },
        ],
      },
    })
    expect(stepData(buildTraceGraph(run, dsl), "s1").model).toBe("opus-4-8")
  })

  it("gives steps with no sub-spans an empty array and null model", () => {
    const run = makeRun({
      sub_spans: { s1: [{ kind: "bash", name: "a", status: "ok" }] },
    })
    const graph = buildTraceGraph(run, dsl)
    expect(stepData(graph, "s2").subSpans).toEqual([])
    expect(stepData(graph, "s2").model).toBeNull()
  })

  it("empty sub_spans map → every step gets []", () => {
    const graph = buildTraceGraph(makeRun({ sub_spans: {} }), dsl)
    expect(stepData(graph, "s1").subSpans).toEqual([])
    expect(stepData(graph, "s2").subSpans).toEqual([])
  })

  it("missing sub_spans (undefined) renders identically — [] everywhere", () => {
    const graph = buildTraceGraph(makeRun({ sub_spans: undefined }), dsl)
    expect(stepData(graph, "s1").subSpans).toEqual([])
    expect(stepData(graph, "s1").model).toBeNull()
  })

  it("never throws on malformed sub_spans payloads", () => {
    const run = makeRun({
      // garbage value for a step id, plus a stray non-step key
      sub_spans: { s1: "not-an-array", ghost: [{ kind: "bash" }] } as unknown as Record<string, unknown>,
    })
    expect(() => buildTraceGraph(run, dsl)).not.toThrow()
    expect(stepData(buildTraceGraph(run, dsl), "s1").subSpans).toEqual([])
  })

  it("does not regress the existing node shape (status/edges) when sub_spans present", () => {
    const run = makeRun({
      sub_spans: { s1: [{ kind: "bash", name: "a", status: "ok" }] },
    })
    const graph = buildTraceGraph(run, dsl)
    // trigger + 2 steps
    expect(graph.nodes.filter((n) => n.type === "traceStep")).toHaveLength(2)
    expect(stepData(graph, "s1").status).toBe("success")
  })
})

// ── Layout direction ────────────────────────────────────────────────
//
// The canvas laid out left-to-right, which turns any routine longer
// than a handful of steps into a single-pixel-tall noodle: 15 nodes
// side by side is ~3500px wide, fitView shrinks that to fit the
// viewport width, and every label becomes unreadable. A vertical rank
// direction is what makes a DAG read as a tree — branches sit beside
// each other, depth runs down the page, and the viewport's scarce
// dimension (height) is the one the user can scroll.
//
// These assert the SHAPE the layout produces, not dagre's exact
// numbers: depth increases downward, and siblings share a rank.

function posOf(graph: ReturnType<typeof buildTraceGraph>, id: string) {
  const node = graph.nodes.find((n) => n.id === id)
  if (!node) throw new Error(`node ${id} not found`)
  return node.position
}

describe("buildTraceGraph — vertical tree layout", () => {
  const chain: PipelineDSL = {
    steps: [
      { id: "a", type: "http" },
      { id: "b", type: "http", needs: ["a"] },
      { id: "c", type: "http", needs: ["b"] },
    ],
  }

  it("runs depth down the page, not across it", () => {
    const g = buildTraceGraph(makeRun({ step_outputs: {} }), chain)
    const a = posOf(g, "a")
    const b = posOf(g, "b")
    const c = posOf(g, "c")
    expect(b.y).toBeGreaterThan(a.y)
    expect(c.y).toBeGreaterThan(b.y)
  })

  it("keeps a straight chain in one column instead of one row", () => {
    const g = buildTraceGraph(makeRun({ step_outputs: {} }), chain)
    const xs = ["a", "b", "c"].map((id) => posOf(g, id).x)
    const spreadX = Math.max(...xs) - Math.min(...xs)
    const ys = ["a", "b", "c"].map((id) => posOf(g, id).y)
    const spreadY = Math.max(...ys) - Math.min(...ys)
    // A chain is deep, not wide. LR produced the exact opposite.
    expect(spreadY).toBeGreaterThan(spreadX)
  })

  it("places parallel branches side by side on the same rank", () => {
    const fork: PipelineDSL = {
      steps: [
        { id: "root", type: "http" },
        { id: "left", type: "http", needs: ["root"] },
        { id: "right", type: "http", needs: ["root"] },
        { id: "join", type: "http", needs: ["left", "right"] },
      ],
    }
    const g = buildTraceGraph(makeRun({ step_outputs: {} }), fork)
    const left = posOf(g, "left")
    const right = posOf(g, "right")
    // Same depth…
    expect(left.y).toBe(right.y)
    // …different columns. That is what makes it read as a tree.
    expect(left.x).not.toBe(right.x)
    // And the join sits below both.
    expect(posOf(g, "join").y).toBeGreaterThan(left.y)
  })

  it("puts the trigger above the first step", () => {
    const g = buildTraceGraph(makeRun({ step_outputs: {} }), chain)
    expect(posOf(g, "__trigger__").y).toBeLessThan(posOf(g, "a").y)
  })
})

// ── Edge routes + direction ─────────────────────────────────────────
//
// dagre routes every edge around the nodes between its endpoints. That
// route was computed and discarded on every layout, and the canvas drew
// a bezier corner to corner instead — fine for a one-rank hop, and a
// diagonal sweep across the whole graph for a skip edge.

describe("buildTraceGraph — edge routing", () => {
  const skip: PipelineDSL = {
    steps: [
      { id: "a", type: "http" },
      { id: "b", type: "http", needs: ["a"] },
      { id: "c", type: "http", needs: ["b"] },
      { id: "d", type: "http", needs: ["c"] },
      // The skip edge: five ranks from a, past b and c.
      { id: "e", type: "http", needs: ["d", "a"] },
    ],
  }

  it("carries dagre's route on every edge", () => {
    const g = buildTraceGraph(makeRun({ step_outputs: {} }), skip)
    for (const edge of g.edges) {
      const pts = (edge.data as { points?: { x: number; y: number }[] } | undefined)?.points
      expect(pts, `edge ${edge.id} has no route`).toBeDefined()
      expect(pts!.length).toBeGreaterThanOrEqual(2)
    }
  })

  it("routes a skip edge through waypoints rather than corner to corner", () => {
    const g = buildTraceGraph(makeRun({ step_outputs: {} }), skip)
    const skipEdge = g.edges.find((e) => e.source === "a" && e.target === "e")
    expect(skipEdge).toBeDefined()
    const pts = (skipEdge!.data as { points: { x: number; y: number }[] }).points
    // A straight-line edge is two points. A routed one bends around
    // every rank it passes, which is the entire fix.
    expect(pts.length).toBeGreaterThan(2)
  })

  it("gives every edge an arrowhead so direction is readable", () => {
    const g = buildTraceGraph(makeRun({ step_outputs: {} }), skip)
    for (const edge of g.edges) {
      expect(edge.markerEnd, `edge ${edge.id} has no arrowhead`).toBeDefined()
    }
  })

  it("draws sequencing edges with the routed renderer", () => {
    const g = buildTraceGraph(makeRun({ step_outputs: {} }), skip)
    const seq = g.edges.filter((e) => e.id.startsWith("seq:"))
    expect(seq.length).toBeGreaterThan(0)
    for (const edge of seq) expect(edge.type).toBe("traceRouted")
  })
})
