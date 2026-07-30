import { describe, it, expect } from "vitest"
import { initialAdminTab } from "../page"

// Admin was the one console whose URL never changed — /admin whichever section
// you were on. So a section could not be bookmarked, pasted into a ticket, or
// survive a reload, and "look at the Keeper page" meant "open Admin, then click
// Keeper". Settings has had `?tab=` since its rewrite; this is the same contract.
describe("initialAdminTab", () => {
  it("returns the section from a valid ?tab= param", () => {
    expect(initialAdminTab("?tab=security")).toBe("security")
    expect(initialAdminTab("?tab=reviews")).toBe("reviews")
    expect(initialAdminTab("?tab=ratelimits")).toBe("ratelimits")
  })

  it("falls back to overview for a missing param", () => {
    expect(initialAdminTab("")).toBe("overview")
    expect(initialAdminTab("?foo=bar")).toBe("overview")
  })

  // An unknown key is a stale or hand-typed link. Overview is the section every
  // admin can read, so it is the one landing that is never a dead end.
  it("falls back to overview for an unknown section", () => {
    expect(initialAdminTab("?tab=does-not-exist")).toBe("overview")
    expect(initialAdminTab("?tab=")).toBe("overview")
  })

  // The two the user asked for by name, and the two most likely to be linked
  // from a ticket or a runbook.
  it("resolves the Keeper sections, which are the ones people link to", () => {
    expect(initialAdminTab("?tab=security")).toBe("security")
    expect(initialAdminTab("?tab=reviews")).toBe("reviews")
  })
})
