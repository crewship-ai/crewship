// The credentials overview replaced a table with numbers, and a number nobody
// can check is worse than a row nobody reads. These tests are the check: what
// counts as needing attention and in what order, what "expiring" excludes, and
// that the type breakdown sums to the vault rather than to whatever survived a
// label collision.

import { describe, it, expect } from "vitest"
import {
  attentionQueue,
  expiringSoon,
  recentlyUsed,
  typeBreakdown,
  vaultTotals,
  type OverviewCredential,
} from "../overview"

const DAY = 24 * 3600 * 1000
// Half a day past the boundary. `daysUntilExpiry` floors, so an exact `+2 * DAY`
// resolves to 1 whenever a millisecond elapses between building the string and
// reading it — a test that fails once every few runs and looks like a real bug.
const inDays = (n: number) => new Date(Date.now() + (n + 0.5) * DAY).toISOString()
const daysAgo = (n: number) => new Date(Date.now() - n * DAY).toISOString()

function cred(over: Partial<OverviewCredential> & { id: string }): OverviewCredential {
  return {
    name: over.id,
    provider: "NONE",
    status: "ACTIVE",
    scope: "WORKSPACE",
    type: "API_KEY",
    ...over,
  }
}

const NONE: ReadonlySet<string> = new Set()

describe("typeBreakdown", () => {
  it("counts by type, commonest first", () => {
    const rows = typeBreakdown([
      cred({ id: "a", type: "API_KEY" }),
      cred({ id: "b", type: "API_KEY" }),
      cred({ id: "c", type: "SSH_KEY" }),
    ])
    expect(rows.map((r) => [r.label, r.count])).toEqual([
      ["api key", 2],
      ["ssh key", 1],
    ])
  })

  // SECRET and GENERIC_SECRET are both "secret". Two rows with the same name
  // and different counts reads as a rendering bug, not as a storage detail.
  it("collapses server types that share a label into one row", () => {
    const rows = typeBreakdown([
      cred({ id: "a", type: "SECRET" }),
      cred({ id: "b", type: "GENERIC_SECRET" }),
    ])
    expect(rows).toHaveLength(1)
    expect(rows[0]).toEqual(expect.objectContaining({ label: "secret", count: 2 }))
  })

  it("shares the vault, not the largest row — so the bars are comparable", () => {
    const rows = typeBreakdown([
      cred({ id: "a", type: "API_KEY" }),
      cred({ id: "b", type: "API_KEY" }),
      cred({ id: "c", type: "API_KEY" }),
      cred({ id: "d", type: "SSH_KEY" }),
    ])
    expect(rows[0].share).toBeCloseTo(0.75)
    expect(rows[1].share).toBeCloseTo(0.25)
    expect(rows.reduce((n, r) => n + r.count, 0)).toBe(4)
  })

  it("returns nothing for an empty vault rather than dividing by zero", () => {
    expect(typeBreakdown([])).toEqual([])
  })

  it("shows a type the console has not heard of rather than dropping the row", () => {
    const rows = typeBreakdown([cred({ id: "a", type: "QUANTUM_KEY" })])
    expect(rows[0].label).toBe("quantum key")
  })
})

