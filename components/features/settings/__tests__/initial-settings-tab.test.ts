import { describe, it, expect } from "vitest"
import { initialFocusedMember, initialSettingsTab, MOVED_SECTIONS } from "../settings-layout"

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

describe("initialFocusedMember", () => {
  it("returns the user a ?member= deep link names", () => {
    expect(initialFocusedMember("?tab=members&member=u-fredy")).toBe("u-fredy")
  })

  it("returns nothing when the link names nobody", () => {
    expect(initialFocusedMember("?tab=members")).toBe("")
    expect(initialFocusedMember("")).toBe("")
  })

  // Both parsers take a query STRING, not window.location, so the layout can
  // seed them from useSearchParams. It used to read window.location.search in
  // a useState initializer, which during a client-side navigation still held
  // the PREVIOUS url — so every settings deep link in the product opened
  // Profile, while the same URL pasted into the address bar worked.
  it("parses a bare param string, with or without the leading ?", () => {
    expect(initialSettingsTab("tab=audit")).toBe("audit")
    expect(initialFocusedMember("tab=members&member=u-1")).toBe("u-1")
  })
})
