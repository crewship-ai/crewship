import { describe, it, expect } from "vitest"
import { safeRedirectPath } from "./page"

// safe-redirect.test.ts pins the post-login open-redirect guard. The TS
// guard mirrors the server-side isSafeRedirect (internal/api/helpers.go):
// only same-origin relative paths are allowed, and backslashes are
// rejected because browsers normalize "\" → "/", which would otherwise
// turn "\\evil.com" / "/\evil.com" into protocol-relative URLs.

const SAFE_DEFAULT = "/"

describe("safeRedirectPath", () => {
  it("allows a plain relative path", () => {
    expect(safeRedirectPath("/dashboard")).toBe("/dashboard")
  })

  it("allows nested relative paths", () => {
    expect(safeRedirectPath("/workspaces/abc/agents")).toBe("/workspaces/abc/agents")
  })

  it("falls back to default for null/empty", () => {
    expect(safeRedirectPath(null)).toBe(SAFE_DEFAULT)
    expect(safeRedirectPath("")).toBe(SAFE_DEFAULT)
  })

  it.each([
    ["protocol-relative //", "//evil.com"],
    ["backslash protocol-relative /\\", "/\\evil.com"],
    ["double backslash", "\\\\evil.com"],
    ["absolute https URL", "https://evil.com"],
    ["javascript scheme", "javascript:alert(1)"],
    ["does not start with slash", "evil.com"],
    ["embedded backslash", "/foo\\bar"],
  ])("rejects %s", (_label, input) => {
    expect(safeRedirectPath(input)).toBe(SAFE_DEFAULT)
  })

  it("still rejects /login bounce variants", () => {
    expect(safeRedirectPath("/login")).toBe(SAFE_DEFAULT)
    expect(safeRedirectPath("/login?x=1")).toBe(SAFE_DEFAULT)
    expect(safeRedirectPath("/login/")).toBe(SAFE_DEFAULT)
    expect(safeRedirectPath("/login#h")).toBe(SAFE_DEFAULT)
  })
})
