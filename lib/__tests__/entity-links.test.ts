import { describe, it, expect } from "vitest"
import { entityHref } from "@/lib/entity-links"
import { filtersFromParams } from "@/hooks/use-issue-filters"

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
    expect(entityHref({ kind: "issues", assigneeId: "agt_1" })).toBe("/issues?assignee_id=agt_1")
    expect(entityHref({ kind: "issues", crewId: "crw_1", status: "IN_PROGRESS" })).toBe("/issues?crew_id=crw_1&status=IN_PROGRESS")
    expect(entityHref({ kind: "issue", identifier: "ENG-48" })).toBe("/issues/ENG-48")
  })

  // The page (and GET /issues) read crew_id / assignee_id and match them
  // against ids; a link spelled ?crew=&assignee= landed on the whole list.
  it("builds an /issues link the Issues page reads back as the same filter", () => {
    const href = entityHref({ kind: "issues", crewId: "crw_1", assigneeId: "agt_1", status: "REVIEW" })
    const f = filtersFromParams(new URL(href, "http://x").searchParams)
    expect(f.crewId).toBe("crw_1")
    expect(f.agentId).toBe("agt_1")
    expect(f.statuses).toEqual(["REVIEW"])
  })

  it("carries the pipeline alongside a run id, which /activity needs to resolve it", () => {
    expect(entityHref({ kind: "run", runId: "run_1", pipelineSlug: "page-watch" })).toBe("/activity?run=run_1&pipeline=page-watch")
    expect(entityHref({ kind: "run", runId: "run_1" })).toBe("/activity?run=run_1")
  })

  it("opens the inbox filtered to one agent — the decision banner's way out", () => {
    expect(entityHref({ kind: "inbox", agentSlug: "alex" })).toBe("/inbox?agent=alex")
    expect(entityHref({ kind: "inbox" })).toBe("/inbox")
    expect(entityHref({ kind: "inbox", attention: "run-alerts" })).toBe("/inbox?attention=run-alerts")
  })

  it("opens a skill beside the Skills explorer, or the list filtered to an agent", () => {
    expect(entityHref({ kind: "skill", id: "sk_1" })).toBe("/skills?skill=sk_1")
    expect(entityHref({ kind: "skill", id: "sk_1", tab: "agents" })).toBe("/skills?skill=sk_1&tab=agents")
    expect(entityHref({ kind: "skills", agentId: "agt_1" })).toBe("/skills?agent=agt_1")
  })

  it("opens a crew tool on the integrations page, where Connect lives", () => {
    expect(entityHref({ kind: "integrations", tab: "tools", section: "crew-tools", server: "srv_1" })).toBe("/integrations?tab=tools&section=crew-tools&server=srv_1")
    expect(entityHref({ kind: "integrations" })).toBe("/integrations")
  })

  it("sends a journal reference straight to Activity, in Activity's own keys (F-02)", () => {
    expect(entityHref({ kind: "journal" })).toBe("/activity")
    expect(entityHref({ kind: "journal", crewSlug: "ops", agentSlug: "riley", missionId: "ENG-1", traceId: "run_1" })).toBe(
      "/activity?crew=ops&agent=riley&mission=ENG-1&run=run_1",
    )
  })

  it("encodes and drops empty params", () => {
    expect(entityHref({ kind: "chat", agentSlug: "a b", sessionId: "" })).toBe("/chat/a%20b")
    expect(entityHref({ kind: "journal", missionId: "m/1" })).toBe("/activity?mission=m%2F1")
    expect(entityHref({ kind: "credentials" })).toBe("/credentials")
  })
})
