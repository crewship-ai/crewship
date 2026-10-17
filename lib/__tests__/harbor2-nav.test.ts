import { describe, it, expect } from "vitest"

import { navSections, phoneTabs, isHiddenForRole } from "../nav-sections"

// Harbor 2 navigation (#3350): no heading for the places you start from,
// then Work and Build; Settings and Admin sit in the sidebar footer.

const shape = navSections.map((s) => ({ label: s.label, placement: s.placement ?? "main", items: s.items.map((i) => `${i.title} ${i.href}`) }))

describe("Harbor 2 navigation", () => {
  it("groups and orders the destinations like the design", () => {
    expect(shape).toEqual([
      { label: "", placement: "main", items: ["Dashboard /", "Inbox /inbox", "Chat /chat", "Activity /activity", "Journal /journal"] },
      { label: "Work", placement: "main", items: ["Issues /issues", "Routines /routines", "Pages /pages", "Calendar /calendar"] },
      { label: "Build", placement: "main", items: ["Crews /crews", "Skills /skills", "Vault /credentials", "Integrations /integrations"] },
      { label: "", placement: "footer", items: ["Settings /settings", "Admin /admin"] },
    ])
  })

  it("has no announced-but-unbuilt rows (Marketplace is gone)", () => {
    const all = navSections.flatMap((s) => s.items)
    // The "FUTURE" badge no longer exists in NavItem, so only the row itself can come back.
    expect(all.some((i) => i.href === "/marketplace" || i.title === "Marketplace")).toBe(false)
  })

  it("keeps Journal admin-only until it moves into Activity", () => {
    const journal = navSections.flatMap((s) => s.items).find((i) => i.href === "/journal")!
    expect(isHiddenForRole(journal, "MEMBER")).toBe(true)
    expect(isHiddenForRole(journal, "ADMIN")).toBe(false)
  })

  it("gives the phone Home, Inbox, Chat and Issues (More opens the rest)", () => {
    expect(phoneTabs().map((i) => i.href)).toEqual(["/", "/inbox", "/chat", "/issues"])
  })
})
