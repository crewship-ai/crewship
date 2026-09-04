import { describe, expect, it } from "vitest"

import type { InboxItem } from "@/hooks/use-inbox"
import type { ApprovalRow } from "@/lib/types/approvals"

import {
  approvalEntry,
  entryAgentRef,
  entryCrewId,
  entryKindPill,
  entryTitle,
  entryVerb,
  inboxEntry,
} from "../inbox-v2-derive"

// The row as the audit (docs/ux/audit-conversations.md P1-1) redraws it: a
// kind pill, the question without the server's prefix, the crew and the
// agent, and a verb. Every value here is a pure derivation over the row.

function item(overrides: Partial<InboxItem> = {}): InboxItem {
  return {
    id: "ibx-1",
    workspace_id: "ws-1",
    kind: "escalation",
    source_id: "esc-1",
    title: "Agent escalation: Need the Grafana API key to finish the dashboard",
    state: "unread",
    priority: "high",
    blocking: true,
    sender_type: "agent",
    sender_name: "riley",
    created_at: "2026-09-03T13:11:00Z",
    updated_at: "2026-09-03T13:11:00Z",
    payload: {
      crew_id: "crew-ops",
      chat_id: "chat-1",
      escalation_type: "TEXT",
      reason: "Need the Grafana API key to finish the dashboard",
    },
    ...overrides,
  }
}

function approval(overrides: Partial<ApprovalRow> = {}): ApprovalRow {
  return {
    id: "ap-1",
    workspace_id: "ws-1",
    kind: "ephemeral_hire",
    status: "pending",
    reason: "Need an SRE for the arm64 migration",
    created_at: "2026-09-03T13:13:00Z",
    crew_id: "crew-eng",
    agent_id: "ag-1",
    ...overrides,
  } as ApprovalRow
}

describe("entryTitle", () => {
  it("drops the server's 'Agent escalation:' prefix and uses the whole reason", () => {
    expect(entryTitle(inboxEntry(item()))).toBe("Need the Grafana API key to finish the dashboard")
  })

  it("keeps a title the server truncated when the payload has no reason", () => {
    const row = inboxEntry(item({ title: "Agent escalation: Should I delete…", payload: { escalation_type: "TEXT" } }))
    expect(entryTitle(row)).toBe("Should I delete…")
  })

  it("leaves other titles alone", () => {
    expect(entryTitle(inboxEntry(item({ kind: "waitpoint", title: "Hire ephemeral agent: DevOps / SRE (60m)" }))))
      .toBe("Hire ephemeral agent: DevOps / SRE (60m)")
    expect(entryTitle(inboxEntry(item({ title: "Credential approval: GRAFANA_API_KEY" }))))
      .toBe("Credential approval: GRAFANA_API_KEY")
  })
})

describe("entryKindPill", () => {
  it("names an escalation by what it asks for, not by its table", () => {
    expect(entryKindPill(inboxEntry(item()))).toEqual({ label: "Question", tone: "warn" })
    expect(entryKindPill(inboxEntry(item({ payload: { escalation_type: "LINK" } })))).toEqual({ label: "Link", tone: "blue" })
    expect(entryKindPill(inboxEntry(item({ payload: { escalation_type: "CREDENTIAL" } })))).toEqual({ label: "Credential", tone: "warn" })
    expect(entryKindPill(inboxEntry(item({ payload: { kind: "routine_proposal" } })))).toEqual({ label: "Routine proposal", tone: "purple" })
    expect(entryKindPill(inboxEntry(item({ payload: { request_type: "access", request_id: "kr-1" } })))).toEqual({ label: "Access request", tone: "warn" })
  })

  it("tells a hire from a routine approval", () => {
    expect(entryKindPill(inboxEntry(item({ kind: "waitpoint", payload: { kind: "hire" } })))).toEqual({ label: "Hire", tone: "blue" })
    expect(entryKindPill(inboxEntry(item({ kind: "waitpoint", payload: {} })))).toEqual({ label: "Approval", tone: "blue" })
    expect(entryKindPill(approvalEntry(approval()))).toEqual({ label: "Hire", tone: "blue" })
    expect(entryKindPill(approvalEntry(approval({ kind: "destructive_op" })))).toEqual({ label: "Approval", tone: "blue" })
  })

  it("uses plain words for the schedule kinds", () => {
    expect(entryKindPill(inboxEntry(item({ kind: "schedule_missed" }))).label).toBe("Missed run")
    expect(entryKindPill(inboxEntry(item({ kind: "schedule_circuit_breaker_tripped" }))).label).toBe("Paused schedule")
    expect(entryKindPill(inboxEntry(item({ kind: "failed_run" })))).toEqual({ label: "Failed run", tone: "danger" })
  })

  it("never renders a raw enum for something it does not know", () => {
    const pill = entryKindPill(inboxEntry(item({ kind: "message", payload: {} })))
    expect(pill.label).toBe("Update")
    expect(pill.label).not.toMatch(/[A-Z_]{4,}/)
  })
})

