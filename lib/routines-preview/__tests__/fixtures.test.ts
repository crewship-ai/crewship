import { describe, it, expect } from "vitest"

import { buildTraceGraph } from "@/lib/trace/build-trace-graph"
import {
  TODAY_DSL,
  GRANULAR_DSL,
  definitionRun,
  opacityOf,
  DEPENDENCY_SUMMARY,
  RUN_HISTORY,
} from "../fixtures"

// The /routines-new preview is a design surface, but it argues a
// product point with numbers ("6 of 7 steps are opaque"), and it
// feeds the REAL Activity renderer. Both make it worth pinning:
// a fixture that quietly grows a dangling `needs` ref would render
// a broken graph on a page whose whole job is to look correct, and
// a fixture that drifts would make the opacity claim a lie.

function idsOf(dsl: typeof TODAY_DSL): Set<string> {
  return new Set((dsl.steps ?? []).map((s) => s.id))
}

describe("preview fixtures — structural integrity", () => {
  it.each([
    ["today", TODAY_DSL],
    ["granular", GRANULAR_DSL],
  ])("%s: every needs ref points at a real step", (_name, dsl) => {
    const ids = idsOf(dsl)
    for (const step of dsl.steps ?? []) {
      for (const need of step.needs ?? []) {
        expect(ids, `step "${step.id}" needs unknown "${need}"`).toContain(need)
      }
    }
  })

  it.each([
    ["today", TODAY_DSL],
    ["granular", GRANULAR_DSL],
  ])("%s: step ids are unique", (_name, dsl) => {
    const steps = dsl.steps ?? []
    expect(idsOf(dsl).size).toBe(steps.length)
  })

  it.each([
    ["today", TODAY_DSL],
    ["granular", GRANULAR_DSL],
  ])("%s: the graph is acyclic and topologically orderable", (_name, dsl) => {
    const steps = dsl.steps ?? []
    const settled = new Set<string>()
    let progressed = true
    while (progressed) {
      progressed = false
      for (const step of steps) {
        if (settled.has(step.id)) continue
        if ((step.needs ?? []).every((n) => settled.has(n))) {
          settled.add(step.id)
          progressed = true
        }
      }
    }
    expect(settled.size, "cycle or unreachable step in fixture").toBe(steps.length)
  })

  it("today mirrors the production accounting routine: 7 steps ending on a human gate", () => {
    const steps = TODAY_DSL.steps ?? []
    expect(steps).toHaveLength(7)
    expect(steps[steps.length - 1].type).toBe("wait")
  })
})

describe("preview fixtures — the granularity argument", () => {
  it("granular replaces agent steps with deterministic ones", () => {
    const agentsToday = (TODAY_DSL.steps ?? []).filter((s) => s.type === "agent_run").length
    const agentsGranular = (GRANULAR_DSL.steps ?? []).filter((s) => s.type === "agent_run").length
    // The whole pitch: fewer black boxes, more visible cells.
    expect(agentsGranular).toBeLessThan(agentsToday)
    expect((GRANULAR_DSL.steps ?? []).length).toBeGreaterThan((TODAY_DSL.steps ?? []).length)
  })

  it("granular surfaces the hidden loop as a foreach step", () => {
    const kinds = (GRANULAR_DSL.steps ?? []).map((s) => s.type)
    expect(kinds).toContain("foreach")
    // and the deterministic work the prompts describe in prose
    expect(kinds).toContain("script")
    expect(kinds).toContain("transform")
  })

  it("opacityOf reports the share of steps that are agent black boxes", () => {
    expect(opacityOf(TODAY_DSL)).toBeGreaterThan(opacityOf(GRANULAR_DSL))
    expect(opacityOf(TODAY_DSL)).toBeLessThanOrEqual(100)
    expect(opacityOf(GRANULAR_DSL)).toBeGreaterThanOrEqual(0)
  })

  it("opacityOf is 0 for a routine with no steps rather than NaN", () => {
    expect(opacityOf({ steps: [] })).toBe(0)
  })
})

describe("definitionRun — definition mode paints nothing as executed", () => {
  it("renders every step pending, so a spec never looks like a green run", () => {
    const graph = buildTraceGraph(definitionRun(), GRANULAR_DSL)
    const stepNodes = graph.nodes.filter((n) => n.type === "traceStep")
    expect(stepNodes).toHaveLength((GRANULAR_DSL.steps ?? []).length)
    for (const node of stepNodes) {
      expect((node.data as { status: string }).status).toBe("pending")
    }
  })

  it("emits a trigger node plus one node per step", () => {
    const graph = buildTraceGraph(definitionRun(), TODAY_DSL)
    expect(graph.nodes).toHaveLength((TODAY_DSL.steps ?? []).length + 1)
    expect(graph.nodes.some((n) => n.id === "__trigger__")).toBe(true)
  })

  it("wires an edge for every declared dependency", () => {
    const graph = buildTraceGraph(definitionRun(), GRANULAR_DSL)
    for (const step of GRANULAR_DSL.steps ?? []) {
      for (const need of step.needs ?? []) {
        expect(
          graph.edges.some((e) => e.source === need && e.target === step.id),
          `missing edge ${need} → ${step.id}`,
        ).toBe(true)
      }
    }
  })
})

describe("preview fixtures — narrative data", () => {
  it("dependency summary covers the four things a reviewer asks about", () => {
    const kinds = DEPENDENCY_SUMMARY.map((g) => g.kind)
    expect(kinds).toContain("integrations")
    expect(kinds).toContain("notifications")
    expect(kinds).toContain("credentials")
    expect(kinds).toContain("agents")
  })

  it("run history is newest-first so the card needs no re-sort", () => {
    const times = RUN_HISTORY.map((r) => Date.parse(r.started_at))
    expect(times.every((t) => Number.isFinite(t))).toBe(true)
    const sorted = [...times].sort((a, b) => b - a)
    expect(times).toEqual(sorted)
  })
})
