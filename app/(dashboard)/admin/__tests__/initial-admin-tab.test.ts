import { describe, it, expect } from "vitest"
import { initialAdminTab, movedAdminTabHref, initialBackupsSection, adminSectionLabel, filterNav, sections, ALL_TABS } from "../navigation"

// Admin was the one console whose URL never changed — /admin whichever section
// you were on. So a section could not be bookmarked, pasted into a ticket, or
// survive a reload, and "look at the Keeper page" meant "open Admin, then click
// Keeper". Settings has had `?tab=` since its rewrite; this is the same contract.
describe("initialAdminTab", () => {
  it("returns the section from a valid ?tab= param", () => {
    expect(initialAdminTab("?tab=backups")).toBe("backups")
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

// Admin › Backups became six pages nested under Data › Backups, with Data
// retention beside it. `?tab=backups&section=` deep-links a page; the old bare
// `?tab=backups` keeps working and lands on its Overview.
describe("Backups pages", () => {
  it("reads ?section= for each of the six pages", () => {
    for (const s of ["overview", "history", "schedules", "storage", "recovery", "keys"]) {
      expect(initialBackupsSection(`?tab=backups&section=${s}`)).toBe(s)
    }
  })
  it("sends an old ?tab=backups link and an unknown section to Overview", () => {
    expect(initialAdminTab("?tab=backups")).toBe("backups")
    expect(initialBackupsSection("?tab=backups")).toBe("overview")
    expect(initialBackupsSection("?tab=backups&section=nope")).toBe("overview")
  })
  it("keeps ?tab=retention as its own section, named Data retention", () => {
    expect(initialAdminTab("?tab=retention")).toBe("retention")
    expect(adminSectionLabel("retention", "overview")).toBe("Data retention")
  })
  it("names the page in the sub-bar as Backups › page", () => {
    expect(adminSectionLabel("backups", "keys")).toBe("Backups › Keys & alerts")
    expect(adminSectionLabel("backups", "history")).toBe("Backups › Backup history")
  })
  it("nests the six pages under Data › Backups, Data retention beside it", () => {
    const data = sections.find((s) => s.label === "Data")!
    expect(data.items.map((i) => i.label)).toEqual(["Backups", "Data retention"])
    expect(data.items[0].children?.map((c) => c.label)).toEqual(["Overview", "Backup history", "Schedules", "Storage", "Recovery", "Keys & alerts"])
    expect(ALL_TABS).toContain("backups")
    expect(ALL_TABS).toContain("retention")
  })
  it("a search for a page keeps it under Backups; a search for Backups keeps all six", () => {
    const hit = filterNav("keys").flatMap((s) => s.items)
    expect(hit).toHaveLength(1)
    expect(hit[0].key).toBe("backups")
    expect(hit[0].children?.map((c) => c.key)).toEqual(["keys"])
    expect(filterNav("backup").flatMap((s) => s.items)[0].children).toHaveLength(6)
  })
})
