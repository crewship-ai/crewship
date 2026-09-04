import { describe, it, expect } from "vitest"
import { readinessScopeLabel } from "@/components/features/credentials/credentials-overview"

// The readiness probe stops at 24 crews; "across 24 crews" on a 103-crew
// workspace read as the whole fleet (audit-fleet.md §5 item 5).
describe("readinessScopeLabel", () => {
  it("says when the count is partial", () => {
    expect(readinessScopeLabel({ loading: false, checked: 24, total: 103 })).toBe("checked 24 of 103 crews")
  })
  it("keeps the short form when every crew answered", () => {
    expect(readinessScopeLabel({ loading: false, checked: 3, total: 3 })).toBe("across 3 crews")
    expect(readinessScopeLabel({ loading: false, checked: 1, total: 1 })).toBe("across 1 crew")
  })
  it("never invents a scope while loading or with nothing reported", () => {
    expect(readinessScopeLabel({ loading: true, checked: 0, total: 5 })).toBe("checking crews…")
    expect(readinessScopeLabel({ loading: false, checked: 0, total: 5 })).toBe("no crew reported")
  })
})
