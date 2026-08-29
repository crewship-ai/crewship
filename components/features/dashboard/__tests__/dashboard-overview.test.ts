import { describe, expect, it } from "vitest"

import {
  buildAttentionItems,
  deriveFleetHealth,
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
    const items = buildAttentionItems({
      inbox,
      capacity: {
        enabled: true,
        held: [{ crew_id: "crew-1", reason: "host_memory", since: "2026-01-01", waited_ms: 5000 }],
      },
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
