import { describe, expect, it } from "vitest"

import type { WorkItem, WorkState } from "@/hooks/use-work-items"
import type { WebhookDelivery } from "@/hooks/use-webhook-deliveries"
import {
  deliveryLine,
  deliveryTone,
  deliveryFamily,
  endpointHealth,
  endpointName,
  formatCost,
  eventFamily,
  failureCauses,
  humanReason,
  isDeletedAgent,
  ledgerAgents,
  ledgerCounts,
  ledgerFlow,
  ledgerHeadline,
  ledgerLanes,
  ledgerTone,
  narrowDeliveries,
  workFamily,
  narrowWork,
  needsYou,
  workLine,
  workSubject,
} from "../work-ledger"

const NOW = Date.parse("2026-10-08T15:30:00Z")
const casey = { id: "a-casey", name: "Casey", slug: "casey", avatar_seed: "c", avatar_style: "" }
const robot2 = { id: "a-robot2", name: "Lab Robot 2", slug: "lab-robot-2", avatar_seed: "r", avatar_style: "" }

let n = 0
function item(state: WorkState, over: Partial<WorkItem> = {}): WorkItem {
  n++
  return {
    id: `wk-${n}`,
    workspace_id: "ws",
    source: "webhook",
    source_ref: `dlv-${n}`,
    domain_kind: "",
    domain_id: "",
    agent_id: casey.id,
    crew_id: "",
    session_id: "",
    class: "background",
    authorized_by_user_id: "",
    target_revision: "",
    state,
    state_reason: "",
    generation: 1,
    attempt_count: 1,
    priority: 0,
    eligible_at: new Date(NOW - 60_000).toISOString(),
    deadline_at: null,
    replay_of: null,
    replay_reason: "",
    created_at: new Date(NOW - n * 60_000).toISOString(),
    updated_at: new Date(NOW - n * 60_000).toISOString(),
    terminal_at: null,
    agent: casey,
    crew: null,
    event_type: "invoice.disputed",
    duration_ms: null,
    cost_usd: null,
    ...over,
  } as WorkItem
}

describe("ledgerTone (#3012)", () => {
  it("speaks the Activity rail's words, never Waiting or Expired", () => {
    expect(ledgerTone("needs_reconciliation")).toBe("needs")
    expect(ledgerTone("queued")).toBe("line")
    expect(ledgerTone("retry_wait")).toBe("line")
    expect(ledgerTone("starting")).toBe("running")
    expect(ledgerTone("running")).toBe("running")
    expect(ledgerTone("succeeded")).toBe("done")
    expect(ledgerTone("failed")).toBe("failed")
    expect(ledgerTone("expired")).toBe("failed")
    expect(ledgerTone("cancelled")).toBe("cancelled")
  })
})

describe("the work queue's summary (#3012)", () => {
  const items = [
    item("succeeded", { duration_ms: 18_000 }),
    item("succeeded", { duration_ms: 20_000 }),
    item("succeeded", { duration_ms: 40_000 }),
    item("failed", { state_reason: "agent deleted while queued", agent: null, agent_id: "a-gone", event_type: "order.shipped" }),
    item("failed", { state_reason: "agent deleted while queued", agent: null, agent_id: "a-gone", event_type: "stock.low" }),
    item("failed", { state_reason: "sidecar did not start" }),
    item("cancelled", { attempt_count: 0 }),
    item("needs_reconciliation", { agent: robot2, agent_id: robot2.id, state_reason: "provider rejected the API key", event_type: "invoice.export" }),
    item("queued", { agent: robot2, agent_id: robot2.id }),
  ]

  it("counts every tone and says the day in one sentence", () => {
    const counts = ledgerCounts(items)
    expect(counts).toMatchObject({ needs: 1, line: 1, running: 0, done: 3, failed: 3, cancelled: 1, total: 9 })
    expect(ledgerHeadline(counts).map((p) => p.text)).toEqual([
      "9 pieces of work",
      "1 needs you",
      "1 in the queue",
      "3 done",
      "3 failed",
      "1 cancelled",
    ])
  })

  it("draws the flow with the median time and the line held behind a blocked item", () => {
    const flow = ledgerFlow(items)
    expect(flow.arrived).toBe(9)
    expect(flow.line).toEqual({ count: 1, blocked: true })
    expect(flow.medianDoneMs).toBe(20_000)
    expect(flow.causes).toBe(2)
  })

  it("puts what needs you first, with how many items wait behind it", () => {
    const needs = needsYou(items)
    expect(needs).toHaveLength(1)
    expect(needs[0].behind).toBe(1)
    expect(needs[0].item.agent?.name).toBe("Lab Robot 2")
  })

  it("groups failures by cause, naming agents and events", () => {
    const causes = failureCauses(items)
    expect(causes[0]).toMatchObject({ cause: "agent deleted while queued", count: 2, agents: ["Deleted agent"], events: ["order.shipped", "stock.low"] })
    expect(causes[1]).toMatchObject({ cause: "sidecar did not start", count: 1, agents: ["Casey"] })
  })

  it("draws one lane per agent, busiest first, with what went wrong on the right", () => {
    const lanes = ledgerLanes(items, { from: NOW - 24 * 3_600_000, to: NOW })
    expect(lanes.map((l) => [l.name, l.summary.text])).toEqual([
      ["Casey", "1 failed"],
      ["Lab Robot 2", "1 needs you"],
      ["Deleted agent", "2 failed"],
    ])
    expect(lanes[0].dots.every((d) => d.left >= 0 && d.left <= 100)).toBe(true)
  })

  it("lists agents with their state for the rail", () => {
    const agents = ledgerAgents(items)
    expect(agents.map((a) => [a.name, a.state, a.count])).toEqual([
      ["Casey", "idle", 5],
      ["Lab Robot 2", "blocked", 2],
      ["Deleted agent", "deleted", 2],
    ])
  })

  it("narrows by tone, agent and event family together", () => {
    expect(narrowWork(items, { tone: "failed" })).toHaveLength(3)
    expect(narrowWork(items, { tone: "failed", agentId: "a-gone" })).toHaveLength(2)
    expect(narrowWork(items, { family: "invoice.*" }).length).toBe(7)
    expect(narrowWork(items, { tone: "all" })).toHaveLength(9)
  })
})

