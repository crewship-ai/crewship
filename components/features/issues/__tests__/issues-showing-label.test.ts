import { describe, it, expect } from "vitest"

import { issuesShowingLabel } from "@/components/features/orchestration/issues-toolbar-strip"

// The board said "100 issues" on a workspace with 1 015 because it counted
// the page it received. The label reads the server's total (X-Total-Count,
// via usePagedList) and is silent when the page IS the list.

describe("issuesShowingLabel", () => {
  it("is silent while the total is unknown or everything is loaded", () => {
    expect(issuesShowingLabel(15, null)).toBeNull()
    expect(issuesShowingLabel(15, 15)).toBeNull()
    expect(issuesShowingLabel(0, 0)).toBeNull()
  })

  it("says how much of the list is loaded", () => {
    expect(issuesShowingLabel(100, 1015)).toBe("Showing newest 100 of 1 015")
    expect(issuesShowingLabel(200, 1015)).toBe("Showing newest 200 of 1 015")
  })
})
