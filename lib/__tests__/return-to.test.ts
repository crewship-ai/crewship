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
    // Backslash is an authority separator in the WHATWG URL parser every
    // browser implements, so these start with "/" and not "//" — passing any
    // prefix-based check — and still resolve to http://evil.example/.
    "/\\evil.example",
    "/\\\\evil.example",
    "/\\/evil.example",
    "\\/evil.example",
  ])("refuses to send the user to %s", (hostile) => {
    expect(parseReturnTo(hostile, "Riley")).toBeNull()
  })

  // The escaped forms are NOT an escape: %5C stays a literal path character,
  // so these are ordinary in-app paths and must keep working.
  it("keeps genuinely in-app paths, including odd ones", () => {
    expect(parseReturnTo("/crews", "Crews")?.href).toBe("/crews")
    expect(parseReturnTo("/issues/OPS-4?tab=activity#note", "Issue")?.href)
      .toBe("/issues/OPS-4?tab=activity#note")
    expect(parseReturnTo("/%5Cevil.example", "Odd")?.href).toBe("/%5Cevil.example")
  })
})
