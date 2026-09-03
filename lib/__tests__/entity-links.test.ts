import { describe, it, expect } from "vitest"
import { entityHref } from "@/lib/entity-links"

// Every link the audits found dead pointed at a route that does not exist
// (/agents/<id>, /orchestration/issues, /crews/<id>). These pin the routes
// that do, and the encoding of what goes into them.
describe("entityHref", () => {
  it("points agents and crews at the crews canvas, never at dead routes", () => {
    expect(entityHref({ kind: "agent", slug: "riley" })).toBe("/crews?agent=riley")
    expect(entityHref({ kind: "crew", slug: "ops", tab: "roster" })).toBe("/crews?crew=ops&tab=roster")
    expect(entityHref({ kind: "agent", slug: "riley" })).not.toMatch(/^\/agents\//)
  })

  it("scopes issues to a crew or assignee on the issues page, not /orchestration", () => {
    expect(entityHref({ kind: "issues", assigneeSlug: "alex" })).toBe("/issues?assignee=alex")
    expect(entityHref({ kind: "issues", crewSlug: "engineering", status: "IN_PROGRESS" })).toBe("/issues?crew=engineering&status=IN_PROGRESS")
    expect(entityHref({ kind: "issue", identifier: "ENG-48" })).toBe("/issues/ENG-48")
  })

  it("carries the pipeline alongside a run id, which /activity needs to resolve it", () => {
    expect(entityHref({ kind: "run", runId: "run_1", pipelineSlug: "page-watch" })).toBe("/activity?run=run_1&pipeline=page-watch")
    expect(entityHref({ kind: "run", runId: "run_1" })).toBe("/activity?run=run_1")
  })

  it("encodes and drops empty params", () => {
    expect(entityHref({ kind: "chat", agentSlug: "a b", sessionId: "" })).toBe("/chat/a%20b")
    expect(entityHref({ kind: "journal", missionId: "m/1" })).toBe("/journal?mission_id=m%2F1")
    expect(entityHref({ kind: "credentials" })).toBe("/credentials")
  })
})
