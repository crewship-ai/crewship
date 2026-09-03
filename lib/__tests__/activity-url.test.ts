import { describe, it, expect } from "vitest"

import { activityUrl, activityUrlParams, parseActivityUrl } from "@/lib/activity-url"
import { ACTIVITY_HOME, openStop, selectStop, type ActivityPath } from "@/lib/activity-selection"

// The URL is the state on /activity: a reload resumes the walk, Back closes
// the drill-down, and a person can send the page they are looking at. These
// pin the round trip and the promise that every legacy inbound link still
// lands where it said.

function walk(...stops: [string, string][]): ActivityPath {
  let p = ACTIVITY_HOME
  for (const [kind, id] of stops) p = openStop(p, { kind, id, label: id })
  return p
}

describe("activityUrlParams", () => {
  it("writes nothing for the overview", () => {
    expect(activityUrl("/activity", { path: ACTIVITY_HOME, lens: "workflows" })).toBe("/activity")
  })

  it("spells a single run, issue or routine in the legacy vocabulary", () => {
    expect(activityUrlParams({ path: selectStop({ kind: "run", id: "run_1", label: "x" }), lens: "workflows" }).toString()).toBe("run=run_1")
    expect(activityUrlParams({ path: selectStop({ kind: "issue", id: "m_1", label: "x" }), lens: "workflows" }).toString()).toBe("mission=m_1")
    expect(activityUrlParams({ path: selectStop({ kind: "routine", id: "page-watch", label: "x" }), lens: "workflows" }).toString()).toBe("pipeline=page-watch")
  })

  it("spells the routine→run pair the way the routine card links it", () => {
    const p = walk(["routine", "page-watch"], ["run", "run_1"])
    expect(activityUrlParams({ path: p, lens: "workflows" }).toString()).toBe("pipeline=page-watch&run=run_1")
  })

  it("falls back to walk= for anything the legacy params cannot say", () => {
    const p = walk(["workflow", "chain_9"], ["run", "run_1"], ["agent", "ag_1"])
    expect(activityUrlParams({ path: p, lens: "workflows" }).get("walk")).toBe("workflow:chain_9/run:run_1/agent:ag_1")
  })

  it("carries lens, status and the opened record, omitting defaults", () => {
    const params = activityUrlParams({ path: ACTIVITY_HOME, lens: "issues", scope: "failed", entryId: "je_1" })
    expect(params.toString()).toBe("lens=issues&status=failed&entry=je_1")
    expect(activityUrlParams({ path: ACTIVITY_HOME, lens: "workflows", scope: "all" }).toString()).toBe("")
  })
})

describe("parseActivityUrl", () => {
  it("round-trips every shape", () => {
    const shapes: ActivityPath[] = [
      ACTIVITY_HOME,
      selectStop({ kind: "run", id: "run_1", label: "run_1" }),
      walk(["routine", "page-watch"], ["run", "run_1"]),
      walk(["workflow", "chain_9"], ["run", "run_1"], ["agent", "ag_1"]),
      walk(["issue", "m_1"], ["run", "run_2"]),
    ]
    for (const path of shapes) {
      const state = { path, lens: "agents" as const, scope: "failed", entryId: "je_9" }
      const back = parseActivityUrl(activityUrlParams(state))
      expect(back.path.stops.map((s) => [s.kind, s.id])).toEqual(path.stops.map((s) => [s.kind, s.id]))
      expect(back.lens).toBe("agents")
      expect(back.scope).toBe("failed")
      expect(back.entryId).toBe("je_9")
    }
  })

  it("still lands every legacy inbound link", () => {
    const fromInbox = parseActivityUrl(new URLSearchParams("run=run_1"))
    expect(fromInbox.path.stops.map((s) => s.kind)).toEqual(["run"])
    const fromRoutine = parseActivityUrl(new URLSearchParams("pipeline=page-watch&run=run_1"))
    expect(fromRoutine.path.stops.map((s) => [s.kind, s.id])).toEqual([["routine", "page-watch"], ["run", "run_1"]])
    const fromBell = parseActivityUrl(new URLSearchParams("status=active"))
    expect(fromBell.scope).toBe("active")
    expect(fromBell.path).toBe(ACTIVITY_HOME)
  })

  it("ignores an unknown lens and an empty entry", () => {
    const s = parseActivityUrl(new URLSearchParams("lens=bogus&entry="))
    expect(s.lens).toBe("workflows")
    expect(s.entryId).toBeUndefined()
  })

  it("survives an id with a slash or a colon", () => {
    const p = selectStop({ kind: "agent", id: "a/b:c", label: "x" })
    const back = parseActivityUrl(activityUrlParams({ path: openStop(p, { kind: "run", id: "r", label: "r" }), lens: "workflows" }))
    expect(back.path.stops[0].id).toBe("a/b:c")
    expect(back.path.stops[1].id).toBe("r")
  })
})
