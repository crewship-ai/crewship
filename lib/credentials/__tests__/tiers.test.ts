// The tier table is a mirror of internal/keeper/tier.go, and the console's
// reading of `security_level` decides what an operator believes about a secret.
// What these tests protect is the part that is easy to get backwards: the two
// kinds of unknown. A missing field is "we were not told"; a field holding a
// number the tier table does not define is "the server guards this as
// critical", because that is what keeper.SecurityLevel.Tier() does with it.
// Reading the second as L1 would put a "low" badge on a row the backend treats
// as production-admin.

import { describe, it, expect } from "vitest"
import {
  CREDENTIAL_TIERS,
  UNCLASSIFIED_TIER,
  buildTierFacet,
  guardedCount,
  tierBuckets,
  tierLabel,
  tierMeta,
  tierOf,
} from "../tiers"

describe("tierOf", () => {
  it("returns the tier for every level the table defines", () => {
    for (const t of CREDENTIAL_TIERS) {
      expect(tierOf({ security_level: t.level })).toBe(t.level)
    }
  })

  it("returns null when the server did not send a tier at all", () => {
    expect(tierOf({})).toBeNull()
    expect(tierOf({ security_level: null })).toBeNull()
    expect(tierOf({ security_level: undefined })).toBeNull()
  })

  // The fail-closed rule, and the reason this module exists. keeper's Tier()
  // resolves an out-of-range level to L4; a console that resolved it to L1
  // would be the cheapest way to make a corrupt row look harmless.
  it("resolves an out-of-range level to L4, never to L1", () => {
    for (const bad of [0, 5, 99, -1, 1.5]) {
      expect(tierOf({ security_level: bad })).toBe(4)
    }
  })
})

describe("tierMeta", () => {
  it("names each tier the way keeper.SecurityLevel.Label does", () => {
    expect(tierMeta(1).label).toBe("L1 · low")
    expect(tierMeta(2).label).toBe("L2 · medium")
    expect(tierMeta(3).label).toBe("L3 · high")
    expect(tierMeta(4).label).toBe("L4 · critical")
  })

  it("falls back to L4 for a level the table does not define", () => {
    expect(tierMeta(7).level).toBe(4)
  })

  it("gives every tier a colour and a consequence, so no surface has to invent one", () => {
    for (const t of CREDENTIAL_TIERS) {
      expect(t.color).toMatch(/^rgb\(/)
      expect(t.consequence.length).toBeGreaterThan(10)
      expect(t.short).toBe(`L${t.level}`)
    }
  })
})

describe("tierLabel", () => {
  it("says Unclassified rather than guessing when there is no tier", () => {
    expect(tierLabel(null)).toBe("Unclassified")
    expect(tierLabel(3)).toBe("L3 · high")
  })
})

describe("tierBuckets", () => {
  const CREDS = [
    { security_level: 1 },
    { security_level: 1 },
    { security_level: 3 },
    { security_level: 4 },
  ]

  it("returns every tier, including the empty ones", () => {
    const buckets = tierBuckets(CREDS)
    expect(buckets.map((b) => b.key)).toEqual(["1", "2", "3", "4"])
    expect(buckets.map((b) => b.count)).toEqual([2, 0, 1, 1])
  })

  // "L4 · 0" is a fact about the workspace — nothing here stops for a human —
  // not an empty control, which is why this section prints zeroes where the
  // other facets omit them.
  it("keeps a zero tier on the list rather than dropping it", () => {
    const buckets = tierBuckets([{ security_level: 1 }])
    expect(buckets.find((b) => b.key === "4")).toEqual(
      expect.objectContaining({ count: 0, label: "L4 · critical" }),
    )
  })

  it("adds the unclassified bucket only when something is in it", () => {
    expect(tierBuckets(CREDS).some((b) => b.key === UNCLASSIFIED_TIER)).toBe(false)
    const mixed = tierBuckets([...CREDS, {}])
    expect(mixed.find((b) => b.key === UNCLASSIFIED_TIER)).toEqual(
      expect.objectContaining({ count: 1, label: "Unclassified" }),
    )
  })

  it("sums to the vault, so the donut centre matches the header", () => {
    const total = tierBuckets([...CREDS, {}, { security_level: 42 }]).reduce(
      (n, b) => n + b.count,
      0,
    )
    expect(total).toBe(6)
  })

  it("counts an out-of-range level under L4, matching tierOf", () => {
    const buckets = tierBuckets([{ security_level: 9 }])
    expect(buckets.find((b) => b.key === "4")!.count).toBe(1)
  })
})

describe("buildTierFacet", () => {
  it("hands the rail the same numbers the donut draws", () => {
    const creds = [{ security_level: 2 }, { security_level: 2 }, { security_level: 4 }]
    expect(buildTierFacet(creds)).toEqual(
      tierBuckets(creds).map((b) => ({ value: b.key, label: b.label, count: b.count })),
    )
  })
})

describe("guardedCount", () => {
  // L3 and up are the tiers Keeper mediates per read (SelfServiceDelivery:
  // false in the tier table); L1 and L2 are handed to the agent for the run.
  it("counts L3 and L4, and nothing below", () => {
    expect(
      guardedCount([
        { security_level: 1 },
        { security_level: 2 },
        { security_level: 3 },
        { security_level: 4 },
      ]),
    ).toBe(2)
  })

  it("does not count a credential whose tier the server never sent", () => {
    expect(guardedCount([{}, { security_level: null }])).toBe(0)
  })

  it("counts an out-of-range level as guarded, since the server treats it as L4", () => {
    expect(guardedCount([{ security_level: 77 }])).toBe(1)
  })
})
