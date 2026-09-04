import { describe, expect, it } from "vitest"

import { isIssueBoardEvent, shouldRefetchForIssueEvent } from "@/components/features/orchestration/issue-realtime"

describe("isIssueBoardEvent", () => {
  it("recognizes every issue.* board event", () => {
    for (const type of [
      "issue.created",
      "issue.updated",
      "issue.status_changed",
      "issue.started",
      "issue.deleted",
      "issues.bulk_updated",
    ]) {
      expect(isIssueBoardEvent(type)).toBe(true)
    }
  })

  it("ignores unrelated event types", () => {
    expect(isIssueBoardEvent("mission.updated")).toBe(false)
    expect(isIssueBoardEvent("task.updated")).toBe(false)
    expect(isIssueBoardEvent("crew.updated")).toBe(false)
  })
})

describe("shouldRefetchForIssueEvent", () => {
  it("refetches an issue.created event for the visible crew (no filter active)", () => {
    expect(shouldRefetchForIssueEvent("issue.created", { id: "m1", crew_id: "crew-1" }, null)).toBe(true)
  })

  it("refetches an issue.created event that matches the active crew filter", () => {
    expect(shouldRefetchForIssueEvent("issue.created", { id: "m1", crew_id: "crew-1" }, "crew-1")).toBe(true)
  })

  it("skips an issue.created event for a different, known crew", () => {
    expect(shouldRefetchForIssueEvent("issue.created", { id: "m1", crew_id: "crew-2" }, "crew-1")).toBe(false)
  })

  it("refetches when crew_id is absent from the payload — can't rule it out", () => {
    expect(shouldRefetchForIssueEvent("issue.created", { id: "m1" }, "crew-1")).toBe(true)
  })

  it("always refetches issue.deleted regardless of crew filter", () => {
    expect(shouldRefetchForIssueEvent("issue.deleted", { identifier: "ENG-1" }, "crew-1")).toBe(true)
    expect(shouldRefetchForIssueEvent("issue.deleted", { identifier: "ENG-1", crew_id: "crew-2" }, "crew-1")).toBe(true)
  })

  it("always refetches issues.bulk_updated regardless of crew filter", () => {
    expect(shouldRefetchForIssueEvent("issues.bulk_updated", { count: "3" }, "crew-1")).toBe(true)
  })

  it("applies the same crew match to issue.status_changed and issue.updated", () => {
    expect(shouldRefetchForIssueEvent("issue.status_changed", { id: "m1", crew_id: "crew-1" }, "crew-1")).toBe(true)
    expect(shouldRefetchForIssueEvent("issue.status_changed", { id: "m1", crew_id: "crew-2" }, "crew-1")).toBe(false)
    expect(shouldRefetchForIssueEvent("issue.updated", { id: "m1", crew_id: "crew-2" }, "crew-1")).toBe(false)
  })

  it("ignores event types the issue board doesn't care about", () => {
    expect(shouldRefetchForIssueEvent("mission.updated", { id: "m1", crew_id: "crew-1" }, "crew-1")).toBe(false)
    expect(shouldRefetchForIssueEvent("agent.status", { crew_id: "crew-1" }, "crew-1")).toBe(false)
  })
})
