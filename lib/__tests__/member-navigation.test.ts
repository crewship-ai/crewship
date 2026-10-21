import { describe, expect, it } from "vitest"
import { restrictedHome, restrictedPathAllowed, restrictedSurfacesOf } from "../restricted-surfaces"

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
})
