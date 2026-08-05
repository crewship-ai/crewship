import { describe, it, expect } from "vitest"

import { describeStepInput, hasHoverStats } from "@/lib/trace/describe-step-input"
import type { TraceStep } from "@/lib/trace/types"

// The hover card rendered as a header, a gap, and "Click for full
// detail" for script steps — a box that costs a hover and says nothing.
// Two independent causes, both of them the same drift that made
// `foreach` render as an agent: the summary knew six of the executor's
// ten step kinds, and the stats guard tested for `undefined` while the
// definition canvas passes `null`.

const step = (s: Partial<TraceStep>): TraceStep => ({ id: "s", type: "http", ...s })

describe("describeStepInput", () => {
  it("summarises an http step by method and url", () => {
    expect(describeStepInput(step({ type: "http", http: { method: "post", url: "https://x.test/y" } })))
      .toBe("POST https://x.test/y")
  })

  it("summarises a script step by the file it runs", () => {
    expect(describeStepInput(step({ type: "script", script: { path: "scripts/verify.py" } })))
      .toBe("scripts/verify.py")
  })

  it("summarises a query step by its source", () => {
    expect(describeStepInput(step({ type: "query", query: { source: "pipeline_runs" } })))
      .toContain("pipeline_runs")
  })

  it("summarises a foreach step by what it loops over", () => {
    const out = describeStepInput(step({ type: "foreach", foreach: { items: "{{ steps.a.output }}" } }))
    expect(out).toContain("{{ steps.a.output }}")
  })

  it("summarises a notify step by its recipient", () => {
    expect(describeStepInput(step({ type: "notify", notify: { to: "role:OWNER" } })))
      .toContain("role:OWNER")
  })

  it("returns null only when the step genuinely carries nothing to say", () => {
    expect(describeStepInput(step({ type: "script" }))).toBeNull()
    expect(describeStepInput(step({ type: "agent_run" }))).toBeNull()
  })

  // The regression guard. A kind the executor can run but this cannot
  // describe renders an empty hover card, which is worse than no card:
  // it costs a hover to learn nothing.
  it("has something to say about every kind that carries a body", () => {
    const bodied: TraceStep[] = [
      step({ type: "http", http: { url: "https://x.test" } }),
      step({ type: "script", script: { path: "a.py" } }),
      step({ type: "code", code: { runtime: "python" } }),
      step({ type: "transform", transform: { expression: "." } }),
      step({ type: "wait", wait: { kind: "approval" } }),
      step({ type: "query", query: { source: "pipeline_runs" } }),
      step({ type: "foreach", foreach: { items: "{{ x }}" } }),
      step({ type: "notify", notify: { to: "workspace" } }),
      step({ type: "call_pipeline", pipeline_slug: "sub" }),
      step({ type: "agent_run", agent_slug: "morgan" }),
    ]
    for (const s of bodied) {
      expect(describeStepInput(s), `${s.type} has no summary`).not.toBeNull()
    }
  })
})

describe("hasHoverStats", () => {
  it("is false when the canvas is drawing a definition, not a run", () => {
    // buildTraceGraph hands through `null` for a step with no metrics.
    // The old guard tested `!== undefined`, so null passed it and the
    // card rendered an empty stats block with padding and no rows.
    expect(hasHoverStats({ durationMs: null, costUsd: null })).toBe(false)
    expect(hasHoverStats({})).toBe(false)
  })

  it("is false for a step that ran instantly and cost nothing", () => {
    expect(hasHoverStats({ durationMs: 0, costUsd: 0 })).toBe(false)
  })

  it("is true as soon as there is one real number to show", () => {
    expect(hasHoverStats({ durationMs: 1200, costUsd: null })).toBe(true)
    expect(hasHoverStats({ durationMs: null, costUsd: 0.004 })).toBe(true)
  })
})
