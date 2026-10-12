import { describe, it, expect } from "vitest"
import { initialAdminTab, movedAdminTabHref, initialBackupsSection, adminSectionLabel, filterNav, sections, ALL_TABS } from "../navigation"

// Admin was the one console whose URL never changed — /admin whichever section
// you were on. So a section could not be bookmarked, pasted into a ticket, or
// survive a reload, and "look at the Keeper page" meant "open Admin, then click
// Keeper". Settings has had `?tab=` since its rewrite; this is the same contract.
describe("initialAdminTab", () => {
  it("returns the section from a valid ?tab= param", () => {
    expect(initialAdminTab("?tab=retention")).toBe("retention")
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

})

// Workspaces and Users became one nested page, Admin › People & workspaces.
// A link someone saved to either tab still has to land on it.
describe("movedAdminTabHref", () => {
  it("sends the old Workspaces and Users tabs to the People page", () => {
    expect(movedAdminTabHref("?tab=users")).toBe("/admin/people")
    expect(movedAdminTabHref("?tab=workspaces")).toBe("/admin/people?view=workspaces")
  })
  it("sends Posture, Keeper and Keeper reviews to the Security page", () => {
    expect(movedAdminTabHref("?tab=posture")).toBe("/admin/security")
    expect(movedAdminTabHref("?tab=security")).toBe("/admin/security")
    expect(movedAdminTabHref("?tab=reviews")).toBe("/admin/security?section=activity")
  })
  it("leaves every other section where it is", () => {
    expect(movedAdminTabHref("?tab=ratelimits")).toBeNull()
    expect(movedAdminTabHref("")).toBeNull()
  })
})

// Admin › Backups is a nested page of its own (/admin/backups, a DrillPage
// like Security and People), with Data retention left beside it in the
// console. An old `?tab=backups&section=` link lands on the same page there,
// keeping its section, scope, workspaces and the run it pointed at.
describe("Backups pages", () => {
  it("sends an old ?tab=backups link to /admin/backups", () => {
    expect(movedAdminTabHref("?tab=backups")).toBe("/admin/backups")
  })
  it("keeps the section, scope, workspaces and run of an old link", () => {
    expect(movedAdminTabHref("?tab=backups&section=history&scope=workspaces&ws=dess,coolify&run=r1"))
      .toBe("/admin/backups?section=history&scope=workspaces&ws=dess%2Ccoolify&run=r1")
  })
  it("reads ?section= for each page and sends an unknown one to Overview", () => {
    for (const s of ["overview", "history", "schedules", "storage", "recovery", "keys"]) {
      expect(initialBackupsSection(`?section=${s}`)).toBe(s)
    }
    expect(initialBackupsSection("")).toBe("overview")
    expect(initialBackupsSection("?section=nope")).toBe("overview")
  })
  it("keeps ?tab=retention as its own section, named Data retention", () => {
    expect(initialAdminTab("?tab=retention")).toBe("retention")
    expect(adminSectionLabel("retention")).toBe("Data retention")
  })
  it("lists Backups under Data as one row that opens its own page, Data retention beside it", () => {
    const data = sections.find((s) => s.label === "Data")!
    expect(data.items.map((i) => i.label)).toEqual(["Backups", "Data retention"])
    expect(data.items[0].href).toBe("/admin/backups")
    expect(ALL_TABS).not.toContain("backups")
    expect(ALL_TABS).toContain("retention")
  })
  it("a search for Backups finds the row", () => {
    expect(filterNav("backup").flatMap((s) => s.items).map((i) => i.label)).toEqual(["Backups"])
  })
})
