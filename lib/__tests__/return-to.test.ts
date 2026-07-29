import { describe, it, expect } from "vitest"

import { buildReturnTo, parseReturnTo, withReturnTo } from "../return-to"

describe("return-to", () => {
  it("round-trips an agent origin", () => {
    const qs = buildReturnTo("/crews?agent=riley", "Riley")
    const params = new URLSearchParams(qs)
    expect(parseReturnTo(params.get("from"), params.get("fromLabel")))
      .toEqual({ href: "/crews?agent=riley", label: "Riley" })
  })

  it("appends to a destination that already has a query", () => {
    expect(withReturnTo("/issues/OPS-4?tab=activity", "/crews?agent=riley", "Riley"))
      .toContain("?tab=activity&from=")
    expect(withReturnTo("/issues/OPS-4", "/crews?agent=riley", "Riley"))
      .toContain("OPS-4?from=")
  })

  it("falls back to a generic label", () => {
    expect(parseReturnTo("/issues", null)?.label).toBe("Back")
    expect(parseReturnTo("/issues", "   ")?.label).toBe("Back")
  })

  it("returns null when there is no origin", () => {
    expect(parseReturnTo(null, "Riley")).toBeNull()
    expect(parseReturnTo("", "Riley")).toBeNull()
  })

  // The destination comes from the URL, so it is attacker-controllable.
  it.each([
    "//evil.example",              // protocol-relative — a different origin
    "https://evil.example",
    "http://evil.example",
    "javascript:alert(1)",
    "evil.example",
    " /crews",                      // leading space would defeat startsWith
  ])("refuses to send the user to %s", (hostile) => {
    expect(parseReturnTo(hostile, "Riley")).toBeNull()
  })
})
