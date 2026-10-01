import { describe, it, expect } from "vitest"

import { familyKey, foldIssueFamilies, memberTitle } from "@/components/features/issues/issue-families"
import type { Mission, MissionStatus } from "@/lib/types/mission"

// The explorer on dev3 listed "infra-prehled/verdikt …" fifteen times, each
// truncated to the same prefix, so no row could be told from its neighbour.
// Generated issues share a source prefix; the rail folds a run of three or
// more into one row and names the rest of the title once it is open.

function issue(id: string, title: string, status: MissionStatus = "BACKLOG"): Mission {
  return { id, identifier: id, title, status } as unknown as Mission
}

describe("familyKey", () => {
  it.each([
    ["infra-prehled/verdikt stopped reporting", "infra-prehled/verdikt"],
    ['infra-prehled/verdikt: any(state == "critical")', "infra-prehled/verdikt"],
    ["coolify/prehled stopped reporting", "coolify/prehled"],
    ["[e2e-scan] test-crew-links.sh", "[e2e-scan]"],
    ["Fix the flaky test", null],
    ["Replicate https://www.seznam.cz as a page", null],
    ["a/b", null],
  ])("%s → %s", (title, want) => {
    expect(familyKey(title)).toBe(want)
  })
})

describe("memberTitle", () => {
  it.each([
    ["infra-prehled/verdikt stopped reporting", "infra-prehled/verdikt", "stopped reporting"],
    ['infra-prehled/verdikt: any(state == "critical")', "infra-prehled/verdikt", 'any(state == "critical")'],
    ["[e2e-scan] test-crew-links.sh", "[e2e-scan]", "test-crew-links.sh"],
    ["[e2e-scan]", "[e2e-scan]", "[e2e-scan]"],
  ])("%s under %s → %s", (title, key, want) => {
    expect(memberTitle(title, key)).toBe(want)
  })
})

describe("foldIssueFamilies", () => {
  const verdikt = (n: number, status: MissionStatus = "BACKLOG") =>
    issue(`INF-${n}`, `infra-prehled/verdikt stopped reporting`, status)

  it("folds three or more of a family into one entry at the first member's place", () => {
    const rows = [issue("A-1", "Plain issue"), verdikt(3), issue("B-1", "Another"), verdikt(2), verdikt(1)]
    const out = foldIssueFamilies(rows, { minGroup: 3 })
    expect(out.map((e) => (e.kind === "issue" ? e.issue.id : `${e.key}×${e.issues.length}`))).toEqual([
      "A-1",
      "infra-prehled/verdikt×3",
      "B-1",
    ])
  })

  it("leaves a family below the threshold as plain rows", () => {
    const out = foldIssueFamilies([verdikt(1), verdikt(2)], { minGroup: 3 })
    expect(out.map((e) => e.kind)).toEqual(["issue", "issue"])
  })

  it("keeps rows that need a person outside the fold", () => {
    const rows = [verdikt(4, "FAILED"), verdikt(3), verdikt(2), verdikt(1)]
    const out = foldIssueFamilies(rows, { minGroup: 3 })
    expect(out.map((e) => (e.kind === "issue" ? e.issue.id : `${e.key}×${e.issues.length}`))).toEqual([
      "INF-4",
      "infra-prehled/verdikt×3",
    ])
  })

  it("does not fold at all while searching", () => {
    const rows = [verdikt(3), verdikt(2), verdikt(1)]
    expect(foldIssueFamilies(rows, { minGroup: 3, disabled: true }).map((e) => e.kind)).toEqual([
      "issue",
      "issue",
      "issue",
    ])
  })

  it("keeps the input order inside a family", () => {
    const rows = [verdikt(9), issue("X-1", "coolify/prehled down"), verdikt(5), verdikt(7)]
    const fam = foldIssueFamilies(rows, { minGroup: 3 }).find((e) => e.kind === "family")
    expect(fam && fam.kind === "family" ? fam.issues.map((i) => i.id) : []).toEqual(["INF-9", "INF-5", "INF-7"])
  })
})
