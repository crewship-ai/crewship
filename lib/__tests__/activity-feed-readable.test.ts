import { describe, expect, it } from "vitest"

import type { JournalEntry } from "@/lib/types/journal"
import { CHANGE_KIND_LABEL, changeFromEntry, type ChangeContext } from "@/lib/activity-feed"

// #3636: a change row is titled with the object's name and says the action
// and the person under it (v67 `acrow`). The shapes below are real journal
// entries read off dev1, ids shortened.

function entry(over: Partial<JournalEntry>): JournalEntry {
  return {
    id: "e1",
    ts: "2026-10-18T19:25:00Z",
    workspace_id: "ws",
    entry_type: "audit.entity_updated",
    severity: "info",
    actor_type: "user",
    summary: "",
    payload: {},
    ...over,
  } as JournalEntry
}

const ctx: ChangeContext = {
  agentName: (id) => ({ agt_taylor: "Taylor" })[id],
  crewName: (id) => ({ crw_ops: "Operations" })[id],
  personName: (id) => ({ usr_jana: "Jana Nováková" })[id],
  issueLabel: (id) => ({ m1: "ENG-16 Fix flaky login" })[id],
}

describe("change rows read as names, not ids (#3636)", () => {
  it("names the person who made the change", () => {
    const c = changeFromEntry(entry({ actor_id: "usr_jana", payload: { entity_type: "crew", entity_id: "crw_ops" } }), ctx)!
    expect(c.who).toBe("Jana Nováková")
    // Someone the lookup does not know keeps the neutral "A person".
    expect(changeFromEntry(entry({ actor_id: "usr_gone", payload: { entity_type: "crew", entity_id: "crw_ops" } }), ctx)!.who).toBe("A person")
  })

  it("titles an audited agent or crew with its name, and never shouts the entity type", () => {
    const agent = changeFromEntry(
      entry({ summary: "update AGENT cmvdmezwy004", payload: { action: "update", entity_id: "agt_taylor", entity_type: "AGENT", metadata: { avatar_seed: "x" } } }),
      ctx,
    )!
    expect(agent.object).toBe("Agent Taylor")
    expect(agent.action).toBe("Updated")
    const crew = changeFromEntry(entry({ payload: { entity_type: "CREW", entity_id: "crw_ops" } }), ctx)!
    expect(crew.object).toBe("Crew Operations")
    // An agent the lookup no longer has: the type in words, no raw id.
    expect(changeFromEntry(entry({ payload: { entity_type: "AGENT", entity_id: "cmvdmezwy004ce8d70a22" } }), ctx)!.object).toBe("Agent")
  })

  it("titles an artifact change with the artifact, and says it was created", () => {
    const c = changeFromEntry(
      entry({
        entry_type: "page.spec.changed",
        actor_type: "system",
        actor_id: "pages",
        summary: "page palette-page-6b963867 created: 1 panel(s) — status",
        payload: { created: true, page: "palette-page-6b963867", page_id: "p1", panels: ["status"] },
      }),
      ctx,
    )!
    expect(c.object).toBe("palette-page-6b963867")
    expect(c.action).toBe("Created")
    expect(c.who).toBe("System")
    expect(changeFromEntry(entry({ entry_type: "page.spec.changed", payload: { page: "weekly-report", panels: ["a", "b"] } }), ctx)!.action).toBe("Updated")
  })

  it("titles an issue change with the issue, not the event's own words", () => {
    const created = changeFromEntry(
      entry({ entry_type: "mission.created", summary: "created: E2E needs-human 1792276975468", payload: { action: "created", details: "E2E needs-human 1792276975468" } }),
      ctx,
    )!
    expect(created.object).toBe("E2E needs-human 1792276975468")
    expect(created.action).toBe("Created")
    const known = changeFromEntry(entry({ entry_type: "mission.created", mission_id: "m1", summary: "created: Fix flaky login", payload: { details: "Fix flaky login" } }), ctx)!
    expect(known.object).toBe("ENG-16 Fix flaky login")
  })

  it("spells statuses as words and names the new assignee", () => {
    const moved = changeFromEntry(
      entry({ entry_type: "mission.status_change", mission_id: "m1", summary: "status_changed: BACKLOG → TODO", payload: { action: "status_changed", details: "BACKLOG → TODO", from: "BACKLOG", to: "TODO" } }),
      ctx,
    )!
    expect(moved.diff).toBe("Backlog → Todo")
    expect(changeFromEntry(entry({ entry_type: "mission.status_change", payload: { from: "IN_PROGRESS", to: "IN_REVIEW" } }), ctx)!.diff).toBe("In progress → In review")
    const assigned = changeFromEntry(
      entry({ entry_type: "mission.assigned", mission_id: "m1", summary: "assignee_changed: assignee_id: agt_taylor", payload: { action: "assignee_changed", details: "assignee_id: agt_taylor" } }),
      ctx,
    )!
    expect(assigned.object).toBe("ENG-16 Fix flaky login")
    expect(assigned.diff).toBe("to Taylor")
  })

  it("calls the artifacts group Artifacts, as the rail does", () => {
    expect(CHANGE_KIND_LABEL.artifact).toBe("Artifacts")
  })
})
