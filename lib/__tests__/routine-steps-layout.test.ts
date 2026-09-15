// The step list of a complex routine, derived from the DSL and nothing else.
//
// Rules 1–7 of docs/ux/routines-operator-console-2026-09-15.md §3: phases
// from `needs`, helper transforms folded, foreach nested, hooks around the
// run, chips instead of prose, a row cap, and a grouped view for very large
// recipes. None of it invents order — a recipe without `needs` keeps plain
// numbering.

import { describe, it, expect } from "vitest"
import {
  layoutRoutineSteps,
  levelsOf,
  stepChips,
  describeTimeout,
  groupKeyOf,
  stepDisplayName,
  routineStepFiles,
} from "../routine-steps-layout"

const step = (id: string, over: Record<string, unknown> = {}) => ({ id, type: "transform", ...over })

/** Modelled on the real "Docs drift audit" on dev1 plus a foreach and hooks. */
function docsDriftAudit() {
  return {
    hooks: {
      before_all: { id: "clean", type: "code", name: "Clean the scratch folder" },
      on_failure: { id: "tell", type: "notify", name: "Tell #docs the audit did not finish" },
    },
    steps: [
      { id: "scan", type: "script", name: "Scan the repository for drift candidates", script: { path: "scripts/docs_audit.sh" }, timeout_seconds: 600 },
      step("scan_state", { name: "Panel state", needs: ["scan"] }),
      step("scan_label", { name: "Panel label", needs: ["scan"] }),
      step("sha_label", { name: "Commit label", needs: ["scan"] }),
      step("total", { name: "Total candidates", needs: ["scan"] }),
      step("pairs", { name: "Doc ↔ code pairs", needs: ["scan"] }),
      {
        id: "judge_each",
        type: "foreach",
        name: "Judge each candidate",
        needs: ["pairs"],
        foreach: {
          items: "{{ steps.pairs.output }}",
          parallelism: 3,
          steps: [
            { id: "fetch", type: "http", name: "Fetch the doc page and the code lines", http: { url: "https://github.com" }, retry: { max_attempts: 3 } },
            { id: "judge", type: "agent_run", name: "Decide: real drift or just another name?", agent_slug: "jordan", outcomes: { grader_agent_slug: "vale", criteria: [{ name: "a", rule: "a" }, { name: "b", rule: "b" }, { name: "c", rule: "c" }] } },
          ],
        },
      },
      { id: "review", type: "agent_run", name: "Write the audit report", agent_slug: "jordan", needs: ["scan", "judge_each"], timeout_seconds: 2400, validation: { min_length: 30, must_contain: ["COUNTS:"], must_not_contain: ["token"] } },
      { id: "post", type: "notify", name: "Post the summary to the workspace", needs: ["review"], if: "steps.total.output > 0" },
      { id: "page-status", type: "crewship", name: "Update the status panel", action: "page.write", needs: ["review", "scan_state", "scan_label", "sha_label"] },
      { id: "page-summary", type: "crewship", name: "Update the summary page", action: "page.write", needs: ["page-status", "total", "pairs"] },
    ],
  }
}

/** 101 steps: 8 services × 12 checks fan out from one build. */
function nightlyMatrix() {
  const services = ["Billing", "Ledger", "Auth", "Search", "Mailer", "Reports", "Webhooks", "Pages"]
  const checks = ["Health", "Migrations", "Contract tests", "Smoke", "Lint", "Unit", "Integration", "Load", "Security scan", "Dependencies", "Docs build", "Screenshots"]
  const steps: Record<string, unknown>[] = [
    { id: "build", type: "http", name: "Fetch last night's build", http: { url: "https://ci.internal" } },
    { id: "matrix", type: "transform", name: "Prepare the service × check matrix", needs: ["build"] },
    { id: "warm", type: "script", name: "Warm the test containers", script: { path: "scripts/warm.sh" }, needs: ["build"] },
  ]
  for (const svc of services)
    for (const [ci, chk] of checks.entries())
      steps.push({
        id: `${svc.toLowerCase()}_${chk.toLowerCase().replace(/ /g, "_")}`,
        name: `${chk} on ${svc}`,
        type: ci < 8 ? "script" : "http",
        script: ci < 8 ? { path: `checks/${svc.toLowerCase()}/${chk.toLowerCase().replace(/ /g, "-")}.sh` } : undefined,
        needs: ["matrix", "warm"],
      })
  steps.push({ id: "judge", type: "agent_run", name: "Judge flaky results", agent_slug: "jordan", needs: steps.filter((s) => String(s.name).includes(" on ")).map((s) => s.id) })
  steps.push({ id: "post", type: "notify", name: "Post the matrix to #quality", needs: ["judge"] })
  return { steps }
}

