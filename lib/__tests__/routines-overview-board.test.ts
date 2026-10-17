import { describe, it, expect } from "vitest"

import { next24Hours, overviewNeeds } from "@/lib/routines-overview-board"

// RT-OV (routines.md R02): Needs you and Next 24 hours, composed from the
// waitpoints, the live runs, the routines, the schedules and the accepted
// one-time starts the page already loads.

const now = new Date("2026-10-17T08:00:00Z")
const at = (h: number) => new Date(now.getTime() + h * 3_600_000).toISOString()
const routines = [
  { slug: "weekly", name: "Weekly report", status: "active" as const },
  { slug: "invoice", name: "Invoice intake", status: "proposed" as const },
  { slug: "news", name: "News digest", status: "active" as const, last_invocation_status: "failed" },
  { slug: "cert", name: "Cert rotation", status: "active" as const },
  { slug: "old", name: "Old export", status: "disabled" as const },
]

describe("next24Hours", () => {
  it("lists enabled schedules and planned one-time starts due within a day, soonest first", () => {
    const rows = next24Hours({
      routines,
      schedules: [
        { id: "s1", target_pipeline_slug: "weekly", enabled: true, next_run_at: at(5) },
        { id: "s2", target_pipeline_slug: "news", enabled: true, next_run_at: at(30) },
        { id: "s3", target_pipeline_slug: "cert", enabled: false, next_run_at: at(1) },
      ],
      starts: [
        { id: "p1", pipeline_slug: "news", fire_at: at(2), status: "pending" },
        { id: "p2", pipeline_slug: "news", fire_at: at(3), status: "fired" },
      ],
      now,
    })
    expect(rows.map((r) => [r.slug, r.kind, r.at])).toEqual([
      ["news", "once", at(2)],
      ["weekly", "schedule", at(5)],
    ])
    expect(rows[0].name).toBe("News digest")
  })
})

describe("overviewNeeds", () => {
  it("puts decisions first, then proposals, failures, the circuit breaker and turned-off routines", () => {
    const items = overviewNeeds({
      routines,
      activeRuns: [
        { id: "run_w", pipeline_slug: "weekly", pipeline_name: "Weekly report", status: "waiting" },
        { id: "run_r", pipeline_slug: "news", status: "running" },
      ],
      waitpoints: [{ token: "wp-ref-5512", pipeline_run_id: "run_w", step_id: "approve", kind: "approval", prompt: "Publish the report?\nmore", timeout_at: at(23), created_at: at(-1) }],
      schedules: [{ id: "s9", target_pipeline_slug: "cert", enabled: false, disabled_reason: "circuit_breaker", consecutive_failures: 5 }],
    })
    expect(items.map((i) => [i.kind, i.slug])).toEqual([
      ["decision", "weekly"],
      ["proposed", "invoice"],
      ["failed", "news"],
      ["breaker", "cert"],
      ["off", "old"],
    ])
    const decision = items[0]
    expect(decision.runId).toBe("run_w")
    expect(decision.detail).toBe("Publish the report?")
    expect(decision.approvable).toBe(true)
    // The waitpoint token is a credential: never part of what is shown.
    expect(JSON.stringify({ ...decision, waitpoint: undefined })).not.toContain("wp-ref-5512")
  })

  it("a decision with a form is answered on the run page, not inline", () => {
    const [item] = overviewNeeds({
      routines,
      activeRuns: [{ id: "run_w", pipeline_slug: "weekly", status: "waiting" }],
      waitpoints: [{ token: "t", pipeline_run_id: "run_w", step_id: "s", kind: "approval", prompt: "", timeout_at: at(1), created_at: at(0), decision_form: { fields: [] } as never }],
      schedules: [],
    })
    expect(item.approvable).toBe(false)
  })

  it("a run waiting on a person without an approval waitpoint still needs you", () => {
    const [item] = overviewNeeds({ routines: [], activeRuns: [{ id: "r", pipeline_slug: "weekly", pipeline_name: "Weekly report", status: "waiting" }], waitpoints: [], schedules: [] })
    expect(item).toMatchObject({ kind: "decision", slug: "weekly", runId: "r", approvable: false })
  })
})
