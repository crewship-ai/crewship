import { describe, expect, it } from "vitest"

import type { InboxItem } from "@/hooks/use-inbox"
import { matchesInboxAttention, parseInboxAttention } from "../inbox-v2-attention"
import type { InboxV2Entry } from "../inbox-v2-types"
import { approvalEntry } from "../inbox-v2-derive"

const entry = (kind: string, historical = false, itemProps: Partial<InboxItem> = {}): InboxV2Entry => ({
  key: kind,
  source: "inbox",
  title: kind,
  summary: "",
  subject: "",
  category: "",
  priority: "low",
  createdAt: "2026-09-25T12:00:00Z",
  unread: true,
  actionable: false,
  historical,
  inboxItem: { id: kind, kind, state: historical ? "resolved" : "unread", ...itemProps } as InboxItem,
})

describe("dashboard attention links in Inbox", () => {
  it("accepts only known URL categories", () => {
    expect(parseInboxAttention("approvals")).toBe("approvals")
    expect(parseInboxAttention("run-alerts")).toBe("run-alerts")
    expect(parseInboxAttention("schedule-alerts")).toBe("schedule-alerts")
    expect(parseInboxAttention("unknown")).toBeNull()
  })

  it("shows real decisions and alerts, excluding source-less advisories and resolved rows", () => {
    const rows = ["waitpoint", "failed_run", "schedule_circuit_breaker_tripped", "schedule_missed", "message"]
      .map((kind) => entry(kind))
    rows.push(entry("run_needs_human", false, { blocking: true }))
    rows.push(entry("escalation", false, { payload: { kind: "routine_proposal" } }))
    rows.push(entry("escalation"))
    const matching = (attention: "approvals" | "run-alerts" | "schedule-alerts") =>
      rows.filter((row) => matchesInboxAttention(row, attention)).map((row) => row.key)
    expect(matching("approvals")).toEqual(["waitpoint", "run_needs_human", "escalation"])
    expect(matching("run-alerts")).toEqual(["failed_run", "schedule_circuit_breaker_tripped"])
    expect(matching("schedule-alerts")).toEqual(["schedule_missed"])
    expect(matchesInboxAttention(entry("waitpoint", true), "approvals")).toBe(false)
  })

  // /approvals now redirects here (#3406): the pending rows of the approvals
  // queue are exactly the "Decisions waiting" that page listed. Decided ones
  // are History.
  it("counts a pending approval-queue request as a decision waiting, a decided one not", () => {
    const row = { id: "ap1", kind: "tool_call", status: "pending", reason: "", created_at: "2026-09-25T12:00:00Z", payload: { tool: "github.merge_pr" } }
    expect(matchesInboxAttention(approvalEntry(row), "approvals")).toBe(true)
    expect(matchesInboxAttention(approvalEntry({ ...row, status: "approved" }), "approvals")).toBe(false)
    expect(matchesInboxAttention(approvalEntry(row), "run-alerts")).toBe(false)
  })
})
