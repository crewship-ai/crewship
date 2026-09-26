import type { InboxV2Entry } from "./inbox-v2-types"
import { isBlockingInboxItem } from "./inbox-v2-derive"

export const ATTENTION_LABELS = {
  approvals: "Decisions waiting",
  "run-alerts": "Run alerts",
  "schedule-alerts": "Schedule alerts",
} as const

export type InboxAttention = keyof typeof ATTENTION_LABELS

const ATTENTION_KINDS: Record<InboxAttention, ReadonlySet<string>> = {
  approvals: new Set(),
  "run-alerts": new Set(["failed_run", "schedule_circuit_breaker_tripped"]),
  "schedule-alerts": new Set(["schedule_missed"]),
}

export function parseInboxAttention(value: string | null): InboxAttention | null {
  return value && Object.hasOwn(ATTENTION_LABELS, value) ? value as InboxAttention : null
}

/** Keep the dashboard count and focused Inbox tied to actual human decisions. */
export function matchesInboxAttention(entry: InboxV2Entry, attention: InboxAttention): boolean {
  if (entry.historical) return false
  const items = entry.source === "group" ? entry.groupedItems ?? [] : entry.inboxItem ? [entry.inboxItem] : []
  return items.some((item) => attention === "approvals" ? isBlockingInboxItem(item) : ATTENTION_KINDS[attention].has(item.kind))
}
