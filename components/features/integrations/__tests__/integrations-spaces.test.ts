import { describe, expect, it } from "vitest"
import {
  integrationsSearch,
  parseIntegrationsRoute,
  splitChannels,
  type IntegrationsRoute,
} from "../spaces"

// Integrations is four spaces (Tools, Triggers, Channels, Webhooks). The page
// used to be three tabs with `?tab=&section=`, and those links live on in the
// command palette, in Settings' retired tabs, in Routines and in the docs. They
// all have to keep landing on the thing they named.

const base: IntegrationsRoute = {
  space: "tools",
  view: "connected",
  id: null,
  pane: null,
  server: null,
  starts: null,
  target: null,
}

describe("parseIntegrationsRoute", () => {
  it("opens Tools by default, as the new layout does", () => {
    expect(parseIntegrationsRoute("")).toEqual(base)
  })

  it("opens the remembered space when the link names none", () => {
    expect(parseIntegrationsRoute("", "channels")).toMatchObject({ space: "channels", view: "all" })
  })

  it("reads the new space, view, object and pane", () => {
    expect(parseIntegrationsRoute("?space=webhooks&view=failing&id=ch1&pane=deliveries")).toEqual({
      ...base,
      space: "webhooks",
      view: "failing",
      id: "ch1",
      pane: "deliveries",
    })
  })

  it("falls back to the space's first view for a view it does not have", () => {
    expect(parseIntegrationsRoute("?space=webhooks&view=mine").view).toBe("all")
    expect(parseIntegrationsRoute("?space=nope").space).toBe("tools")
  })

  it.each([
    ["?tab=notifications&section=connections", { space: "channels", view: "all" }],
    ["?tab=notifications", { space: "channels", view: "all" }],
    ["?tab=notifications&section=preferences", { space: "channels", view: "preferences" }],
    ["?tab=notifications&section=deliveries", { space: "channels", view: "deliveries" }],
    ["?section=preferences", { space: "channels", view: "preferences" }],
    ["?tab=notifications&section=triggers", { space: "channels", view: "all" }],
  ])("Z12/Z13: %s lands on Channels", (search, want) => {
    expect(parseIntegrationsRoute(search)).toMatchObject(want)
  })

  it.each([
    ["?tab=incoming", { space: "triggers", view: "all", starts: null, target: null }],
    ["?tab=incoming&section=endpoints", { space: "triggers", view: "all", starts: null }],
    ["?tab=incoming&section=routine&target=review-pr", { space: "triggers", starts: "routine", target: "review-pr" }],
    ["?tab=incoming&section=agent&target=a1", { space: "triggers", starts: "agent", target: "a1" }],
    ["?tab=incoming&section=page", { space: "triggers", starts: "page", target: null }],
    // A target only means something next to the kind it belongs to.
    ["?tab=incoming&section=accounts&target=review-pr", { space: "triggers", starts: null, target: null }],
  ])("legacy incoming deep link %s opens filtered triggers", (search, want) => {
    expect(parseIntegrationsRoute(search)).toMatchObject(want)
  })

  it.each([
    ["?tab=tools", "connected"],
    ["?tab=tools&section=accounts", "connected"],
    ["?tab=tools&section=catalog", "catalog"],
    ["?tab=tools&section=agents", "connected"],
    ["?tab=tools&section=tools", "connected"],
    ["?tab=tools&section=triggers", "connected"],
    ["?tab=tools&section=mcp", "mcp"],
    ["?tab=tools&section=crew-tools", "crew-tools"],
    ["?tab=tools&section=nope", "connected"],
  ])("legacy tools section %s lands on Tools › %s", (search, view) => {
    expect(parseIntegrationsRoute(search)).toMatchObject({ space: "tools", view })
  })

  it("carries the crew server a Connect link named, and only on Tools", () => {
    expect(parseIntegrationsRoute("?tab=tools&section=crew-tools&server=srv_1")).toMatchObject({ view: "crew-tools", server: "srv_1" })
    expect(parseIntegrationsRoute("?tab=incoming&section=accounts&server=secret").server).toBeNull()
    expect(parseIntegrationsRoute("?space=channels&server=srv_1").server).toBeNull()
  })

  it("keeps the Starts facet on the new triggers link", () => {
    expect(parseIntegrationsRoute("?space=triggers&starts=agent&target=a1")).toMatchObject({ starts: "agent", target: "a1" })
    expect(parseIntegrationsRoute("?space=triggers&starts=nope&target=a1")).toMatchObject({ starts: null, target: null })
  })
})

describe("integrationsSearch", () => {
  it("writes the canonical link and drops the legacy keys", () => {
    const q = integrationsSearch("?tab=notifications&section=preferences&other=1", { ...base, space: "channels", view: "preferences" })
    const p = new URLSearchParams(q)
    expect(p.get("space")).toBe("channels")
    expect(p.get("view")).toBe("preferences")
    expect(p.get("tab")).toBeNull()
    expect(p.get("section")).toBeNull()
    expect(p.get("other")).toBe("1")
  })

  it("omits the default view and empty keys", () => {
    const p = new URLSearchParams(integrationsSearch("", { ...base, space: "webhooks", view: "all" }))
    expect([...p.keys()]).toEqual(["space"])
  })

  it("round-trips an object page", () => {
    const route: IntegrationsRoute = { ...base, space: "channels", view: "all", id: "c1", pane: "events" }
    expect(parseIntegrationsRoute(integrationsSearch("", route))).toEqual(route)
  })
})

describe("splitChannels", () => {
  it("type=webhook rows go to Webhooks, others to Channels", () => {
    const rows = [
      { id: "a", type: "email" },
      { id: "b", type: "webhook" },
      { id: "c", type: "shoutrrr" },
    ]
    const { channels, webhooks } = splitChannels(rows)
    expect(channels.map((r) => r.id)).toEqual(["a", "c"])
    expect(webhooks.map((r) => r.id)).toEqual(["b"])
  })
})
