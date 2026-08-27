// The /credentials sidebar is a filter surface, so every number on it is a
// claim about the list beside it. These tests pin the two ways that goes
// wrong: a count that disagrees with what the filter actually selects, and a
// facet that silently swallows rows (an unknown category, a crew the user
// cannot name).

import { describe, it, expect } from "vitest"
import {
  EMPTY_CREDENTIAL_FILTERS,
  applyCredentialFilters,
  buildAgentFacet,
  buildBrandFacet,
  buildScopeFacet,
  buildShapeFacet,
  buildTagFacet,
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

// The category facet is gone, and with it `categoryOf`.
//
// It was the one control on the page nobody could set. The create wizard asks
// which SHAPE a credential is and which BRAND it belongs to; the word
// "category" appears nowhere in it, and the value was inferred from the
// provider through the registry. So the rail filtered on a property the user
// never chose, while the two properties they do choose could not be filtered on
// at all. These two facets are the honest replacement.
describe("buildBrandFacet", () => {
  it("counts per brand, commonest first, and names it from the registry", () => {
    const facet = buildBrandFacet([
      cred({ id: "a", provider: "GITLAB" }),
      cred({ id: "b", provider: "GITHUB" }),
      cred({ id: "c", provider: "GITHUB" }),
    ])
    expect(facet).toEqual([
      { value: "GITHUB", label: "GitHub", count: 2, providers: ["GITHUB"] },
      { value: "GITLAB", label: "GitLab", count: 1, providers: ["GITLAB"] },
    ])
  })

  // A provider the registry has never heard of must still get a row: a
  // credential you cannot reach by filtering is a credential you cannot find.
  // And two unknown providers must not collapse into two rows both reading
  // "Generic secret" — getBrand answers the generic for anything it does not
  // hold, so the label has to fall back to the key.
  it("keeps providers the registry cannot name, and keeps them apart", () => {
    const facet = buildBrandFacet([
      cred({ id: "a", provider: "SOME_NEW_THING" }),
      cred({ id: "b", provider: "ANOTHER_NEW_THING" }),
    ])
    expect(facet).toHaveLength(2)
    const labels = facet.map((f) => f.label).sort()
    expect(labels).toEqual(["ANOTHER_NEW_THING", "SOME_NEW_THING"])
  })

  it("returns nothing for an empty list rather than a row of zeroes", () => {
    expect(buildBrandFacet([])).toEqual([])
  })
})

describe("buildShapeFacet", () => {
  it("counts per shape, using the short lowercase name the rest of the UI uses", () => {
    const facet = buildShapeFacet([
      cred({ id: "a", type: "CERTIFICATE" }),
      cred({ id: "b", type: "API_KEY" }),
      cred({ id: "c", type: "API_KEY" }),
    ])
    expect(facet).toEqual([
      { value: "API_KEY", label: "api key", count: 2 },
      { value: "CERTIFICATE", label: "cert", count: 1 },
    ])
  })

  // A row with no label and an empty value is a control that appears to filter
  // and does not — applyCredentialFilters reads "" as no filter at all.
  it("skips a credential with no shape rather than inventing a blank row", () => {
    const facet = buildShapeFacet([
      cred({ id: "a", type: "API_KEY" }),
      cred({ id: "b", type: undefined }),
    ])
    expect(facet).toHaveLength(1)
    expect(facet[0].value).toBe("API_KEY")
  })

  // SECRET and GENERIC_SECRET are both "secret". Two rows with the same name
  // and different counts read as a rendering bug, not a storage distinction.
  it("collapses shapes that share a label into one row", () => {
    const facet = buildShapeFacet([
      cred({ id: "a", type: "SECRET" }),
      cred({ id: "b", type: "GENERIC_SECRET" }),
    ])
    expect(facet).toHaveLength(1)
    expect(facet[0].count).toBe(2)
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

  it("filters by brand", () => {
    const out = applyCredentialFilters(rows, { ...EMPTY_CREDENTIAL_FILTERS, brand: ["GITHUB"] }, new Set())
    expect(out.map((c) => c.id)).toEqual(["gh"])
  })

  // Values inside one facet OR together. Until #1776 a facet held a single
  // value, so "GitHub or Anthropic" was not a question the vault could be
  // asked — picking the second brand silently dropped the first.
  it("ORs the values inside one facet", () => {
    const out = applyCredentialFilters(
      rows,
      { ...EMPTY_CREDENTIAL_FILTERS, brand: ["GITHUB", "ANTHROPIC"] },
      new Set(),
    )
    expect(out.map((c) => c.id)).toEqual(["gh", "an"])
  })

  it("ORs several scopes, including a crew alongside the workspace", () => {
    expect(
      applyCredentialFilters(
        rows,
        { ...EMPTY_CREDENTIAL_FILTERS, scope: ["WORKSPACE", "crew:crew1"] },
        new Set(),
      ).map((c) => c.id),
    ).toEqual(["gh", "aws", "an"])
  })

  it("filters by shape", () => {
    const out = applyCredentialFilters(
      [cred({ id: "cert", type: "CERTIFICATE" }), cred({ id: "key", type: "API_KEY" })],
      { ...EMPTY_CREDENTIAL_FILTERS, shape: ["CERTIFICATE"] },
      new Set(),
    )
    expect(out.map((c) => c.id)).toEqual(["cert"])
  })

  it("filters by workspace scope and by one crew", () => {
    expect(
      applyCredentialFilters(rows, { ...EMPTY_CREDENTIAL_FILTERS, scope: ["WORKSPACE"] }, new Set()).map((c) => c.id),
    ).toEqual(["aws", "an"])
    expect(
      applyCredentialFilters(rows, { ...EMPTY_CREDENTIAL_FILTERS, scope: ["crew:crew1"] }, new Set()).map((c) => c.id),
    ).toEqual(["gh"])
  })

  it("filters by tag", () => {
    const out = applyCredentialFilters(rows, { ...EMPTY_CREDENTIAL_FILTERS, tag: ["prod"] }, new Set())
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
      { ...EMPTY_CREDENTIAL_FILTERS, scope: ["WORKSPACE"], brand: ["ANTHROPIC"] },
      new Set(),
    )
    expect(out.map((c) => c.id)).toEqual(["an"])
  })

  // The Keeper tier as a facet. The counts in the rail come from
  // buildTierFacet, so what this pins is the other half of the promise: the
  // rows a tier row SELECTS are the rows it counted.
  describe("the tier facet", () => {
    const tiered = [
      cred({ id: "low", security_level: 1 }),
      cred({ id: "high", security_level: 3 }),
      cred({ id: "crit", security_level: 4 }),
      cred({ id: "legacy" }),
    ]

    it("keeps only the selected tier", () => {
      expect(
        applyCredentialFilters(tiered, { ...EMPTY_CREDENTIAL_FILTERS, tier: "3" }, new Set()).map(
          (c) => c.id,
        ),
      ).toEqual(["high"])
    })

    it("selects the rows the server never tiered under 'unclassified'", () => {
      expect(
        applyCredentialFilters(
          tiered,
          { ...EMPTY_CREDENTIAL_FILTERS, tier: "unclassified" },
          new Set(),
        ).map((c) => c.id),
      ).toEqual(["legacy"])
    })

    // Fail-closed, matching keeper.SecurityLevel.Tier(): a level the table does
    // not define is guarded as L4, so the L4 row must select it. Filing it
    // under L1 would hide a corrupt row behind the least alarming facet.
    it("files an out-of-range level under L4", () => {
      const out = applyCredentialFilters(
        [cred({ id: "weird", security_level: 99 })],
        { ...EMPTY_CREDENTIAL_FILTERS, tier: "4" },
        new Set(),
      )
      expect(out.map((c) => c.id)).toEqual(["weird"])
    })

    // The "Guarded · L3+" tile counts L3 and L4 and has to be able to select
    // both. Pointing it at tier "3" made the tile report five and filter to
    // four, dropping the L4 that is the whole reason anyone reads the number.
    it("selects every mediated tier under the guarded value, not just L3", () => {
      const out = applyCredentialFilters(
        [
          cred({ id: "l2", security_level: 2 }),
          cred({ id: "l3", security_level: 3 }),
          cred({ id: "l4", security_level: 4 }),
          cred({ id: "none" }),
        ],
        { ...EMPTY_CREDENTIAL_FILTERS, tier: "guarded" },
        new Set(),
      )
      expect(out.map((c) => c.id)).toEqual(["l3", "l4"])
    })

    it("does nothing when no tier is selected", () => {
      expect(
        applyCredentialFilters(tiered, EMPTY_CREDENTIAL_FILTERS, new Set()),
      ).toHaveLength(4)
    })

    it("narrows alongside the other facets rather than replacing them", () => {
      const out = applyCredentialFilters(
        [
          cred({ id: "a", security_level: 4, tags: ["prod"] }),
          cred({ id: "b", security_level: 4, tags: [] }),
        ],
        { ...EMPTY_CREDENTIAL_FILTERS, tier: "4", tag: ["prod"] },
        new Set(),
      )
      expect(out.map((c) => c.id)).toEqual(["a"])
    })
  })
})

// The tag facet used to be built inline as `tags.map(t => ({value: t, count: 0}))`
// with the counts hidden — the only facet that could not tell you how much it
// would narrow the list, sorted alphabetically over a Set.
describe("buildTagFacet", () => {
  it("counts each tag and puts the commonest first", () => {
    const facet = buildTagFacet([
      cred({ id: "a", tags: ["prod", "infra"] }),
      cred({ id: "b", tags: ["prod"] }),
      cred({ id: "c", tags: ["prod", "infra"] }),
      cred({ id: "d", tags: ["demo"] }),
    ])
    expect(facet).toEqual([
      { value: "prod", label: "prod", count: 3 },
      { value: "infra", label: "infra", count: 2 },
      { value: "demo", label: "demo", count: 1 },
    ])
  })

  it("returns nothing when the workspace tags nothing", () => {
    expect(buildTagFacet([cred({ id: "a", tags: [] })])).toEqual([])
  })
})

// The agent facet is keyed by ID rather than name, which is the whole point:
// an avatar and a filter are both keyed by id, and deriving one from a name
// would render a different face for the same agent than every other page.
describe("buildAgentFacet", () => {
  it("lists each agent once, by id, with how many credentials it holds", () => {
    const facet = buildAgentFacet([
      cred({ id: "a", agent_ids: ["ag1", "ag2"], agent_names: ["Alice", "Bob"] }),
      cred({ id: "b", agent_ids: ["ag1"], agent_names: ["Alice"] }),
    ])
    expect(facet).toEqual([
      { value: "ag1", label: "Alice", count: 2 },
      { value: "ag2", label: "Bob", count: 1 },
    ])
  })

  it("pairs each id with its OWN name, not with the first one it saw", () => {
    const facet = buildAgentFacet([
      cred({ id: "a", agent_ids: ["ag2", "ag1"], agent_names: ["Bravo", "Alpha"] }),
    ])
    expect(facet.find((o) => o.value === "ag1")?.label).toBe("Alpha")
    expect(facet.find((o) => o.value === "ag2")?.label).toBe("Bravo")
  })

  // A row that cannot be filtered by is a control that does nothing, so an
  // agent the server named but did not identify is skipped rather than given
  // a synthesised key.
  it("skips an agent with no id rather than inventing one", () => {
    expect(
      buildAgentFacet([cred({ id: "a", agent_ids: [], agent_names: ["Nameless"] })]),
    ).toEqual([])
  })

  it("falls back to a short id when the name is missing", () => {
    const facet = buildAgentFacet([cred({ id: "a", agent_ids: ["abcdefghijkl"], agent_names: [] })])
    expect(facet[0].label).toBe("abcdefgh")
  })
})

describe("the agent filter", () => {
  const rows = [
    cred({ id: "held", agent_ids: ["ag1"], agent_names: ["Deploy bot"] }),
    cred({ id: "free", agent_ids: [], agent_names: [] }),
  ]

  it("keeps only the credentials that agent holds", () => {
    expect(
      applyCredentialFilters(rows, { ...EMPTY_CREDENTIAL_FILTERS, agentId: ["ag1"] }, new Set()).map(
        (c) => c.id,
      ),
    ).toEqual(["held"])
  })

  it("matches nothing for an agent that holds nothing", () => {
    expect(
      applyCredentialFilters(rows, { ...EMPTY_CREDENTIAL_FILTERS, agentId: ["ghost"] }, new Set()),
    ).toEqual([])
  })

  // "Which secrets does the deploy bot hold" gets typed as often as it gets
  // clicked, and the rail has one search box.
  it("finds a credential by the name of the agent holding it", () => {
    expect(
      applyCredentialFilters(rows, { ...EMPTY_CREDENTIAL_FILTERS, search: "deploy bot" }, new Set()).map(
        (c) => c.id,
      ),
    ).toEqual(["held"])
  })
})
