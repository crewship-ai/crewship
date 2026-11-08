import { describe, expect, it } from "vitest"
import { RESTRICTED_SURFACE_PATHS, restrictedHome, restrictedLegacyPath, restrictedPathAllowed, restrictedSurfacesOf } from "../restricted-surfaces"
import { visibleItems } from "../nav-sections"

describe("restricted member navigation", () => {
  const rows = [
    { id: "alex-workspace", restricted_session: true, currentUserAccessMode: "restricted" as const, restricted_surfaces: ["chat", "account_security"] },
    { id: "empty-workspace", restricted_session: true, currentUserAccessMode: "trusted" as const, restricted_surfaces: ["account_security"] },
  ]
  it("uses the selected workspace membership rather than the first restricted row", () => {
    expect(restrictedSurfacesOf(rows, "empty-workspace")).toEqual(["account_security"])
    expect(restrictedSurfacesOf(rows, "alex-workspace")).toEqual(["chat", "account_security"])
  })
  it("fails closed for an unknown selected workspace and missing projection", () => {
    expect(restrictedSurfacesOf(rows, "unknown")).toEqual([])
    expect(restrictedSurfacesOf([{ id: "missing", restricted_session: true }], "missing")).toEqual([])
  })
  it("keeps exact path boundaries and routes away from unavailable content", () => {
    expect(restrictedPathAllowed("/chat/alex", ["chat"])).toBe(true)
    expect(restrictedPathAllowed("/chatty", ["chat"])).toBe(false)
    expect(restrictedPathAllowed("/routines", ["account_security"])).toBe(false)
    expect(restrictedHome(["account_security"])).toBe("/settings")
    expect(restrictedHome(["chat", "account_security"])).toBe("/chat")
  })
  // #5132: the pages surface is the member's Artifacts screen; /pages is only an old address that redirects.
  it("maps the pages surface to /artifacts and keeps /pages only as a redirect address", () => {
    expect(RESTRICTED_SURFACE_PATHS.pages).toBe("/artifacts")
    expect(restrictedPathAllowed("/artifacts", ["pages"])).toBe(true)
    expect(restrictedPathAllowed("/artifacts/page-a", ["pages"])).toBe(true)
    expect(restrictedPathAllowed("/pages", ["pages"])).toBe(false)
    expect(restrictedPathAllowed("/artifacts", ["chat"])).toBe(false)
    expect(restrictedLegacyPath("/pages", ["pages"])).toBe(true)
    expect(restrictedLegacyPath("/pages/brief", ["pages"])).toBe(true)
    expect(restrictedLegacyPath("/pagesx", ["pages"])).toBe(false)
    expect(restrictedLegacyPath("/pages", ["chat", "routines"])).toBe(false)
    expect(restrictedHome(["pages"])).toBe("/artifacts")
  })
  it("points a restricted member's Artifacts row at /artifacts whatever the build's flag", () => {
    const row = { title: "Artifacts", href: "/pages", icon: (() => null) as never }
    expect(visibleItems([row], { role: "MEMBER", surfaces: ["pages"] }).map(item => item.href)).toEqual(["/artifacts"])
    expect(visibleItems([row], { role: "MEMBER", surfaces: ["chat"] })).toEqual([])
    expect(visibleItems([row], { role: "MEMBER", surfaces: null }).map(item => item.href)).toEqual(["/pages"])
  })
})
