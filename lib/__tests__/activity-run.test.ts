import { describe, expect, it } from "vitest"

import {
  chainBranches,
  historyStrip,
  linkedEntities,
  runActions,
  runStatusLabel,
  runTone,
  stepProgress,
  triggerPhrase,
} from "../activity-run"

describe("runTone", () => {
  it("speaks the rail's five words, whatever case the server used", () => {
    expect(runTone("RUNNING")).toBe("running")
    expect(runTone("queued")).toBe("running")
    expect(runTone("paused")).toBe("waiting")
    expect(runTone("waiting")).toBe("waiting")
    expect(runTone("failed")).toBe("failed")
    expect(runTone("COMPLETED")).toBe("done")
    expect(runTone("cancelled")).toBe("stopped")
    expect(runTone("interrupted")).toBe("stopped")
  })
})

describe("triggerPhrase", () => {
  it("names what started the run in words, not the column value", () => {
    expect(triggerPhrase({ triggered_via: "schedule" })).toBe("Schedule")
    expect(triggerPhrase({ triggered_via: "manual" })).toBe("Started by hand")
    expect(triggerPhrase({ triggered_via: "call_pipeline" })).toBe("Called by another routine")
    expect(triggerPhrase({ triggered_via: "webhook" })).toBe("Webhook")
  })

  it("adds the rule's name for an automation, but never a database id", () => {
    expect(triggerPhrase({ triggered_via: "automation", metadata: { automation_name: "On new lead" } })).toBe(
      "Automation · On new lead",
    )
    expect(triggerPhrase({ triggered_via: "schedule", triggered_by_id: "psched_cmuxw94h0001abcd1234ef" })).toBe(
      "Schedule",
    )
  })

  it("keeps an unknown trigger's own word rather than guessing", () => {
    expect(triggerPhrase({ triggered_via: "wake_check" })).toBe("Wake check")
    expect(triggerPhrase({})).toBe("Unknown trigger")
  })
})

describe("stepProgress", () => {
  const steps = [{ id: "a" }, { id: "b" }, { id: "c" }, { id: "d" }]
  const summary = (status: string) => ({ latest: { status } })

  it("counts finished steps out of the recipe, skipped ones included", () => {
    const byStep = new Map([
      ["a", summary("completed")],
      ["b", summary("skipped")],
      ["c", summary("failed")],
    ])
    expect(stepProgress(steps, byStep)).toEqual({ done: 2, total: 4 })
  })

  it("says nothing when the recipe is unknown", () => {
    expect(stepProgress(undefined, new Map())).toBeNull()
    expect(stepProgress([], new Map())).toBeNull()
  })
})

describe("runActions", () => {
  it("offers Stop only on a live run and only to an admin", () => {
    expect(runActions("running", "ADMIN")).toEqual({ retry: false, stop: true })
    expect(runActions("waiting", "MANAGER")).toEqual({ retry: false, stop: false })
  })

  it("offers Retry on a finished run to a manager or above", () => {
    expect(runActions("failed", "MANAGER")).toEqual({ retry: true, stop: false })
    expect(runActions("completed", "OWNER")).toEqual({ retry: true, stop: false })
    expect(runActions("failed", "MEMBER")).toEqual({ retry: false, stop: false })
    expect(runActions("failed", null)).toEqual({ retry: false, stop: false })
  })
})

