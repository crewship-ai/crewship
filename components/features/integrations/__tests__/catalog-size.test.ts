import { describe, expect, it } from "vitest"

import {
  buildServiceOptions,
  CATALOG_EXTRA_ENTRIES,
  catalogSections,
  catalogSize,
} from "../service-catalog"
import type { NotificationProvider } from "@/hooks/use-notification-channels"

function provider(over: Partial<NotificationProvider> = {}): NotificationProvider {
  return {
    provider: "discord",
    label: "Discord",
    blurb: "Post to a Discord channel",
    category: "chat",
    fields: [],
    enabled: true,
    ...over,
  }
}

/**
 * The sub-bar and the Add-integration wizard both state how many services
 * exist. They must state the same number.
 *
 * They did not, once: the bar counted the shoutrrr provider registry while the
 * catalog listed the registry plus the built-in transports. Two contradicting
 * counts a centimetre apart is how a page stops being believed. One helper
 * owns the arithmetic now, so the next entry has to update the constant rather
 * than only the JSX.
 */
describe("catalogSize", () => {
  it("counts the built-in transports on top of the registry", () => {
    expect(catalogSize(11)).toBe(11 + CATALOG_EXTRA_ENTRIES)
    expect(catalogSize(11)).toBe(13)
  })

  it("still reports the built-ins when the registry is empty", () => {
    // An instance with every provider disabled can still send e-mail and hit a
    // webhook, so "0 services available" would be wrong.
    expect(catalogSize(0)).toBe(CATALOG_EXTRA_ENTRIES)
  })
})

describe("buildServiceOptions", () => {
  it("matches what catalogSize promises", () => {
    const built = buildServiceOptions([provider(), provider({ provider: "slack", label: "Slack" })], {})
    expect(built).toHaveLength(catalogSize(2))
  })

  it("does not offer managed tools as a notification service", () => {
    // Tools/MCP has its own connect flow. Listing it here is what made the old
    // catalog show a card that could not be completed.
    const keys = buildServiceOptions([provider()], {}).map((s) => s.key)
    expect(keys).not.toContain("composio")
    expect(keys).toEqual(["discord", "email", "webhook"])
  })

  it("carries existing usage so a connected service says so", () => {
    const built = buildServiceOptions([provider()], { discord: 3, email: 1 })
    expect(built.find((s) => s.key === "discord")?.used).toBe(3)
    expect(built.find((s) => s.key === "email")?.used).toBe(1)
    expect(built.find((s) => s.key === "webhook")?.used).toBe(0)
  })

  it("keeps a provider an older server sent without a category", () => {
    // Dropping it would hide a working destination because the server is one
    // version behind; the built-in section is the honest place for it.
    const built = buildServiceOptions([provider({ category: undefined })], {})
    expect(built.find((s) => s.key === "discord")?.section).toBe("builtin")
  })

  it("marks a provider an admin disabled as unavailable rather than hiding it", () => {
    const built = buildServiceOptions([provider({ enabled: false })], {})
    expect(built.find((s) => s.key === "discord")?.available).toBe(false)
  })
})

describe("catalogSections", () => {
  it("appends the built-in section after the server's categories", () => {
    const got = catalogSections([
      { key: "chat", label: "Chat" },
      { key: "push", label: "Push" },
    ])
    expect(got.map((s) => s.key)).toEqual(["chat", "push", "builtin"])
  })

  it("preserves the server's order rather than sorting it", () => {
    // Order is a deliberate product decision on the Go side (chat first, then
    // push, then incident); re-sorting here would silently override it.
    const server = [
      { key: "incident", label: "Incident" },
      { key: "chat", label: "Chat" },
    ]
    expect(catalogSections(server).slice(0, 2).map((s) => s.key)).toEqual(["incident", "chat"])
  })
})
