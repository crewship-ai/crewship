import { describe, it, expect } from "vitest"

import { isAdminTier, isManagerTier, isOwner } from "@/lib/permissions/tiers"

// These mirror internal/api/rbac_routes.go. If the server's tiers move, these
// tests are the tripwire — a UI that gates on the wrong tier either offers a
// button that 403s or hides one the user is entitled to.

describe("role tiers", () => {
  it.each(["OWNER", "ADMIN"])("%s satisfies roleManage", (role) => {
    expect(isAdminTier(role)).toBe(true)
  })

  it.each(["MANAGER", "MEMBER", "VIEWER"])("%s does not satisfy roleManage", (role) => {
    expect(isAdminTier(role)).toBe(false)
  })

  it.each(["OWNER", "ADMIN", "MANAGER"])("%s satisfies roleCreate", (role) => {
    expect(isManagerTier(role)).toBe(true)
  })

  it.each(["MEMBER", "VIEWER"])("%s does not satisfy roleCreate", (role) => {
    expect(isManagerTier(role)).toBe(false)
  })

  it("only OWNER is the owner", () => {
    expect(isOwner("OWNER")).toBe(true)
    expect(isOwner("ADMIN")).toBe(false)
  })

  // Role arrives asynchronously from the workspace query. Defaulting an
  // unknown role to "permitted" would flash editable controls at a viewer
  // and snatch them back; defaulting to denied only shows them late.
  it.each([null, undefined, "", "SOMETHING_NEW"])("denies the unknown role %s", (role) => {
    expect(isAdminTier(role as string | null)).toBe(false)
    expect(isManagerTier(role as string | null)).toBe(false)
    expect(isOwner(role as string | null)).toBe(false)
  })
})
