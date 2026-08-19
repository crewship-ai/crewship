import { describe, it, expect, vi } from "vitest"

import { CONCEPT_ICON } from "@/lib/concept-icons"

// app-toolbar reaches for a lot of live machinery at import time (auth,
// realtime, the command palette). None of it matters for a question about a
// static array, so stub the leaves and keep the module graph cheap.
vi.mock("@/hooks/use-auth", () => ({ useAuth: () => ({ session: null, signOut: vi.fn() }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtime: () => ({ status: "connected" }) }))
vi.mock("@/components/command-palette", () => ({ CommandPalette: () => null }))

import { navSections } from "../app-sidebar"
import { mobileNavSections } from "../app-toolbar"

interface NavItem {
  title: string
  href: string
  icon: unknown
}

function flatten(sections: readonly { label: string; items: readonly NavItem[] }[]) {
  return sections.flatMap((s) => s.items.map((i) => ({ ...i, group: s.label })))
}

// =============================================================================
// Chat was not buried in the navigation — it was absent from it. The product
// had no front door to its agents on either breakpoint, so the only way to
// reach a conversation was to already know the agent's slug.
//
// This pins the entry on BOTH nav definitions, because they are two hand-kept
// arrays and "we added it to the sidebar" is exactly how the phone stayed
// broken last time.
// =============================================================================

describe("Chat is in the navigation", () => {
  it("the desktop rail has a Chat entry pointing at /chat", () => {
    const chat = flatten(navSections).find((i) => i.href === "/chat")
    expect(chat, "no /chat entry in navSections").toBeTruthy()
    expect(chat!.title).toBe("Chat")
  })

  it("the mobile sheet has a Chat entry pointing at /chat", () => {
    const chat = flatten(mobileNavSections).find((i) => i.href === "/chat")
    expect(chat, "no /chat entry in mobileNavSections").toBeTruthy()
    expect(chat!.title).toBe("Chat")
  })

  it("wears the product's conversation icon on both breakpoints", () => {
    // Chat is the same concept the agent overview calls "Sessions". Picking a
    // second icon for it is the drift lib/concept-icons exists to stop.
    const desktop = flatten(navSections).find((i) => i.href === "/chat")!
    const mobile = flatten(mobileNavSections).find((i) => i.href === "/chat")!
    expect(desktop.icon).toBe(CONCEPT_ICON.sessions)
    expect(mobile.icon).toBe(CONCEPT_ICON.sessions)
  })

  it("claims /chat exactly once per nav", () => {
    // Two rows for one route means one of them is always the wrong one to
    // click, and both light up as active.
    expect(flatten(navSections).filter((i) => i.href === "/chat")).toHaveLength(1)
    expect(flatten(mobileNavSections).filter((i) => i.href === "/chat")).toHaveLength(1)
  })

  it("does not link at the per-agent route, which needs a slug nobody has yet", () => {
    for (const item of [...flatten(navSections), ...flatten(mobileNavSections)]) {
      expect(item.href).not.toMatch(/^\/chat\/.+/)
    }
  })
})
