import { describe, expect, it } from "vitest"

import type { InboxItem } from "@/hooks/use-inbox"
import type { ApprovalRow } from "@/lib/types/approvals"

import {
  approvalEntry,
  EMPTY_INBOX_V2_FILTERS,
  entryAttention,
  facetCounts,
  filterEntries,
  inboxEntry,
  sortInboxEntries,
} from "../inbox-v2-derive"
import { filterInboxEntries } from "../inbox-entry-identity"
import type { InboxLookup } from "../inbox-v2-types"

// Harbor 2 Inbox (#3385): the panel sorts by urgency by default, offers Newest
// and Crew, and filters by the four attention classes the bell also uses.

function item(o: Partial<InboxItem> = {}): InboxItem {
  return {
    id: "i", workspace_id: "ws", kind: "message", source_id: "s", title: "An update",
    state: "unread", priority: "medium", blocking: false,
    created_at: "2026-08-30T10:00:00Z", updated_at: "2026-08-30T10:00:00Z", ...o,
  }
}

const NOW = Date.parse("2026-08-30T12:00:00Z")

describe("sorting the inbox list", () => {
  const undated = inboxEntry(item({ id: "undated", kind: "waitpoint", created_at: "2026-08-30T11:59:00Z" }))
  const late = inboxEntry(item({ id: "late", kind: "waitpoint", created_at: "2026-08-29T10:00:00Z", payload: { timeout_at: "2026-08-30T18:00:00Z" } }))
  const soon = inboxEntry(item({ id: "soon", kind: "waitpoint", created_at: "2026-08-28T10:00:00Z", payload: { timeout_at: "2026-08-30T12:30:00Z" } }))
  const update = inboxEntry(item({ id: "update", created_at: "2026-08-30T11:00:00Z" }))

  it("urgency sort puts nearest deadline first, undated last", () => {
    expect(sortInboxEntries([undated, update, late, soon], "urgency").map((e) => e.key)).toEqual([
      "inbox:soon", "inbox:late", "inbox:undated", "inbox:update",
    ])
  })

  it("newest sort ignores deadlines", () => {
    expect(sortInboxEntries([soon, late, undated, update], "newest").map((e) => e.key)).toEqual([
      "inbox:undated", "inbox:update", "inbox:late", "inbox:soon",
    ])
  })

  it("crew sort groups by the crew name shown, urgency inside a crew", () => {
    const crewOf: Record<string, string> = { "inbox:soon": "Research", "inbox:late": "Ops", "inbox:undated": "Ops", "inbox:update": "" }
    expect(sortInboxEntries([update, soon, undated, late], "crew", (e) => crewOf[e.key]).map((e) => e.key)).toEqual([
      "inbox:late", "inbox:undated", "inbox:soon", "inbox:update",
    ])
  })

  it("filterInboxEntries applies the chosen sort", () => {
    const lookup: InboxLookup = { crewById: new Map(), agentBySlug: new Map(), agentById: new Map(), ready: true }
    expect(filterInboxEntries([soon, undated], EMPTY_INBOX_V2_FILTERS, lookup, "newest").map((e) => e.key)).toEqual(["inbox:undated", "inbox:soon"])
    expect(filterInboxEntries([undated, soon], EMPTY_INBOX_V2_FILTERS, lookup).map((e) => e.key)).toEqual(["inbox:soon", "inbox:undated"])
  })
})

describe("attention classes", () => {
  it("prefers the class the server wrote", () => {
    expect(entryAttention(inboxEntry(item({ kind: "message", attention_class: "review" })))).toBe("review")
  })

  it("falls back to the kind for rows written before the contract", () => {
    expect(entryAttention(inboxEntry(item({ kind: "waitpoint" })))).toBe("decision")
    expect(entryAttention(inboxEntry(item({ kind: "run_needs_human" })))).toBe("input")
    expect(entryAttention(inboxEntry(item({ kind: "escalation", payload: { escalation_type: "TEXT" } })))).toBe("input")
    expect(entryAttention(inboxEntry(item({ kind: "escalation", payload: { kind: "skill_proposal" } })))).toBe("review")
    expect(entryAttention(inboxEntry(item({ kind: "memory_consolidation" })))).toBe("review")
    expect(entryAttention(inboxEntry(item({ kind: "failed_run" })))).toBe("repair")
    expect(entryAttention(inboxEntry(item({ kind: "schedule_missed" })))).toBe("repair")
    expect(entryAttention(inboxEntry(item({ kind: "message" })))).toBeNull()
    expect(entryAttention(approvalEntry({ id: "a", status: "pending", kind: "destructive_op", reason: "", created_at: "2026-08-30T10:00:00Z" } as ApprovalRow))).toBe("decision")
  })

  it("never classes a resolved row as something to do", () => {
    expect(entryAttention(inboxEntry(item({ kind: "waitpoint", state: "resolved", attention_class: "decision" })))).toBeNull()
  })

  it("filters and counts by attention", () => {
    const rows = [
      inboxEntry(item({ id: "a", kind: "waitpoint" })),
      inboxEntry(item({ id: "b", kind: "failed_run" })),
      inboxEntry(item({ id: "c", kind: "failed_run" })),
    ]
    expect(filterEntries(rows, { ...EMPTY_INBOX_V2_FILTERS, attention: "repair" }, NOW).map((e) => e.key)).toEqual(["inbox:b", "inbox:c"])
    const counts = facetCounts(rows, NOW)
    expect(counts.attention).toEqual({ decision: 1, input: 0, review: 0, repair: 2 })
  })
})
