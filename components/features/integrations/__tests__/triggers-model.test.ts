import { describe, expect, it } from "vitest"
import { filterTriggers, triggerCounts, triggerRows } from "../triggers-model"
import type { IncomingEndpoint, IncomingTarget } from "../incoming-model"
import type { PipelineWebhook } from "@/hooks/use-pipeline-webhooks"

const routine: IncomingTarget = { id: "r1", slug: "review-prs", name: "Review PRs", kind: "routine" }
const agent: IncomingTarget = { id: "a1", slug: "router", name: "Router", kind: "agent", crew_id: "c1", webhook_secret_set: true }
const page: IncomingTarget = { id: "p1", slug: "pricing", name: "Pricing", kind: "page" }
const hook = (over: Partial<PipelineWebhook>): PipelineWebhook => ({ id: "w1", workspace_id: "ws", name: "github-prs", target_pipeline_id: "r1", token: "", signing_secret_set: true, inputs_template: {}, enabled: true, rate_limit_per_min: 60, fire_count: 46, created_at: "", updated_at: "", ...over })

const endpoints: IncomingEndpoint[] = [
  { id: "w1", name: "github-prs", target: routine, sender: "GitHub", signed: true, enabled: true, fired: 46, path: "", routine: hook({ ingress_profile: "github" }) },
  { id: "w2", name: "deploy", target: routine, sender: "Crewship", signed: true, enabled: false, fired: 3, path: "", routine: hook({ id: "w2", name: "deploy", enabled: false, fire_count: 3, last_status: "FAILED" }) },
  { id: "a1", name: "Router", target: agent, sender: "Crewship", signed: true, enabled: true, path: "/api/v1/webhooks/c1/a1/trigger" },
]

describe("triggerRows", () => {
  it("merges routine/agent/page endpoints; counts only routine receipts", () => {
    const rows = triggerRows(endpoints, [{ target: page, hooks: [{ id: "pw1", name: "feedback" }] }])
    expect(rows.map((r) => [r.id, r.kind, r.checks])).toEqual([
      ["w1", "routine", "GitHub signature"],
      ["w2", "routine", "Crewship signature"],
      ["a1", "agent", "Crewship signature"],
      ["pw1", "page", "Token in the URL"],
    ])
    expect(rows.find((r) => r.kind === "agent")!.calls).toBeNull()
    expect(rows.find((r) => r.kind === "page")!.calls).toBeNull()
    const c = triggerCounts(rows)
    expect(c).toEqual({ total: 4, calls: 49, countedSources: 2, failing: 1, off: 1 })
  })

  it("filters by view, Starts, target and checks", () => {
    const rows = triggerRows(endpoints)
    const f = { view: "all" as const, starts: null, target: null, checks: null, search: "" }
    expect(filterTriggers(rows, { ...f, view: "failing" }).map((r) => r.id)).toEqual(["w2"])
    expect(filterTriggers(rows, { ...f, view: "off" }).map((r) => r.id)).toEqual(["w2"])
    expect(filterTriggers(rows, { ...f, starts: "agent" }).map((r) => r.id)).toEqual(["a1"])
    expect(filterTriggers(rows, { ...f, starts: "routine", target: "review-prs" }).map((r) => r.id)).toEqual(["w1", "w2"])
    expect(filterTriggers(rows, { ...f, checks: "GitHub signature" }).map((r) => r.id)).toEqual(["w1"])
    expect(filterTriggers(rows, { ...f, search: "router" }).map((r) => r.id)).toEqual(["a1"])
  })
})
