import { describe, it, expect } from "vitest"
import { initialSettingsTab, MOVED_SECTIONS } from "../settings-layout"

describe("initialSettingsTab", () => {
  it("returns the tab from a valid ?tab= param (deep-link lands on the right section)", () => {
    expect(initialSettingsTab("?tab=audit")).toBe("audit")
    expect(initialSettingsTab("?tab=members")).toBe("members")
    expect(initialSettingsTab("?tab=privacy")).toBe("privacy")
  })

  it("falls back to profile for a missing param", () => {
    expect(initialSettingsTab("")).toBe("profile")
    expect(initialSettingsTab("?foo=bar")).toBe("profile")
  })

  it("falls back to profile for an unknown tab value", () => {
    expect(initialSettingsTab("?tab=does-not-exist")).toBe("profile")
  })

  // A section that moved keeps its key resolvable, or the layout could not tell
  // "this link is stale, forward it" from "this link is junk, show Profile" —
  // and every bookmark, doc link and toolbar entry written before the move
  // would quietly land on the wrong page instead of the right one.
  it("keeps a moved section's key resolvable so the layout can forward it", () => {
    for (const key of Object.keys(MOVED_SECTIONS)) {
      expect(initialSettingsTab(`?tab=${key}`), key).toBe(key)
    }
  })
})

describe("MOVED_SECTIONS", () => {
  it("names both a destination and the page it belongs to", () => {
    for (const [key, moved] of Object.entries(MOVED_SECTIONS)) {
      expect(moved.href, `${key} href`).toMatch(/^\//)
      expect(moved.label.length, `${key} label`).toBeGreaterThan(0)
    }
  })

  it("sends the two notification sections to their own section of Integrations", () => {
    // Channels and the preference matrix are different questions; forwarding
    // both to a bare /integrations would make the second bookmark lie.
    expect(MOVED_SECTIONS["notifications"].href).toContain("section=connections")
    expect(MOVED_SECTIONS["notification-prefs"].href).toContain("section=preferences")
  })
})
