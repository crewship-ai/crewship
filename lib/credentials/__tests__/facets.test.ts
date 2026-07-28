// The /credentials sidebar is a filter surface, so every number on it is a
// claim about the list beside it. These tests pin the two ways that goes
// wrong: a count that disagrees with what the filter actually selects, and a
// facet that silently swallows rows (an unknown category, a crew the user
// cannot name).

import { describe, it, expect } from "vitest"
import {
  EMPTY_CREDENTIAL_FILTERS,
  applyCredentialFilters,
  buildCategoryFacet,
  buildScopeFacet,
  categoryOf,
  deriveCredentialStatus,
  needsAttention,
  type CredentialLike,
} from "../facets"

const DAY = 24 * 3600 * 1000

function cred(overrides: Partial<CredentialLike> = {}): CredentialLike {
  return {
    id: "c1",
    name: "GH_TOKEN",
    description: null,
    provider: "GITHUB",
    status: "ACTIVE",
    scope: "WORKSPACE",
    crew_ids: [],
    account_label: null,
    tags: [],
    token_expires_at: null,
    last_used_at: new Date().toISOString(),
    ...overrides,
  }
}

describe("deriveCredentialStatus", () => {
  it("reports an agent-proposed credential as Pending, ahead of every other rule", () => {
    expect(deriveCredentialStatus(cred({ status: "PENDING_APPROVAL", last_used_at: null }))).toBe("Pending")
  })

  it.each(["EXPIRED", "REVOKED", "ERROR", "RATE_LIMITED"])("reports %s as Error", (status) => {
    expect(deriveCredentialStatus(cred({ status }))).toBe("Error")
  })

  it("reports an expiry in the past as Error even when the row still says ACTIVE", () => {
    expect(
      deriveCredentialStatus(cred({ token_expires_at: new Date(Date.now() - DAY).toISOString() })),
    ).toBe("Error")
  })

  it("reports a credential unused for more than 90 days as Stale", () => {
    expect(
      deriveCredentialStatus(cred({ last_used_at: new Date(Date.now() - 91 * DAY).toISOString() })),
    ).toBe("Stale")
  })

  it("treats an unparseable timestamp as no information rather than as breakage", () => {
    expect(deriveCredentialStatus(cred({ token_expires_at: "not-a-date", last_used_at: "nope" }))).toBe(
      "Connected",
    )
  })

  it("reports a healthy, recently-used credential as Connected", () => {
    expect(deriveCredentialStatus(cred())).toBe("Connected")
  })
})

describe("needsAttention", () => {
  it("includes anything expiring inside 30 days, while it is still working", () => {
    expect(needsAttention(cred({ token_expires_at: new Date(Date.now() + 10 * DAY).toISOString() }))).toBe(true)
  })

  it("excludes an expiry comfortably in the future", () => {
    expect(needsAttention(cred({ token_expires_at: new Date(Date.now() + 200 * DAY).toISOString() }))).toBe(false)
  })

  it.each(["Error", "Stale", "Pending"])("includes anything in %s", (want) => {
    const byStatus: Record<string, CredentialLike> = {
      Error: cred({ status: "REVOKED" }),
      Stale: cred({ last_used_at: new Date(Date.now() - 200 * DAY).toISOString() }),
      Pending: cred({ status: "PENDING_APPROVAL" }),
    }
    expect(needsAttention(byStatus[want])).toBe(true)
  })

  it("excludes a healthy credential", () => {
    expect(needsAttention(cred())).toBe(false)
  })
})

describe("categoryOf", () => {
  it("reads the category off the brand registry", () => {
    expect(categoryOf(cred({ provider: "GITHUB" }))).toBe("Source")
    expect(categoryOf(cred({ provider: "ANTHROPIC" }))).toBe("AI")
  })

  // A provider the registry has never heard of must still land somewhere the
  // sidebar can show — a row that belongs to no facet is a row you cannot
  // find by filtering, which is worse than a slightly wrong bucket.
  it("puts an unknown provider in Other instead of dropping it", () => {
    expect(categoryOf(cred({ provider: "SOME_NEW_THING" }))).toBe("Other")
  })
})

describe("buildCategoryFacet", () => {
  it("counts per category and orders by the registry's display order", () => {
    const facet = buildCategoryFacet([
      cred({ id: "a", provider: "GITHUB" }),
      cred({ id: "b", provider: "GITLAB" }),
      cred({ id: "c", provider: "ANTHROPIC" }),
    ])
    expect(facet).toEqual([
      { value: "AI", label: "AI & inference", count: 1 },
      { value: "Source", label: "Source control", count: 2 },
    ])
  })

  it("returns nothing for an empty list rather than a row of zeroes", () => {
    expect(buildCategoryFacet([])).toEqual([])
  })
})

