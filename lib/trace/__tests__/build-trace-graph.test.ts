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