describe("chainBranches", () => {
  const node = (id: string, label: string, extra: Record<string, unknown> = {}) => ({
    id,
    kind: id.split(":")[0],
    ref: id.split(":")[1],
    label,
    depth: 0,
    ...extra,
  })

  it("nests the work a run caused under it and leaves out what caused the run", () => {
    const graph = {
      nodes: [
        node("run:r1", "parent", { chain_origin: "r1" }),
        node("issue:i1", "Restore delivery"),
        node("routine:p1", "Check form delivery"),
        node("assignment:a1", "Casey · check bank feed", { status: "completed", duration_ms: 1200 }),
        node("assignment:a2", "Jordan · sub-task", { status: "running" }),
        node("inbox:x1", "Send reminder?", { status: "pending" }),
      ],
      edges: [
        { from: "routine:p1", to: "run:r1", kind: "runs" },
        { from: "issue:i1", to: "routine:p1", kind: "triggers" },
        { from: "run:r1", to: "assignment:a1", kind: "triggers" },
        { from: "assignment:a1", to: "assignment:a2", kind: "triggers" },
        { from: "run:r1", to: "inbox:x1", kind: "produces" },
      ],
    }
    const tree = chainBranches(graph, "r1")
    expect(tree.map((n) => n.label)).toEqual(["Casey · check bank feed", "Send reminder?"])
    expect(tree[0].children.map((n) => n.label)).toEqual(["Jordan · sub-task"])
    expect(tree[0].durationMs).toBe(1200)
  })

  it("reaches a sub-run through the routine it called, and only this chain's run of it", () => {
    const graph = {
      nodes: [
        node("run:r1", "parent", { chain_origin: "r1" }),
        node("routine:p2", "Normalize contact"),
        node("run:r2", "normalize-contact", { chain_origin: "r1", status: "completed" }),
        node("run:r9", "normalize-contact", { chain_origin: "r7", status: "failed" }),
      ],
      edges: [
        { from: "run:r1", to: "routine:p2", kind: "triggers" },
        { from: "routine:p2", to: "run:r2", kind: "runs" },
        { from: "routine:p2", to: "run:r9", kind: "runs" },
      ],
    }
    const tree = chainBranches(graph, "r1")
    expect(tree).toHaveLength(1)
    expect(tree[0]).toMatchObject({ kind: "run", ref: "r2", label: "Normalize contact", status: "completed" })
  })

  it("survives a cycle in the walk", () => {
    const graph = {
      nodes: [node("run:r1", "a", { chain_origin: "r1" }), node("assignment:a1", "b")],
      edges: [
        { from: "run:r1", to: "assignment:a1", kind: "triggers" },
        { from: "assignment:a1", to: "run:r1", kind: "triggers" },
      ],
    }
    expect(chainBranches(graph, "r1").map((n) => n.label)).toEqual(["b"])
  })
})

describe("linkedEntities", () => {
  it("lists the routine, the issues and the agents once each, issue identifiers first", () => {
    const links = linkedEntities({
      routine: { slug: "check-form", name: "Check form delivery" },
      issues: [{ id: "i1", identifier: "OPS-1", title: "Restore delivery" }],
      graphIssues: [{ ref: "i1", label: "Restore delivery" }, { ref: "i2", label: "Bank feed lags" }],
      agents: [{ id: "ag1", name: "Casey" }],
    })
    expect(links).toEqual([
      { kind: "routine", ref: "check-form", label: "Check form delivery" },
      { kind: "issue", ref: "i1", label: "OPS-1" },
      { kind: "issue", ref: "i2", label: "Bank feed lags" },
      { kind: "agent", ref: "ag1", label: "Casey" },
    ])
  })
})

describe("historyStrip", () => {
  it("lays the routine's runs oldest to newest and marks this one", () => {
    const strip = historyStrip(
      [
        { id: "r3", status: "failed", started_at: "2026-10-08T09:00:00Z" },
        { id: "r2", status: "completed", started_at: "2026-10-08T08:00:00Z" },
        { id: "r1", status: "completed", started_at: "2026-10-08T07:00:00Z" },
      ],
      "r2",
    )
    expect(strip.map((d) => [d.id, d.tone, d.current])).toEqual([
      ["r1", "done", false],
      ["r2", "done", true],
      ["r3", "failed", false],
    ])
  })

  it("keeps only the newest runs when there are more than fit", () => {
    const records = Array.from({ length: 30 }, (_, i) => ({
      id: `r${i}`,
      status: "completed",
      started_at: new Date(Date.UTC(2026, 9, 1, i)).toISOString(),
    }))
    const strip = historyStrip(records, "r29", 20)
    expect(strip).toHaveLength(20)
    expect(strip[0].id).toBe("r10")
    expect(strip.at(-1)?.id).toBe("r29")
  })
})

describe("runStatusLabel", () => {
  it("keeps a cancel and an interruption apart where the rail says only Stopped (#2981)", () => {
    expect(runStatusLabel("cancelled")).toBe("Cancelled")
    expect(runStatusLabel("CANCELLED")).toBe("Cancelled")
    expect(runStatusLabel("interrupted")).toBe("Interrupted")
    expect(runStatusLabel("failed")).toBe("Could not finish")
    expect(runStatusLabel("completed")).toBe("Completed")
  })
})