describe("levelsOf", () => {
  it("gives every step 1 + the deepest dependency, and never invents order without needs", () => {
    const levels = levelsOf([step("a"), step("b", { needs: ["a"] }), step("c", { needs: ["a", "b"] }), step("d")])
    expect(levels).toEqual({ a: 1, b: 2, c: 3, d: 1 })
  })
  it("survives a dangling or cyclic dependency", () => {
    const levels = levelsOf([step("a", { needs: ["ghost", "b"] }), step("b", { needs: ["a"] })])
    expect(levels.a).toBeGreaterThanOrEqual(1)
    expect(levels.b).toBeGreaterThanOrEqual(1)
  })
})

describe("layoutRoutineSteps", () => {
  it("keeps plain numbering when no step declares needs", () => {
    const layout = layoutRoutineSteps({ steps: [step("a"), step("b"), step("c"), step("d")] })
    expect(layout.hasNeeds).toBe(false)
    expect(layout.big).toBe(false)
    // Even helper transforms stay separate rows: folding is a tool for phased recipes.
    expect(layout.rows.map((r) => r.kind)).toEqual(["step", "step", "step", "step"])
    expect(layout.rows.filter((r) => r.kind === "step").map((r) => (r.kind === "step" ? r.position : 0))).toEqual([1, 2, 3, 4])
  })

  it("splits the docs drift audit into phases from needs and folds the five helper transforms", () => {
    const layout = layoutRoutineSteps(docsDriftAudit())
    expect(layout.hasNeeds).toBe(true)
    expect(layout.big).toBe(false)
    expect(layout.phases.map((p) => p.steps.length)).toEqual([1, 5, 1, 1, 2, 1])
    expect(layout.phases.map((p) => p.label)).toEqual([
      "First",
      "Then · 5 in parallel",
      "Then",
      "Then",
      "Then · 2 in parallel",
      "Then",
    ])
    const fold = layout.rows.find((r) => r.kind === "fold")
    expect(fold && fold.kind === "fold" && fold.steps.length).toBe(5)
    expect(fold && fold.kind === "fold" && fold.from).toBe("Scan the repository for drift candidates")
    // Folded transforms do not also appear as their own rows.
    expect(layout.rows.filter((r) => r.kind === "step" && r.id === "scan_state")).toHaveLength(0)
    expect(layout.topLevel).toBe(11)
    expect(layout.nested).toBe(2)
    expect(layout.hookCount).toBe(2)
  })

  it("does not fold transforms that carry a condition or differ in needs", () => {
    const layout = layoutRoutineSteps({
      steps: [
        step("a"),
        step("t1", { needs: ["a"] }),
        step("t2", { needs: ["a"], if: "inputs.x" }),
        step("t3", { needs: ["a"] }),
        step("t4", { needs: ["a"] }),
      ],
    })
    expect(layout.rows.filter((r) => r.kind === "fold")).toHaveLength(0)
  })

  it("nests the foreach body under its parent and counts its items and parallelism", () => {
    const layout = layoutRoutineSteps(docsDriftAudit())
    const rows = layout.rows
    const parent = rows.findIndex((r) => r.kind === "step" && r.id === "judge_each")
    expect(parent).toBeGreaterThan(-1)
    const children = rows.slice(parent + 1, parent + 3)
    expect(children.map((r) => (r.kind === "step" ? [r.id, r.nested] : null))).toEqual([
      ["fetch", true],
      ["judge", true],
    ])
    const loop = rows[parent]
    expect(loop.kind === "step" && loop.loop).toEqual({ items: "{{ steps.pairs.output }}", parallelism: 3, count: 2 })
  })

  it("draws the routine hooks as muted rows around the run", () => {
    const layout = layoutRoutineSteps(docsDriftAudit())
    const labels = layout.rows.filter((r) => r.kind === "phase").map((r) => r.kind === "phase" && r.label)
    expect(labels[0]).toBe("Before the run")
    expect(labels[labels.length - 1]).toBe("If the run fails")
    const hooks = layout.rows.filter((r) => r.kind === "step" && r.hook)
    expect(hooks.map((r) => r.kind === "step" && r.hook)).toEqual(["before_all", "on_failure"])
  })

  it("collapses a 101-step matrix into 1/2/96/1/1 with eight groups and defaults to the map", () => {
    const layout = layoutRoutineSteps(nightlyMatrix())
    expect(layout.big).toBe(true)
    expect(layout.phases.map((p) => p.steps.length)).toEqual([1, 2, 96, 1, 1])
    const wide = layout.phases[2]
    expect(wide.collapsed).toBe(true)
    expect(wide.groups.map((g) => g.name)).toEqual(["Billing", "Ledger", "Auth", "Search", "Mailer", "Reports", "Webhooks", "Pages"])
    expect(wide.groups.every((g) => g.steps.length === 12)).toBe(true)
    const header = layout.rows.find((r) => r.kind === "phase" && r.level === 3)
    expect(header && header.kind === "phase" && header.label).toBe("Then · 96 steps in parallel")
    expect(header && header.kind === "phase" && header.kinds).toEqual([
      ["Script", 64],
      ["Call a service", 32],
    ])
    // Closed: no step or group rows for that phase.
    expect(layout.rows.filter((r) => r.kind === "group")).toHaveLength(0)
    expect(layout.rows.filter((r) => r.kind === "step" && r.level === 3)).toHaveLength(0)
    // Opened: one row per group, no step rows until a group is opened.
    const open = layoutRoutineSteps(nightlyMatrix(), { openPhases: [3] })
    expect(open.rows.filter((r) => r.kind === "group")).toHaveLength(8)
    expect(open.rows.filter((r) => r.kind === "step" && r.level === 3)).toHaveLength(0)
  })

  it("groups by the foreach parent, then the name pattern, then the needs set", () => {
    expect(groupKeyOf("Health on Billing")).toEqual({ key: "Billing", name: "Billing" })
    expect(groupKeyOf("Billing: Health")).toEqual({ key: "Billing", name: "Billing" })
    expect(groupKeyOf("Billing — Health")).toEqual({ key: "Billing", name: "Billing" })
    expect(groupKeyOf("Health check")).toBeNull()
    const many = {
      steps: [
        step("root", { name: "Root" }),
        ...Array.from({ length: 14 }, (_, i) => step(`t${i}`, { name: `Step ${i}`, type: "http", needs: i < 6 ? ["root"] : ["root", "t0"] })),
      ],
    }
    const layout = layoutRoutineSteps(many, { openPhases: [3] })
    expect(layout.big).toBe(true)
    // Phase 2 holds six steps — at the threshold, still listed one by one.
    expect(layout.phases[1].steps).toHaveLength(6)
    expect(layout.phases[1].collapsed).toBe(false)
    // Phase 3 holds eight with no name pattern: one group, named after what they follow.
    expect(layout.phases[2].collapsed).toBe(true)
    expect(layout.phases[2].groups).toHaveLength(1)
    expect(layout.phases[2].groups[0].name).toBe("After Root, Step 0")
  })
})

