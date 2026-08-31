import { describe, expect, it } from "vitest"

import type { InboxItem } from "@/hooks/use-inbox"
import type { ApprovalRow } from "@/lib/types/approvals"
import type { Mission } from "@/lib/types/mission"

import {
  approvalEntry,
  groupAdvisories,
  inboxEntry,
  isActionableInboxItem,
  missionEntries,
  selectEntry,
  suppressedApprovalIDs,
  deadlineBucket,
  entryType,
  facetCounts,
  filterAndSortEntries as filterAndSort,
  EMPTY_INBOX_V2_FILTERS,
} from "../inbox-v2-derive"

function item(overrides: Partial<InboxItem> = {}): InboxItem {
  return {
    id: "msg-1",
    workspace_id: "ws-1",
    kind: "message",
    source_id: "source-1",
    title: "An update",
    state: "unread",
    priority: "medium",
    blocking: false,
    created_at: "2026-08-30T10:00:00Z",
    updated_at: "2026-08-30T10:00:00Z",
    ...overrides,
  }
}

describe("inbox v2 classification", () => {
  it("does not turn a source-less keeper advisory into a client decision", () => {
    expect(isActionableInboxItem(item({
      kind: "escalation",
      sender_name: "Skill Curator",
      blocking: true,
    }))).toBe(false)
  })

  it("keeps every real decision source in Needs action", () => {
    expect(isActionableInboxItem(item({ kind: "waitpoint" }))).toBe(true)
    expect(isActionableInboxItem(item({ kind: "escalation", payload: { kind: "skill_proposal" } }))).toBe(true)
    expect(isActionableInboxItem(item({
      kind: "escalation",
      payload: { request_type: "access", request_id: "kr-1" },
    }))).toBe(true)
    expect(isActionableInboxItem(item({ kind: "schedule_missed", payload: { schedule_id: "sch-1" } }))).toBe(true)
  })

  it("never reopens an already resolved decision", () => {
    expect(isActionableInboxItem(item({ kind: "waitpoint", state: "resolved" }))).toBe(false)
  })
})

describe("inbox v2 aggregation", () => {
  it("groups repeated infrastructure advisories without discarding their source rows", () => {
    const entries = [1, 2, 3].map((n) => inboxEntry(item({
      id: `skill-${n}`,
      kind: "escalation",
      sender_name: "Skill Curator",
      title: `Skill check: agent ${n}`,
    })))

    const grouped = groupAdvisories(entries)
    expect(grouped).toHaveLength(1)
    expect(grouped[0]).toMatchObject({
      source: "group",
      title: "Skill checks could not run",
      category: "system.health",
    })
    expect(grouped[0].groupedItems?.map((row) => row.id)).toEqual(["skill-1", "skill-2", "skill-3"])
  })

  it("projects pending and decided approval queue rows into action and history", () => {
    const base: ApprovalRow = {
      id: "ap-1",
      kind: "destructive_op",
      reason: "Remove generated deployment",
      status: "pending",
      created_at: "2026-08-30T09:00:00Z",
      timeout_at: "2026-08-30T10:30:00Z",
      payload: { tool: "delete_deployment" },
    }
    expect(approvalEntry(base)).toMatchObject({ actionable: true, historical: false, priority: "urgent" })
    expect(approvalEntry({ ...base, status: "approved", decided_at: "2026-08-30T10:00:00Z" })).toMatchObject({
      actionable: false,
      historical: true,
      outcome: "approved",
    })
  })

  it("surfaces mission reviews as actions and failures as updates", () => {
    const mission = {
      id: "mission-1",
      title: "Release",
      lead_agent_name: "Riley",
      updated_at: "2026-08-30T10:00:00Z",
      tasks: [
        { id: "review", title: "Review proof", status: "AWAITING_APPROVAL", needs_review: true, updated_at: "2026-08-30T10:00:00Z" },
        { id: "failed", title: "Publish", status: "FAILED", needs_review: false, error_message: "Registry unavailable", updated_at: "2026-08-30T09:00:00Z" },
      ],
    } as Mission

    expect(missionEntries([mission])).toMatchObject([
      { key: "mission:mission-1:review", actionable: true, category: "mission.review" },
      { key: "mission:mission-1:failed", actionable: false, category: "mission.failed" },
    ])
  })

  it("filters across title, subject and category while putting the nearest deadline first", () => {
    const later = inboxEntry(item({
      id: "later",
      kind: "waitpoint",
      sender_name: "Riley",
      payload: { timeout_at: "2026-08-30T12:00:00Z" },
    }))
    const sooner = inboxEntry(item({
      id: "sooner",
      kind: "waitpoint",
      sender_name: "Riley",
      payload: { timeout_at: "2026-08-30T11:00:00Z" },
    }))

    expect(filterAndSort([later, sooner], { ...EMPTY_INBOX_V2_FILTERS, search: "riley" }).map((entry) => entry.key)).toEqual([
      "inbox:sooner",
      "inbox:later",
    ])
  })
})