describe("a line of work (#3012)", () => {
  it("names what came in and how it ended, never a raw id", () => {
    expect(workSubject(item("succeeded", { event_type: "health.check" }))).toBe("health.check")
    expect(workSubject(item("succeeded", { source: "chat", event_type: "" }))).toBe("Chat turn")
    expect(workLine(item("succeeded", { duration_ms: 18_000 }))).toBe("Done · 18.0s")
    expect(workLine(item("cancelled", { attempt_count: 0 }))).toBe("Cancelled before it started")
    expect(workLine(item("needs_reconciliation", { state_reason: "provider rejected the API key" }))).toBe(
      "Outcome unclear — provider rejected the API key",
    )
    expect(workLine(item("failed", { state_reason: "" }))).toBe("Failed")
  })

  it("groups events into families", () => {
    expect(eventFamily("invoice.export")).toBe("invoice.*")
    expect(eventFamily("ping")).toBe("ping")
    expect(eventFamily("")).toBe("")
  })
})

function delivery(over: Partial<WebhookDelivery> = {}): WebhookDelivery {
  n++
  return {
    id: `dlv-${n}`,
    workspace_id: "ws",
    endpoint_id: casey.id,
    endpoint_kind: "agent",
    profile: "crewship-hmac",
    source_delivery_id: `s-${n}`,
    event_type: "invoice.overdue",
    event_action: "",
    signing_key_id: "",
    body_sha256: "",
    body_bytes: 96,
    filter_decision: "accepted",
    filter_reason: "",
    target_revision: "",
    work_id: `wk-${n}`,
    received_at: new Date(NOW - n * 60_000).toISOString(),
    dedup_expires_at: "",
    raw_body_available: true,
    raw_body_expires_at: null,
    agent: casey,
    work_state: "succeeded",
    ...over,
  }
}

describe("webhook deliveries (#3012)", () => {
  const deliveries = [
    delivery(),
    delivery({ event_type: "ping", filter_decision: "ignored", filter_reason: "ping is not an event", work_id: null, work_state: null }),
    delivery({ endpoint_id: robot2.id, agent: robot2, event_type: "invoice.export", work_state: "needs_reconciliation" }),
    delivery({ endpoint_id: "a-gone", agent: null, event_type: "order.shipped", work_state: "failed" }),
  ]

  it("says what each delivery became", () => {
    expect(deliveryTone(deliveries[1])).toBe("ignored")
    expect(deliveryTone(deliveries[0])).toBe("accepted")
    expect(deliveryLine(deliveries[1])).toBe("Connection test — nothing to do")
    expect(deliveryLine(deliveries[2])).toBe("→ work · needs you")
    expect(deliveryLine(deliveries[0])).toBe("→ work · done")
  })

  it("judges each endpoint's health in a sentence", () => {
    const health = endpointHealth(deliveries, NOW)
    expect(health.map((h) => [h.name, h.verdict])).toEqual([
      ["Casey", "ok"],
      ["Lab Robot 2", "blocked"],
      ["Deleted agent", "gone"],
    ])
    expect(health[0].count).toBe(2)
  })

  it("does not call an endpoint that has gone quiet for a day arriving", () => {
    const old = new Date(NOW - 3 * 24 * 3_600_000).toISOString()
    const [h] = endpointHealth([delivery({ received_at: old })], NOW)
    expect(h.verdict).toBe("quiet")
    // A blocked queue matters more than a quiet sender.
    const [b] = endpointHealth([delivery({ received_at: old, work_state: "needs_reconciliation" })], NOW)
    expect(b.verdict).toBe("blocked")
  })

  it("narrows by decision, endpoint and family", () => {
    expect(narrowDeliveries(deliveries, { decision: "ignored" })).toHaveLength(1)
    expect(narrowDeliveries(deliveries, { endpointId: robot2.id })).toHaveLength(1)
    expect(narrowDeliveries(deliveries, { family: "invoice.*" })).toHaveLength(2)
  })
})