describe("attentionQueue", () => {
  it("ranks errors above pending, expiring, stale and a missing tool", () => {
    const creds = [
      cred({ id: "stale", last_used_at: daysAgo(200) }),
      cred({ id: "tool" }),
      cred({ id: "expiring", token_expires_at: inDays(3) }),
      cred({ id: "pending", status: "PENDING_APPROVAL" }),
      cred({ id: "revoked", status: "REVOKED" }),
    ]
    const queue = attentionQueue(creds, new Set(["tool"]), 10)
    expect(queue.map((i) => i.id)).toEqual(["revoked", "pending", "expiring", "stale", "tool"])
  })

  it("says why, in words the operator can act on", () => {
    const queue = attentionQueue(
      [
        cred({ id: "revoked", status: "REVOKED" }),
        cred({ id: "limited", status: "RATE_LIMITED" }),
        cred({ id: "soon", token_expires_at: inDays(4) }),
      ],
      NONE,
      10,
    )
    const reasons = Object.fromEntries(queue.map((i) => [i.id, i.reason]))
    expect(reasons.revoked).toBe("revoked")
    expect(reasons.limited).toBe("rate limited")
    expect(reasons.soon).toBe("expires in 4d")
  })

  // "EXPIRED" and "expires in -12d" are the same fact told twice; a past expiry
  // wins over whatever the last check happened to store.
  it("calls an already-expired credential expired, whatever its stored status", () => {
    const queue = attentionQueue([cred({ id: "old", token_expires_at: daysAgo(12) })], NONE, 10)
    expect(queue[0].reason).toBe("expired")
    expect(queue[0].tone).toBe("error")
  })

  it("includes a credential whose only problem is a missing tool", () => {
    const queue = attentionQueue([cred({ id: "ok" })], new Set(["ok"]), 10)
    expect(queue).toHaveLength(1)
    expect(queue[0].reason).toMatch(/CLI/)
  })

  it("leaves a healthy credential out entirely", () => {
    expect(attentionQueue([cred({ id: "fine", last_used_at: daysAgo(1) })], NONE, 10)).toEqual([])
  })

  it("honours the limit, so the caller can ask for the whole queue or a page of it", () => {
    const creds = Array.from({ length: 9 }, (_, i) => cred({ id: `x${i}`, status: "REVOKED" }))
    expect(attentionQueue(creds, NONE, 4)).toHaveLength(4)
    expect(attentionQueue(creds, NONE, Number.MAX_SAFE_INTEGER)).toHaveLength(9)
  })
})

describe("expiringSoon", () => {
  it("returns credentials inside the window, soonest first", () => {
    const rows = expiringSoon(
      [
        cred({ id: "b", token_expires_at: inDays(20) }),
        cred({ id: "a", token_expires_at: inDays(2) }),
      ],
      10,
    )
    expect(rows.map((r) => r.credential.id)).toEqual(["a", "b"])
    expect(rows[0].days).toBe(2)
  })

  // Already-expired is not "about to break", it is broken — the attention queue
  // owns it, with a reason that says so.
  it("excludes what already expired", () => {
    expect(expiringSoon([cred({ id: "gone", token_expires_at: daysAgo(1) })], 10)).toEqual([])
  })

  it("excludes credentials with no expiry, and ones past the warning window", () => {
    expect(
      expiringSoon(
        [cred({ id: "none" }), cred({ id: "far", token_expires_at: inDays(120) })],
        10,
      ),
    ).toEqual([])
  })
})

describe("recentlyUsed", () => {
  it("returns the most recently used first", () => {
    const rows = recentlyUsed(
      [
        cred({ id: "old", last_used_at: daysAgo(5) }),
        cred({ id: "new", last_used_at: daysAgo(1) }),
      ],
      10,
    )
    expect(rows.map((r) => r.id)).toEqual(["new", "old"])
  })

  it("omits credentials nothing has ever read, and ones with a junk timestamp", () => {
    expect(
      recentlyUsed([cred({ id: "never" }), cred({ id: "junk", last_used_at: "not a date" })], 10),
    ).toEqual([])
  })
})

describe("vaultTotals", () => {
  it("counts active, expiring and linked in one pass", () => {
    const totals = vaultTotals([
      cred({ id: "a", last_used_at: daysAgo(1), _count_agent_credentials: 2 }),
      cred({ id: "b", token_expires_at: inDays(5) }),
      cred({ id: "c", status: "REVOKED" }),
    ])
    expect(totals).toEqual({ total: 3, active: 2, expiring: 1, linked: 1 })
  })

  it("reports zeroes for an empty vault rather than throwing", () => {
    expect(vaultTotals([])).toEqual({ total: 0, active: 0, expiring: 0, linked: 0 })
  })
})