describe("cross-source dedupe", () => {
  // These two fixtures are the shapes dev2 actually returned for one gated
  // hire: the approval names the inbox row, the inbox row does not name the
  // approval. Reading one direction only put the same decision in Needs
  // action twice, under two titles that do not look related.
  const hireWaitpoint = item({
    id: "cmtgbbu4c009dbc7c7442",
    kind: "waitpoint",
    blocking: true,
    title: "Hire ephemeral agent: Software Development (30m)",
    payload: { agent_id: "ag-1", agent_name: "Software Development", crew_id: "crew-1", kind: "hire" },
  })
  const hireApproval: ApprovalRow = {
    id: "ap_e727b28c39f945e1",
    kind: "ephemeral_hire",
    reason: "hire ephemeral agent Software Development",
    status: "pending",
    created_at: "2026-08-30T21:19:44Z",
    payload: { tool: "agent.hire", inbox_item_id: "cmtgbbu4c009dbc7c7442" },
  }

  it("suppresses the approval when only the approval carries the link (ephemeral hire)", () => {
    expect([...suppressedApprovalIDs([hireWaitpoint], [hireApproval])]).toEqual(["ap_e727b28c39f945e1"])
  })

  it("still suppresses when only the inbox row carries the link (autonomy gate)", () => {
    const gateRow = item({ id: "ibx-gate", kind: "waitpoint", payload: { approval_id: "ap_gate" } })
    const gateApproval: ApprovalRow = {
      id: "ap_gate", kind: "autonomy_gate", reason: "create routine", status: "pending",
      created_at: "2026-08-30T09:00:00Z",
    }
    expect([...suppressedApprovalIDs([gateRow], [gateApproval])]).toEqual(["ap_gate"])
  })

  it("keeps an approval whose linked inbox row is not in this feed", () => {
    const orphan: ApprovalRow = {
      ...hireApproval, id: "ap_orphan", payload: { inbox_item_id: "some-other-workspace-row" },
    }
    expect([...suppressedApprovalIDs([hireWaitpoint], [orphan])]).toEqual([])
  })
})

describe("deep-link selection", () => {
  const entries = [
    inboxEntry(item({ id: "ibx-1", kind: "waitpoint", title: "Approve production deploy" })),
    approvalEntry({
      id: "ap-1", kind: "destructive_op", reason: "drop table", status: "pending",
      created_at: "2026-08-30T09:00:00Z",
    }),
  ]

  it("resolves ?item= against an inbox row", () => {
    expect(selectEntry(entries, "request:ibx-1")?.key).toBe("inbox:ibx-1")
  })

  it("resolves ?item= against an approval row, which the old inbox: prefix could never match", () => {
    expect(selectEntry(entries, "request:ap-1")?.key).toBe("approval:ap-1")
  })

  it("returns nothing for an unknown id instead of arming a different decision", () => {
    expect(selectEntry(entries, "request:does-not-exist")).toBeNull()
  })
})

describe("facets answer to real fields", () => {
  const NOW = Date.parse("2026-08-30T12:00:00Z")
  const at = (iso: string) => item({ id: iso, kind: "waitpoint", payload: { timeout_at: iso } })

  it("types a row by what it is, not by which endpoint answered", () => {
    expect(entryType(inboxEntry(item({ kind: "escalation" })))).toBe("escalation")
    expect(entryType(inboxEntry(item({ kind: "schedule_missed" })))).toBe("schedule_missed")
    expect(entryType(approvalEntry({
      id: "ap-1", kind: "destructive_op", reason: "r", status: "pending", created_at: "2026-08-30T09:00:00Z",
    }))).toBe("approval")
  })

  it("a grouped advisory answers as the kind of the rows inside it", () => {
    const grouped = groupAdvisories([1, 2].map((n) => inboxEntry(item({
      id: `sk-${n}`, kind: "escalation", sender_name: "Skill Curator", title: `Skill review: ${n}`,
    }))))
    expect(grouped).toHaveLength(1)
    expect(entryType(grouped[0])).toBe("escalation")
  })

  it("buckets a deadline without folding a distant one into today", () => {
    expect(deadlineBucket(inboxEntry(at("2026-08-30T12:30:00Z")), NOW)).toBe("hour")
    expect(deadlineBucket(inboxEntry(at("2026-08-30T22:00:00Z")), NOW)).toBe("today")
    expect(deadlineBucket(inboxEntry(at("2026-09-04T10:00:00Z")), NOW)).toBe("later")
    expect(deadlineBucket(inboxEntry(item({ kind: "waitpoint" })), NOW)).toBe("none")
  })

  it("counts every facet over the whole feed", () => {
    const counts = facetCounts([
      inboxEntry(at("2026-08-30T12:30:00Z")),
      inboxEntry(item({ id: "esc", kind: "escalation", state: "read" })),
      approvalEntry({ id: "ap-1", kind: "ephemeral_hire", reason: "r", status: "pending", created_at: "2026-08-30T09:00:00Z" }),
    ], NOW)
    expect(counts.type.waitpoint).toBe(1)
    expect(counts.type.escalation).toBe(1)
    expect(counts.type.approval).toBe(1)
    expect(counts.type.failed_run).toBe(0)
    expect(counts.deadline.hour).toBe(1)
    expect(counts.deadline.none).toBe(2)
    expect(counts.unread).toBe(2)
    expect(counts.total).toBe(3)
  })

  it("narrows by type, deadline and unread together", () => {
    const rows = [
      inboxEntry(at("2026-08-30T12:30:00Z")),
      inboxEntry(item({ id: "read-soon", kind: "waitpoint", state: "read", payload: { timeout_at: "2026-08-30T12:40:00Z" } })),
      inboxEntry(item({ id: "esc", kind: "escalation" })),
    ]
    const keys = (f: Partial<typeof EMPTY_INBOX_V2_FILTERS>) =>
      filterAndSort(rows, { ...EMPTY_INBOX_V2_FILTERS, ...f }, NOW).map((e) => e.key)

    expect(keys({ type: "waitpoint" })).toEqual(["inbox:2026-08-30T12:30:00Z", "inbox:read-soon"])
    expect(keys({ deadline: "hour" })).toEqual(["inbox:2026-08-30T12:30:00Z", "inbox:read-soon"])
    expect(keys({ type: "waitpoint", unreadOnly: true })).toEqual(["inbox:2026-08-30T12:30:00Z"])
    expect(keys({ type: "message" })).toEqual([])
  })
})
