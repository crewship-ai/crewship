import { describe, it, expect } from "vitest"
import { missionToYaml } from "../mission-yaml-editor"
import type { Mission } from "@/lib/types/mission"

// The /issues list endpoint omits `tasks` (no include_tasks), so a mission
// can reach the Spec tab with tasks === undefined. Iterating that used to
// throw "undefined is not iterable" and crash the dashboard error boundary.
describe("missionToYaml", () => {
  it("does not throw when tasks is undefined (partially-loaded issue)", () => {
    const mission = {
      title: "Inbox redesign",
      status: "IN_PROGRESS",
      lead_agent_slug: "sam",
      // tasks intentionally absent — mirrors the /issues list shape
    } as unknown as Mission

    expect(() => missionToYaml(mission)).not.toThrow()
    const yaml = missionToYaml(mission)
    expect(yaml).toContain("title:")
    // status is escaped (quoted), so a missing value yields "" not null.
    expect(yaml).toContain('status: "IN_PROGRESS"')
    expect(yaml).toContain("tasks:")
  })

  it("does not throw when title is undefined", () => {
    const mission = { status: "TODO", lead_agent_slug: "riley" } as unknown as Mission
    expect(() => missionToYaml(mission)).not.toThrow()
  })

  it("renders task rows when tasks are present", () => {
    const mission = {
      title: "Feature",
      status: "TODO",
      lead_agent_slug: "sam",
      tasks: [{ id: "t1", title: "Do thing", status: "PENDING" }],
    } as unknown as Mission

    const yaml = missionToYaml(mission)
    expect(yaml).toContain("- id: t1")
    expect(yaml).toContain("title: \"Do thing\"")
  })
})
