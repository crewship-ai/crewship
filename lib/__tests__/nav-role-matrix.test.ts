import { describe, it, expect } from "vitest"

import { navSections, visibleNavItems, visiblePhoneTabs } from "../nav-sections"
import type { RestrictedSurface } from "../restricted-surfaces"

// CHR-01 (#3482), chrome-states G10/G11: which destinations each person sees.
// The whole matrix is pinned so that a nav row, a minRole or an order change
// cannot slip through unseen. The matrix describes the UI only; what the
// server lets a role open is decided server-side and is not changed here.

// Calendar (F-01) is a row only on a branch that carries the Calendar page; on
// those it sits right after Pages (PRD SHL-01), and every full session sees it.
const HAS_CALENDAR = navSections.some((s) => s.items.some((i) => i.href === "/calendar"))
const WORK_AND_BUILD = [
  "Dashboard", "Inbox", "Chat", "Activity",
  "Issues", "Routines", "Pages", ...(HAS_CALENDAR ? ["Calendar"] : []),
  "Crews", "Skills", "Vault", "Integrations",
]
const titles = (items: { title: string }[]) => items.map((i) => i.title)

const WORKSPACE_ROLES = ["OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"] as const

describe("role x navigation rows (full session)", () => {
  // Every workspace role sees the same rows: the product gates writes inside
  // the screens, not the places themselves. Admin is the instance console and
  // follows the instance-administrator flag, never the workspace role.
  it.each(WORKSPACE_ROLES)("%s sees every destination and Settings, no Admin", (role) => {
    expect(titles(visibleNavItems({ role, instanceAdmin: false, surfaces: null }))).toEqual([...WORK_AND_BUILD, "Settings"])
  })

  it.each(WORKSPACE_ROLES)("%s who is an instance administrator also sees Admin last", (role) => {
    expect(titles(visibleNavItems({ role, instanceAdmin: true, surfaces: null }))).toEqual([...WORK_AND_BUILD, "Settings", "Admin"])
  })

  it("never shows Journal (it lives inside Activity) or Marketplace", () => {
    for (const role of WORKSPACE_ROLES) {
      const t = titles(visibleNavItems({ role, instanceAdmin: true, surfaces: null }))
      expect(t).not.toContain("Journal")
      expect(t).not.toContain("Marketplace")
    }
  })

  it("loses no row to a role that has not loaded yet", () => {
    expect(titles(visibleNavItems({ role: null, instanceAdmin: false, surfaces: null }))).toEqual([...WORK_AND_BUILD, "Settings"])
  })

  it("keeps the order of the design: Work is Issues, Routines, Pages (then Calendar)", () => {
    const t = titles(visibleNavItems({ role: "MEMBER", instanceAdmin: false, surfaces: null }))
    expect(t.slice(4, HAS_CALENDAR ? 8 : 7)).toEqual(["Issues", "Routines", "Pages", ...(HAS_CALENDAR ? ["Calendar"] : [])])
  })
})

describe("Guest (restricted session) x navigation rows", () => {
  const cases: [string, RestrictedSurface[], string[]][] = [
    ["chat only", ["chat", "account_security"], ["Chat", "Settings"]],
    ["chat and issues", ["chat", "issues", "account_security"], ["Chat", "Issues", "Settings"]],
    ["issues, routines and pages", ["issues", "routines", "pages", "account_security"], ["Issues", "Routines", "Pages", "Settings"]],
    ["account security only", ["account_security"], ["Settings"]],
    ["nothing granted", [], []],
  ]
  it.each(cases)("%s", (_name, surfaces, expected) => {
    expect(titles(visibleNavItems({ role: "VIEWER", instanceAdmin: false, surfaces }))).toEqual(expected)
  })

  it("never offers Dashboard, Inbox, Activity, Calendar, Crews, Skills, Vault, Integrations or Admin", () => {
    const all: RestrictedSurface[] = ["issues", "chat", "routines", "pages", "account_security"]
    const t = titles(visibleNavItems({ role: "OWNER", instanceAdmin: true, surfaces: all }))
    for (const gone of ["Dashboard", "Inbox", "Activity", "Calendar", "Crews", "Skills", "Vault", "Integrations", "Admin"]) {
      expect(t).not.toContain(gone)
    }
  })
})

describe("phone tab bar x role", () => {
  it.each(WORKSPACE_ROLES)("%s gets Home, Inbox, Chat, Issues", (role) => {
    expect(titles(visiblePhoneTabs({ surfaces: null, role }))).toEqual(["Home", "Inbox", "Chat", "Issues"])
  })

  it("a Guest gets only the tabs they were granted (Issues may be missing)", () => {
    // Home leads to the Guest's own home (DSH-02), so the tab it replaces is not offered twice.
    const tabs = (surfaces: RestrictedSurface[]) =>
      visiblePhoneTabs({ surfaces, role: "VIEWER" }).map((i) => `${i.title} ${i.href}`)
    expect(tabs(["chat", "account_security"])).toEqual(["Home /chat"])
    expect(tabs(["chat", "issues"])).toEqual(["Home /chat", "Issues /issues"])
    expect(tabs(["issues"])).toEqual(["Home /issues"])
    expect(tabs([])).toEqual([])
  })
})