describe("buildScopeFacet", () => {
  it("separates workspace-wide from per-crew and names the crews it knows", () => {
    const facet = buildScopeFacet(
      [
        cred({ id: "a", scope: "WORKSPACE" }),
        cred({ id: "b", scope: "CREW", crew_ids: ["crew1"] }),
        cred({ id: "c", scope: "CREW", crew_ids: ["crew1", "crew2"] }),
      ],
      { crew1: "engineering" },
    )
    expect(facet).toEqual([
      { value: "WORKSPACE", label: "Workspace", count: 1 },
      { value: "crew:crew1", label: "Crew · engineering", count: 2 },
      // Unnamed crew still gets a row — hiding it would hide its credentials.
      { value: "crew:crew2", label: "Crew · crew2", count: 1 },
    ])
  })

  it("counts a CREW-scoped credential with no crew link once, under Crew-scoped", () => {
    const facet = buildScopeFacet([cred({ id: "a", scope: "CREW", crew_ids: [] })], {})
    expect(facet).toEqual([{ value: "CREW", label: "Crew-scoped", count: 1 }])
  })
})

describe("applyCredentialFilters", () => {
  const rows = [
    cred({ id: "gh", name: "GH_TOKEN", provider: "GITHUB", scope: "CREW", crew_ids: ["crew1"] }),
    cred({ id: "aws", name: "AWS_MAIN", provider: "AWS", scope: "WORKSPACE", status: "REVOKED" }),
    cred({ id: "an", name: "ANTHROPIC_API_KEY", provider: "ANTHROPIC", scope: "WORKSPACE", tags: ["prod"] }),
  ]

  it("returns everything under the empty filter", () => {
    expect(applyCredentialFilters(rows, EMPTY_CREDENTIAL_FILTERS, new Set()).map((c) => c.id)).toEqual([
      "gh",
      "aws",
      "an",
    ])
  })

  it("narrows to the rows that need attention", () => {
    const out = applyCredentialFilters(rows, { ...EMPTY_CREDENTIAL_FILTERS, status: "attention" }, new Set())
    expect(out.map((c) => c.id)).toEqual(["aws"])
  })

  // The missing-tool set comes from GET /crews/{id}/credential-readiness. It
  // is passed in rather than derived so the sidebar count and the list can
  // never disagree about which rows are affected.
  it("narrows to the rows whose CLI is not in any crew's container", () => {
    const out = applyCredentialFilters(
      rows,
      { ...EMPTY_CREDENTIAL_FILTERS, status: "missing-tool" },
      new Set(["aws"]),
    )
    expect(out.map((c) => c.id)).toEqual(["aws"])
  })

  it("filters by category", () => {
    const out = applyCredentialFilters(rows, { ...EMPTY_CREDENTIAL_FILTERS, category: "Source" }, new Set())
    expect(out.map((c) => c.id)).toEqual(["gh"])
  })

  it("filters by workspace scope and by one crew", () => {
    expect(
      applyCredentialFilters(rows, { ...EMPTY_CREDENTIAL_FILTERS, scope: "WORKSPACE" }, new Set()).map((c) => c.id),
    ).toEqual(["aws", "an"])
    expect(
      applyCredentialFilters(rows, { ...EMPTY_CREDENTIAL_FILTERS, scope: "crew:crew1" }, new Set()).map((c) => c.id),
    ).toEqual(["gh"])
  })

  it("filters by tag", () => {
    const out = applyCredentialFilters(rows, { ...EMPTY_CREDENTIAL_FILTERS, tag: "prod" }, new Set())
    expect(out.map((c) => c.id)).toEqual(["an"])
  })

  it("searches name, account label, description and tags, case-insensitively", () => {
    const withLabel = [
      ...rows,
      cred({ id: "acc", name: "GH_BOT", account_label: "acme-bot", description: "release automation" }),
    ]
    expect(
      applyCredentialFilters(withLabel, { ...EMPTY_CREDENTIAL_FILTERS, search: "ACME" }, new Set()).map((c) => c.id),
    ).toEqual(["acc"])
    expect(
      applyCredentialFilters(withLabel, { ...EMPTY_CREDENTIAL_FILTERS, search: "release" }, new Set()).map((c) => c.id),
    ).toEqual(["acc"])
    expect(
      applyCredentialFilters(withLabel, { ...EMPTY_CREDENTIAL_FILTERS, search: "prod" }, new Set()).map((c) => c.id),
    ).toEqual(["an"])
  })

  it("combines filters rather than letting the last one win", () => {
    const out = applyCredentialFilters(
      rows,
      { ...EMPTY_CREDENTIAL_FILTERS, scope: "WORKSPACE", category: "AI" },
      new Set(),
    )
    expect(out.map((c) => c.id)).toEqual(["an"])
  })
})
