import { describe, expect, it } from "vitest"

import {
  attentionState,
  buildAttentionItems,
  capacitySignal,
  deriveFleetHealth,
  heldForWorkspace,
  kpisFromInsights,
} from "@/components/features/dashboard/dashboard-overview"
import type { AgentSummary, CrewSummary, RunInsightsResponse } from "@/app/(dashboard)/dashboard-types"
import type { InboxItem } from "@/hooks/use-inbox"

const crew: CrewSummary = { id: "crew-1", name: "Docs", slug: "docs", color: "blue", icon: null }
const agent = (status: string): AgentSummary => ({
  id: `agent-${status}`,
  name: status,
  slug: status.toLowerCase(),
  role_title: null,
  agent_role: "SPECIALIST",
  status,
  crew: { name: "Docs", slug: "docs", color: "blue" },
  crew_id: "crew-1",
  _count: { skills: 0, credentials: 0, chats: 0 },
})

describe("dashboard overview derivations", () => {
  it("uses the canonical inbox kinds and capacity holds for the attention strip", () => {
    const inbox = [
      { id: "w1", kind: "waitpoint", state: "unread" },
      { id: "f1", kind: "failed_run", state: "read" },
    ] as InboxItem[]
    // Takes holds already scoped to this workspace and deduped per crew —
    // see heldForWorkspace, which is what the page now feeds it.
    const items = buildAttentionItems({
      inbox,
      heldCrews: [{ crew_id: "crew-1", reason: "host_memory", since: "2026-01-01", waited_ms: 5000 }],
      credentialGapCount: 2,
    })

    expect(items.map((item) => item.id)).toEqual(["approvals", "failures", "capacity", "credentials"])
    expect(items[0].label).toBe("1 approval waiting")
  })

  it("never calls an empty crew 100% healthy and gives concrete failures precedence", () => {
    const empty = deriveFleetHealth({ crews: [crew], agents: [], gapsByCrew: new Map(), servicesByCrew: new Map() })
    expect(empty[0]).toMatchObject({ status: "Empty", tone: "muted", agents: 0 })

    const errored = deriveFleetHealth({
      crews: [crew],
      agents: [agent("RUNNING"), agent("ERROR")],
      gapsByCrew: new Map([["crew-1", 3]]),
      servicesByCrew: new Map([["crew-1", { total: 2, running: 1, degraded: 1, checked: true }]]),
    })
    expect(errored[0]).toMatchObject({ status: "Agent error", tone: "danger", agents: 2, runningAgents: 1 })
  })

  it("computes success only over terminal verdicts and carries the denominator", () => {
    const insights: RunInsightsResponse = {
      window: "24h",
      totals: { total: 12, succeeded: 9, failed: 1, running: 2 },
      duration: { p50_ms: 1_000, p95_ms: 9_000 },
      by_trigger: [], by_model: [], by_crew: [], top_agents: [], truncated: false,
    }
    expect(kpisFromInsights(insights, 2.5, 10, 100)).toEqual({
      completed: 9,
      successPct: 90,
      successOk: 9,
      successTotal: 10,
      cost: 2.5,
      budgetSpent: 10,
      budgetTotal: 100,
      p95Ms: 9_000,
    })
  })
})

// The dashboard's job is to be believed. A green reassurance rendered over a
// failed fetch is worse than a blank panel: the operator stops looking.
describe("the strip distinguishes 'nothing is wrong' from 'we could not find out'", () => {
  it("does not claim all-clear while the inbox is still loading", () => {
    const s = attentionState({ items: [], inboxLoading: true, inboxError: null })
    expect(s.kind).toBe("unknown")
  })

  it("does not claim all-clear when the inbox fetch failed", () => {
    // useInbox throws on non-ok with retry:false, so a 403 on an
    // RBAC-gated workspace lands here permanently, not for a beat.
    const s = attentionState({ items: [], inboxLoading: false, inboxError: "403" })
    expect(s.kind).toBe("unknown")
  })

  it("says all-clear only when the inbox actually answered and was empty", () => {
    const s = attentionState({ items: [], inboxLoading: false, inboxError: null })
    expect(s.kind).toBe("clear")
  })

  it("shows the items whenever there are any, even mid-load", () => {
    const item = { id: "approvals", label: "2 approvals waiting", detail: "", href: "/inbox", tone: "warn" as const, icon: (() => null) as never }
    expect(attentionState({ items: [item], inboxLoading: true, inboxError: null }).kind).toBe("items")
    expect(attentionState({ items: [item], inboxLoading: false, inboxError: "boom" }).kind).toBe("items")
  })
})

describe("capacity holds are this workspace's, counted per crew", () => {
  const crews = [
    { id: "crew-1", name: "Docs", slug: "docs", color: "blue", icon: null },
    { id: "crew-2", name: "Ops", slug: "ops", color: "amber", icon: null },
  ] as CrewSummary[]

  it("ignores holds belonging to another workspace's crews", () => {
    // /api/v1/runtime/capacity is deliberately instance-scoped — the host is
    // a property of the instance. On a multi-workspace instance that means
    // the raw list carries other tenants' crews, and this surface is
    // workspace-scoped, so rendering their detail string would leak it.
    const held = [
      { crew_id: "crew-1", crew_slug: "docs", reason: "host_memory", detail: "ours", since: "", waited_ms: 0 },
      { crew_id: "other-ws-crew", crew_slug: "theirs", reason: "host_memory", detail: "SOMEONE ELSE", since: "", waited_ms: 0 },
    ]
    const mine = heldForWorkspace(held, crews)
    expect(mine.map((h) => h.crew_id)).toEqual(["crew-1"])
    expect(JSON.stringify(mine)).not.toContain("SOMEONE ELSE")
  })

  it("counts crews, not queued starts", () => {
    // admission.Hold is appended per held START, so five queued starts on one
    // crew must not read as five crews.
    const held = Array.from({ length: 5 }, () => ({
      crew_id: "crew-1", crew_slug: "docs", reason: "concurrency", detail: "", since: "", waited_ms: 0,
    }))
    expect(heldForWorkspace(held, crews)).toHaveLength(1)
  })

  it("is empty when the capacity fetch failed, rather than guessing", () => {
    expect(heldForWorkspace(null, crews)).toHaveLength(0)
  })
})

describe("runtime capacity does not report healthy when it could not be read", () => {
  it("is unknown, not Available, when the fetch failed", () => {
    const r = capacitySignal(null, [])
    // "Unavailable" is the right answer and contains the substring, so match
    // the whole value rather than a fragment of it.
    expect(r.value).not.toBe("Available")
    expect(r.value).toBe("Unavailable")
    expect(r.tone).not.toMatch(/success/)
  })

  it("says Disabled when admission control is genuinely off", () => {
    expect(capacitySignal({ enabled: false, held: [] } as never, []).value).toMatch(/disabled/i)
  })

  it("says Available only when it answered and nothing is held", () => {
    expect(capacitySignal({ enabled: true, held: [] } as never, []).value).toMatch(/available/i)
  })
})