describe("entryVerb", () => {
  it("says what pressing the row lets you do", () => {
    expect(entryVerb(inboxEntry(item()))).toBe("Answer")
    expect(entryVerb(inboxEntry(item({ payload: { escalation_type: "CREDENTIAL" } })))).toBe("Grant")
    expect(entryVerb(inboxEntry(item({ payload: { escalation_type: "LINK" } })))).toBe("Review")
    expect(entryVerb(inboxEntry(item({ kind: "waitpoint", payload: { kind: "hire" } })))).toBe("Approve")
    expect(entryVerb(inboxEntry(item({ kind: "schedule_missed", payload: { schedule_id: "s1" } })))).toBe("Run now")
    expect(entryVerb(inboxEntry(item({ kind: "schedule_circuit_breaker_tripped", payload: { schedule_id: "s1" } })))).toBe("Re-enable")
    expect(entryVerb(approvalEntry(approval()))).toBe("Approve")
  })

  it("offers a decided row its record, not a decision", () => {
    expect(entryVerb(inboxEntry(item({ state: "resolved", resolved_action: "approve" })))).toBe("Record")
    expect(entryVerb(approvalEntry(approval({ status: "denied" })))).toBe("Record")
  })
})

describe("entryCrewId / entryAgentRef", () => {
  it("reads the crew off whichever source carries it", () => {
    expect(entryCrewId(inboxEntry(item()))).toBe("crew-ops")
    expect(entryCrewId(inboxEntry(item({ payload: { invoking_crew_id: "crew-inv", crew_id: "crew-ops" } })))).toBe("crew-inv")
    expect(entryCrewId(approvalEntry(approval()))).toBe("crew-eng")
    expect(entryCrewId(inboxEntry(item({ payload: {} })))).toBeNull()
  })

  it("keys the agent by the slug the roster uses", () => {
    expect(entryAgentRef(inboxEntry(item()))).toEqual({ slug: "riley", id: null, label: "riley" })
    expect(entryAgentRef(inboxEntry(item({ sender_type: "system", sender_name: "Keeper" }))).slug).toBeNull()
    expect(entryAgentRef(inboxEntry(item({ payload: { agent_slug: "morgan", agent_name: "Morgan" } }))).slug).toBe("morgan")
  })
})

describe("outcomeStatus", () => {
  it("maps both sources' spellings onto the shared status words", async () => {
    const { outcomeStatus } = await import("../inbox-v2-derive")
    expect(outcomeStatus("approve")).toBe("APPROVED")
    expect(outcomeStatus("approved")).toBe("APPROVED")
    expect(outcomeStatus("reject")).toBe("REJECTED")
    expect(outcomeStatus("denied")).toBe("DENIED")
    expect(outcomeStatus("timeout")).toBe("EXPIRED")
    expect(outcomeStatus("timed_out")).toBe("EXPIRED")
    expect(outcomeStatus(null)).toBeNull()
  })
})

describe("the crew facet", () => {
  it("narrows by the row's crew id, whichever source carries it", async () => {
    const { filterEntries, EMPTY_INBOX_V2_FILTERS } = await import("../inbox-v2-derive")
    const rows = [
      inboxEntry(item({ id: "a", payload: { crew_id: "crew-ops", escalation_type: "TEXT" } })),
      inboxEntry(item({ id: "b", payload: { crew_id: "crew-eng", escalation_type: "TEXT" } })),
      approvalEntry(approval({ id: "ap", crew_id: "crew-ops" })),
    ]
    expect(filterEntries(rows, { ...EMPTY_INBOX_V2_FILTERS, crew: "crew-ops" }).map((e) => e.key)).toEqual(["inbox:a", "approval:ap"])
    expect(filterEntries(rows, EMPTY_INBOX_V2_FILTERS)).toHaveLength(3)
  })
})

describe("entryAgentRef for a queue row", () => {
  it("keys the agent by id and never prints a cuid as a name", () => {
    const ref = entryAgentRef(approvalEntry(approval({ agent_id: "clx0agent0000000000000", requested_by: "clx0user00000000000000" })))
    expect(ref).toEqual({ slug: null, id: "clx0agent0000000000000", label: "Agent" })
  })
})
