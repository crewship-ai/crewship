// The read-side of the automation substrate, as the two pages use it.
//
// Nothing here talks to the server. These are the three questions the UI has
// to answer from a plain `GET /api/v1/automations` payload, and each one has
// a wrong answer that is worse than no answer at all:
//
//   1. "What can start this routine?"  — naming a rule that targets a
//      DIFFERENT routine puts a trigger on a page it does not belong to.
//   2. "What can react to this issue?" — naming a rule whose matcher
//      excludes this issue tells the reader a change here will set something
//      off when it provably will not.
//   3. "What does this routine write?" — missing a `crewship` step buried in
//      a foreach body is the difference between a routine that reads the
//      board and one that edits it, reported as the former.

import { describe, it, expect } from "vitest"

import {
  automationsForIssue,
  automationsForRoutine,
  crewshipActionsInDefinition,
  ISSUE_SCOPED_EVENT_TYPES,
  type Automation,
} from "@/lib/automations"

function rule(over: Partial<Automation> = {}): Automation {
  return {
    id: "a1",
    workspace_id: "ws1",
    name: "Triage new bugs",
    enabled: true,
    event_type: "mission.status_change",
    matcher: {},
    action_kind: "routine",
    action: { routine_slug: "triage" },
    debounce_seconds: 10,
    max_per_hour: 60,
    created_at: "2026-08-07T10:00:00Z",
    updated_at: "2026-08-07T10:00:00Z",
    ...over,
  }
}

describe("automationsForRoutine", () => {
  it("keeps only the rules whose action targets this routine", () => {
    const list = [
      rule({ id: "a1", action: { routine_slug: "triage" } }),
      rule({ id: "a2", action: { routine_slug: "nightly-report" } }),
      rule({ id: "a3", action: { routine_slug: "triage" } }),
    ]
    expect(automationsForRoutine(list, "triage").map((a) => a.id)).toEqual(["a1", "a3"])
  })

  it("returns nothing for a routine no rule targets", () => {
    expect(automationsForRoutine([rule()], "some-other-routine")).toEqual([])
  })

  it("puts enabled rules first, then orders by name", () => {
    const list = [
      rule({ id: "off-a", name: "Alpha", enabled: false }),
      rule({ id: "on-z", name: "Zulu", enabled: true }),
      rule({ id: "on-b", name: "Bravo", enabled: true }),
    ]
    expect(automationsForRoutine(list, "triage").map((a) => a.id)).toEqual([
      "on-b",
      "on-z",
      "off-a",
    ])
  })

  it("survives a payload whose action came back without a slug", () => {
    const broken = { ...rule(), action: undefined } as unknown as Automation
    expect(automationsForRoutine([broken], "triage")).toEqual([])
  })
})

describe("automationsForIssue", () => {
  const issue = { missionId: "m-1", crewId: "crew-1" }

  it("keeps a rule whose matcher names this issue", () => {
    const list = [rule({ id: "a1", matcher: { mission_ids: ["m-1"] } })]
    expect(automationsForIssue(list, issue).map((a) => a.id)).toEqual(["a1"])
  })

  it("drops a rule whose mission_ids matcher excludes this issue", () => {
    const list = [rule({ id: "a1", matcher: { mission_ids: ["m-999"] } })]
    expect(automationsForIssue(list, issue)).toEqual([])
  })

  it("drops a rule whose crew_ids matcher excludes this issue's crew", () => {
    const list = [rule({ id: "a1", matcher: { crew_ids: ["crew-other"] } })]
    expect(automationsForIssue(list, issue)).toEqual([])
  })

  it("keeps a rule with an empty matcher — an empty field is don't-care", () => {
    expect(automationsForIssue([rule({ id: "a1" })], issue).map((a) => a.id)).toEqual(["a1"])
  })

  it("drops a rule whose event type cannot carry a mission at all", () => {
    // A rule on container metrics is a real rule; it is just not one this
    // issue can ever set off, and listing it here would be a lie of omission
    // dressed up as completeness.
    const list = [rule({ id: "a1", event_type: "container.metrics", matcher: {} })]
    expect(automationsForIssue(list, issue)).toEqual([])
  })

  it("keeps a disabled rule, so 'why did nothing happen' has an answer", () => {
    const list = [rule({ id: "a1", enabled: false })]
    expect(automationsForIssue(list, issue).map((a) => a.id)).toEqual(["a1"])
  })

  it("evaluates mission_ids and crew_ids together, not either-or", () => {
    // Right crew, wrong issue: the rule is still excluded. An OR here would
    // put every crew-scoped rule on every issue in the crew.
    const list = [
      rule({ id: "a1", matcher: { crew_ids: ["crew-1"], mission_ids: ["m-999"] } }),
      rule({ id: "a2", matcher: { crew_ids: ["crew-1"], mission_ids: ["m-1"] } }),
    ]
    expect(automationsForIssue(list, issue).map((a) => a.id)).toEqual(["a2"])
  })

  it("ignores agent_ids and severities, which no issue can decide statically", () => {
    // These narrow WHICH event fires, not whether the issue is in scope —
    // the emitting agent is not the assignee. Treating them as exclusions
    // would hide rules that do fire here.
    const list = [rule({ id: "a1", matcher: { agent_ids: ["agent-x"], severities: ["ERROR"] } })]
    expect(automationsForIssue(list, issue).map((a) => a.id)).toEqual(["a1"])
  })

  it("declares every issue-scoped event type as a journal entry type", () => {
    for (const t of ISSUE_SCOPED_EVENT_TYPES) {
      expect(t).toMatch(/^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$/)
    }
  })
})

describe("crewshipActionsInDefinition", () => {
  it("returns nothing for a routine with no crewship steps", () => {
    const def = { steps: [{ id: "s1", type: "agent_run" }, { id: "s2", type: "http" }] }
    expect(crewshipActionsInDefinition(def)).toEqual([])
  })

  it("names the verbs a routine acts with, de-duplicated and sorted", () => {
    const def = {
      steps: [
        { id: "s1", type: "crewship", action: "issue.comment" },
        { id: "s2", type: "crewship", action: "issue.create" },
        { id: "s3", type: "crewship", action: "issue.comment" },
      ],
    }
    expect(crewshipActionsInDefinition(def)).toEqual(["issue.comment", "issue.create"])
  })

  it("finds a crewship step nested in a foreach body", () => {
    // The whole point of the walk. A routine that files one issue per row of
    // a report keeps its only write inside the loop, and a top-level-only
    // scan reports it as read-only.
    const def = {
      steps: [
        {
          id: "fan",
          type: "foreach",
          foreach: {
            items: "{{ steps.report.output }}",
            steps: [{ id: "file", type: "crewship", action: "issue.create" }],
          },
        },
      ],
    }
    expect(crewshipActionsInDefinition(def)).toEqual(["issue.create"])
  })

  it("skips a crewship step that names no action", () => {
    const def = { steps: [{ id: "s1", type: "crewship" }] }
    expect(crewshipActionsInDefinition(def)).toEqual([])
  })

  it("tolerates a definition that is not shaped like one", () => {
    expect(crewshipActionsInDefinition(null)).toEqual([])
    expect(crewshipActionsInDefinition({})).toEqual([])
    expect(crewshipActionsInDefinition({ steps: "not-an-array" })).toEqual([])
  })
})
