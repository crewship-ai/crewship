import { describe, expect, it } from "vitest"

import type { InboxItem } from "@/hooks/use-inbox"
import { matchesInboxAttention, parseInboxAttention } from "../inbox-v2-attention"
import type { InboxV2Entry } from "../inbox-v2-types"

const entry = (kind: string, historical = false): InboxV2Entry => ({
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
  inboxItem: { id: kind, kind } as InboxItem,
})

describe("dashboard attention links in Inbox", () => {
  it("accepts only known URL categories", () => {
    expect(parseInboxAttention("approvals")).toBe("approvals")
    expect(parseInboxAttention("run-alerts")).toBe("run-alerts")
    expect(parseInboxAttention("schedule-alerts")).toBe("schedule-alerts")
    expect(parseInboxAttention("unknown")).toBeNull()
  })

  it("shows both counted kinds in each category and excludes unrelated or resolved rows", () => {
    const rows = ["waitpoint", "escalation", "failed_run", "schedule_circuit_breaker_tripped", "schedule_missed", "message"]
      .map((kind) => entry(kind))
    const matching = (attention: "approvals" | "run-alerts" | "schedule-alerts") =>
      rows.filter((row) => matchesInboxAttention(row, attention)).map((row) => row.key)
    expect(matching("approvals")).toEqual(["waitpoint", "escalation"])
    expect(matching("run-alerts")).toEqual(["failed_run", "schedule_circuit_breaker_tripped"])
    expect(matching("schedule-alerts")).toEqual(["schedule_missed"])
    expect(matchesInboxAttention(entry("waitpoint", true), "approvals")).toBe(false)
  })
})
