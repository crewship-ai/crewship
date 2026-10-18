import { describe, it, expect } from "vitest"
import { getConfig } from "@testing-library/react"

// findBy*/waitFor waited the library default 1 s. In full parallel runs
// five unrelated tests (issue detail, realtime, members, chat decisions,
// page import) timed out at 1.0–1.7 s and passed alone. The wait is a
// ceiling, not a delay: a test that finds its element sooner is unchanged.
describe("Testing Library async wait", () => {
  it("allows 3 s for findBy/waitFor under full-suite load", () => {
    expect(getConfig().asyncUtilTimeout).toBe(3000)
  })
})