describe("what a reader sees of a reason (#3017)", () => {
  it("drops raw ids and the operator boilerplate", () => {
    expect(humanReason("refused at dispatch: agent cmuzox3gm000b2ee652f3 was deleted while this work waited")).toBe(
      "refused at dispatch: the agent was deleted while this work waited",
    )
    expect(
      humanReason("resolved by cmuxw94hh0001de39519d: runtime confirmed stopped by operator; Lab agent had an invalid model"),
    ).toBe("Lab agent had an invalid model — settled by hand")
    expect(humanReason("")).toBe("")
  })

  it("treats an agent removed from the workspace as deleted", () => {
    const gone = { ...robot2, deleted: true }
    expect(isDeletedAgent(gone)).toBe(true)
    expect(isDeletedAgent(null)).toBe(true)
    expect(isDeletedAgent(casey)).toBe(false)
    const agents = ledgerAgents([item("failed", { agent: gone, agent_id: gone.id })])
    expect(agents[0]).toMatchObject({ name: "Lab Robot 2", state: "deleted" })
  })

  it("says a ping is a connection test", () => {
    expect(deliveryLine(delivery({ event_type: "ping", filter_decision: "ignored", filter_reason: "ping", work_id: null, work_state: null }))).toBe(
      "Connection test — nothing to do",
    )
  })
})

describe("formatCost", () => {
  it("does not round a real cost away to zero", () => {
    expect(formatCost(0)).toBe("—")
    expect(formatCost(0.004)).toBe("<$0.01")
    expect(formatCost(1.25)).toBe("$1.25")
  })
})

describe("what the second review found (#3017)", () => {
  it("reads a multi-line settled reason without the operator boilerplate", () => {
    expect(humanReason("resolved by cmuxw94hh0001de39519d: runtime confirmed stopped by operator; checked the ERP\nand the bank")).toBe(
      "checked the ERP\nand the bank — settled by hand",
    )
  })

  it("drops ids but not ordinary long words", () => {
    expect(humanReason("crossreferencingcalendars failed")).toBe("crossreferencingcalendars failed")
    expect(humanReason("run cmuzox3gm000b2ee652f3 failed")).toBe("run … failed")
  })

  it("counts the line behind an agent once, on the item that holds the slot", () => {
    const items = [
      item("needs_reconciliation", { agent: robot2, agent_id: robot2.id }),
      item("needs_reconciliation", { agent: robot2, agent_id: robot2.id }),
      item("queued", { agent: robot2, agent_id: robot2.id }),
    ]
    const behind = needsYou(items).map((e) => e.behind)
    expect(behind.reduce((a, b) => a + b, 0)).toBe(1)
  })

  it("sums up a lane of only queued or only cancelled work as such", () => {
    const win = { from: NOW - 86_400_000, to: NOW }
    expect(ledgerLanes([item("queued")], win)[0].summary.text).toBe("1 in the queue")
    expect(ledgerLanes([item("cancelled")], win)[0].summary.text).toBe("1 cancelled")
  })

  it("puts a dot with an unreadable time at the window's start, not at NaN", () => {
    const [lane] = ledgerLanes([item("succeeded", { created_at: "not a time" })], { from: NOW - 1000, to: NOW })
    expect(lane.dots[0].left).toBe(0)
  })

  it("names an endpoint that is not an agent's without calling it deleted", () => {
    const routine = delivery({ endpoint_id: "rt-1", endpoint_kind: "routine", agent: null, work_state: null, work_id: null })
    const [h] = endpointHealth([routine], NOW)
    expect(h.name).toBe("Routine endpoint")
    expect(h.verdict).toBe("ok")
    expect(h.kind).toBe("routine")
    expect(endpointName(routine)).toBe("Routine endpoint")
    expect(endpointName(delivery({ agent: null }))).toBe("Deleted agent")
  })

  it("gives work and deliveries without an event a family to narrow to", () => {
    expect(workFamily(item("succeeded", { event_type: "", source: "chat" }))).toBe("Chat turn")
    const bare = delivery({ event_type: "" })
    expect(deliveryFamily(bare)).toBe("(no event)")
    expect(narrowDeliveries([bare], { family: "(no event)" })).toHaveLength(1)
    expect(narrowWork([item("succeeded", { event_type: "", source: "chat" })], { family: "Chat turn" })).toHaveLength(1)
  })
})