describe("stepChips", () => {
  it("reads if, needs, checks, retry, timeout, file and agent from the step", () => {
    const names = { scan: "Scan", pairs: "Pairs" }
    const chips = stepChips(
      {
        id: "x",
        type: "agent_run",
        agent_slug: "jordan",
        if: "steps.total.output > 0",
        needs: ["scan", "pairs"],
        outcomes: { grader_agent_slug: "vale", criteria: [{ name: "a", rule: "a" }, { name: "b", rule: "b" }] },
        retry: { max_attempts: 3 },
        timeout_seconds: 600,
        script: { path: "scripts/x.py" },
      },
      (id) => names[id as keyof typeof names] ?? id,
    )
    expect(chips.only).toBe("steps.total.output > 0")
    expect(chips.after).toEqual(["Scan", "Pairs"])
    expect(chips.checks).toEqual({ count: 2, grader: "vale" })
    expect(chips.attempts).toBe(3)
    expect(chips.timeout).toBe("10 min")
    expect(chips.file).toBe("scripts/x.py")
    expect(chips.agent).toBe("jordan")
  })
  it("counts structural validation rules when there is no grader", () => {
    const chips = stepChips({ id: "x", type: "agent_run", validation: { min_length: 30, must_contain: ["COUNTS:"], must_not_contain: ["a", "b"] } }, (id) => id)
    expect(chips.checks).toEqual({ count: 4, grader: undefined })
  })
  it("formats timeouts for people", () => {
    expect(describeTimeout(45)).toBe("45 s")
    expect(describeTimeout(600)).toBe("10 min")
    expect(describeTimeout(5400)).toBe("1 h 30 min")
    expect(describeTimeout(7200)).toBe("2 h")
  })
})

describe("stepDisplayName and routineStepFiles", () => {
  it("prefers the declared name, then the described action", () => {
    expect(stepDisplayName({ id: "s", type: "script", name: "Scan" }, 1)).toBe("Scan")
    expect(stepDisplayName({ id: "s", type: "script", script: { path: "scripts/probe.py" } }, 1)).toBe("Run script probe")
  })
  it("collects script paths from steps, foreach bodies and hooks", () => {
    const files = routineStepFiles(docsDriftAudit())
    expect(files).toEqual([{ path: "scripts/docs_audit.sh", step_ids: ["scan"] }])
    const nested = routineStepFiles({
      hooks: { before_all: { id: "h", type: "script", script: { path: "scripts/h.sh" } } },
      steps: [
        { id: "a", type: "script", script: { path: "/crew/shared/scripts/a.py" } },
        { id: "loop", type: "foreach", foreach: { items: "x", steps: [{ id: "b", type: "script", script: { path: "scripts/a.py" } }] } },
      ],
    })
    expect(nested).toEqual([
      { path: "scripts/a.py", step_ids: ["a", "b"] },
      { path: "scripts/h.sh", step_ids: ["h"] },
    ])
  })
})
