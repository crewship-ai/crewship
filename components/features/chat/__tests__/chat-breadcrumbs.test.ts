import { describe, expect, it } from "vitest"

import { chatBreadcrumbs } from "../chat-breadcrumbs"

describe("chatBreadcrumbs", () => {
  it("names the crew and the agent, and links each to where it lives", () => {
    expect(chatBreadcrumbs({ name: "Riley", slug: "riley", crew: { name: "Ops", slug: "ops" } })).toEqual([
      { label: "Crews", href: "/crews" },
      { label: "Ops", href: "/crews?crew=ops" },
      { label: "Riley", href: "/crews?agent=riley" },
    ])
  })

  it("skips the crew when the agent has none", () => {
    expect(chatBreadcrumbs({ name: "Loner", slug: "loner", crew: null }).map((c) => c.label)).toEqual(["Crews", "Loner"])
  })

  it("never shows a slug as a label", () => {
    const crumbs = chatBreadcrumbs({ name: "Crewship Guide", slug: "_crewship-setup-guide", crew: null })
    for (const c of crumbs) expect(c.label).not.toMatch(/^_|-setup-guide/)
  })

  it("is empty until the agent resolves", () => {
    expect(chatBreadcrumbs(null)).toEqual([])
  })
})
