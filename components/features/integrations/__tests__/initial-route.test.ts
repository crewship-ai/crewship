import { describe, it, expect } from "vitest"
import { initialIntegrationsRoute } from "../integrations-layout"

// /integrations became the one home for notification channels, the preference
// matrix and Composio's tool views. That only holds up if a link can name a
// section: Settings forwards two stale tabs here, the docs point at "Tools
// (MCP) → Connected accounts", and neither can land on a page that always
// opens on Connections and makes the reader hunt.

describe("initialIntegrationsRoute", () => {
  it("defaults to the Notifications tab and its Connections section", () => {
    expect(initialIntegrationsRoute("")).toEqual({
      tab: "notifications",
      notifySection: "connections",
      mcpSection: "accounts",
      server: null,
    })
  })

  it("opens the preference matrix from ?section=preferences", () => {
    const r = initialIntegrationsRoute("?tab=notifications&section=preferences")
    expect(r.tab).toBe("notifications")
    expect(r.notifySection).toBe("preferences")
  })

  it("opens a Composio view from the tools tab", () => {
    const r = initialIntegrationsRoute("?tab=tools&section=triggers")
    expect(r.tab).toBe("tools")
    expect(r.mcpSection).toBe("triggers")
  })

  it("ignores a section that belongs to the other tab", () => {
    // ?tab=notifications&section=triggers is a link someone hand-edited; it
    // must not leave the Notifications panel selecting a Composio section.
    const r = initialIntegrationsRoute("?tab=notifications&section=triggers")
    expect(r.notifySection).toBe("connections")
  })

  it("falls back to the defaults for unknown values", () => {
    expect(initialIntegrationsRoute("?tab=nope&section=nope")).toEqual({
      tab: "notifications",
      notifySection: "connections",
      mcpSection: "accounts",
      server: null,
    })
  })

  it("opens Crew tools on the tools tab, carrying the server a Connect link named", () => {
    const r = initialIntegrationsRoute("?tab=tools&section=crew-tools&server=srv_1")
    expect(r.tab).toBe("tools")
    expect(r.mcpSection).toBe("crew-tools")
    expect(r.server).toBe("srv_1")
    // The server only means something on the tools tab.
    expect(initialIntegrationsRoute("?tab=notifications&server=srv_1").server).toBeNull()
  })
})
