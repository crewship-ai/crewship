import { describe, it, expect } from "vitest"

import { AGENT_TABS, CREW_TABS, agentTabFromParam, crewTabFromParam, tabParam } from "@/components/features/crews/crews-tabs"
import { activityHrefFor } from "@/components/features/crews/crews-bar"
import { restartsContainer } from "@/components/features/crews/crew-environment"
import { crewCommands } from "@/components/features/crews/crew-settings"

describe("crew and agent tabs", () => {
  it("are the Harbor 2 sets, in order", () => {
    expect(CREW_TABS.map((t) => t.label)).toEqual(["Overview", "Team", "Tools and access", "Environment", "Memory", "Settings"])
    expect(AGENT_TABS.map((t) => t.label)).toEqual(["Overview", "Instructions", "Model and limits", "Tools and access", "Memory", "Settings"])
  })

  it("?tab= opens the named tab; legacy values land where their content went", () => {
    expect(crewTabFromParam("settings")).toBe("settings")
    expect(crewTabFromParam("work")).toBe("overview")
    expect(crewTabFromParam("roster")).toBe("team")
    expect(crewTabFromParam("skills")).toBe("tools")
    expect(crewTabFromParam("credentials")).toBe("tools")
    expect(crewTabFromParam(null)).toBe("overview")
    expect(crewTabFromParam("banana")).toBe("overview")
    expect(agentTabFromParam("persona")).toBe("persona")
    expect(agentTabFromParam("skills")).toBe("tools")
    expect(agentTabFromParam("credentials")).toBe("tools")
    expect(agentTabFromParam("work")).toBe("overview")
    expect(agentTabFromParam("settings")).toBe("settings")
  })

  it("Overview stays out of the URL", () => {
    expect(tabParam("overview")).toBeNull()
    expect(tabParam("env")).toBe("env")
  })
})

describe("crew page helpers", () => {
  it("Activity opens filtered to the crew or the agent", () => {
    expect(activityHrefFor("crew", "c1", "Ops")).toBe("/activity?walk=crew%3Ac1")
    expect(activityHrefFor("agent", "a1", "Opsy")).toBe("/activity?walk=agent%3Aa1")
  })

  it("only network, resources, tools and the image restart the container", () => {
    expect(restartsContainer({ network_mode: "free" })).toBe(true)
    expect(restartsContainer({ container_memory_mb: 2048 })).toBe(true)
    expect(restartsContainer({ mcp_config_json: "{}" })).toBe(false)
  })

  it("the command line names commands the CLI has", () => {
    expect(crewCommands("ops")).toEqual(["crewship crew get ops", "crewship export crew ops", "crewship agent list --crew ops"])
  })
})
