import { describe, it, expect } from "vitest"
import { buildMiniTrace, type MiniRun } from "@/lib/routine-mini-trace"

// A small, representative DSL: a fetch → agent → notify chain. The mini-trace
// is the compact, read-only projection rendered in the routine "Last Run" card
// (flow + the agent's tool calls), so it must derive cleanly from the DSL + the
// run's sub_spans and never throw on malformed input.
const dsl = {
  steps: [
    { id: "fetch", type: "http", http: { method: "GET", url: "https://example.com/x" } },
    { id: "pick", type: "agent_run", agent_slug: "jordan", prompt: "pick top 5" },
    { id: "notify", type: "call_pipeline", pipeline_slug: "discord" },
  ],
}

const subSpans = {
  pick: [
    {
      kind: "write",
      name: "sysfacts.yml",
      status: "ok",
      duration_ms: 12,
      seq: 0,
      attributes: { artifact_path: "/output/sysfacts.yml", model: "haiku-4-5" },
    },
    {
      kind: "bash",
      name: "ansible-playbook site.yml",
      status: "ok",
      duration_ms: 998,
      seq: 1,
      attributes: { tool: "ansible" },
    },
  ],
}

describe("buildMiniTrace", () => {
  it("emits trigger + one node per step, dropping the output bookend", () => {
    const nodes = buildMiniTrace(dsl, null)
    expect(nodes.map((n) => n.id)).toEqual(["trigger", "fetch", "pick", "notify"])
    expect(nodes.every((n) => n.id !== "output")).toBe(true)
    // first node is the trigger
    expect(nodes[0].kind).toBe("trigger")
    // agent_run resolves to the "agent" kind (drives indigo chrome)
    expect(nodes.find((n) => n.id === "pick")?.kind).toBe("agent")
  })

  it("attaches mini-calls + model from sub_spans keyed by step id", () => {
    const run: MiniRun = { status: "completed", sub_spans: subSpans }
    const nodes = buildMiniTrace(dsl, run)
    const pick = nodes.find((n) => n.id === "pick")!
    expect(pick.calls).toHaveLength(2)
    // ordered by seq
    expect(pick.calls[0].name).toBe("sysfacts.yml")
    expect(pick.calls[0].kind).toBe("write")
    expect(pick.calls[0].artifactPath).toBe("/output/sysfacts.yml")
    expect(pick.calls[1].name).toBe("ansible-playbook site.yml")
    expect(pick.calls[1].tool).toBe("ansible")
    expect(pick.calls[1].durationMs).toBe(998)
    // model surfaced from the first sub-span carrying one
    expect(pick.model).toBe("haiku-4-5")
    // a step with no spans carries no calls
    expect(nodes.find((n) => n.id === "fetch")?.calls).toEqual([])
  })

  it("derives per-step status from the run", () => {
    const run: MiniRun = {
      status: "failed",
      step_outputs: { fetch: "ok" },
      failed_at_step: "pick",
    }
    const nodes = buildMiniTrace(dsl, run)
    expect(nodes.find((n) => n.id === "fetch")?.status).toBe("success")
    expect(nodes.find((n) => n.id === "pick")?.status).toBe("failed")
    // a step after the failure never ran
    expect(nodes.find((n) => n.id === "notify")?.status).toBe("pending")
  })

  it("marks the running step and pins success on a completed run", () => {
    const running: MiniRun = { status: "running", current_step_id: "pick", step_outputs: { fetch: "ok" } }
    const rn = buildMiniTrace(dsl, running)
    expect(rn.find((n) => n.id === "pick")?.status).toBe("running")
    expect(rn.find((n) => n.id === "notify")?.status).toBe("pending")

    const completed: MiniRun = { status: "completed" }
    const cn = buildMiniTrace(dsl, completed)
    // no per-step output recorded, but the run finished → steps read success
    expect(cn.filter((n) => n.kind !== "trigger").every((n) => n.status === "success")).toBe(true)
  })

  it("is defensive — malformed DSL still yields at least a trigger node", () => {
    expect(() => buildMiniTrace(null, null)).not.toThrow()
    expect(buildMiniTrace(null, null)[0].id).toBe("trigger")
    expect(() => buildMiniTrace({ steps: "nope" }, { status: "x", sub_spans: 5 } as unknown as MiniRun)).not.toThrow()
  })

  it("reports whether the run captured any actions at all", () => {
    const withCalls = buildMiniTrace(dsl, { status: "completed", sub_spans: subSpans })
    const totalWith = withCalls.reduce((a, n) => a + n.calls.length, 0)
    expect(totalWith).toBe(2)

    const none = buildMiniTrace(dsl, { status: "completed" })
    const totalNone = none.reduce((a, n) => a + n.calls.length, 0)
    expect(totalNone).toBe(0)
  })
})
