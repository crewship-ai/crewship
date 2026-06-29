import { describe, it, expect } from "vitest"
import { initialSettingsTab } from "../settings-layout"

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
})
