import type { InboxV2Entry } from "./inbox-v2-types"

export const ATTENTION_LABELS = {
  approvals: "Approvals waiting",
  "run-alerts": "Run alerts",
  "schedule-alerts": "Schedule alerts",
} as const

export type InboxAttention = keyof typeof ATTENTION_LABELS

const ATTENTION_KINDS: Record<InboxAttention, ReadonlySet<string>> = {
  approvals: new Set(["waitpoint", "escalation"]),
  "run-alerts": new Set(["failed_run", "schedule_circuit_breaker_tripped"]),
  "schedule-alerts": new Set(["schedule_missed"]),
}

export function parseInboxAttention(value: string | null): InboxAttention | null {
  return value && Object.hasOwn(ATTENTION_LABELS, value) ? value as InboxAttention : null
}

/** Dashboard attention counts are active inbox-item kinds, not approval-queue rows. */
export function matchesInboxAttention(entry: InboxV2Entry, attention: InboxAttention): boolean {
  if (entry.historical) return false
  const items = entry.source === "group" ? entry.groupedItems ?? [] : entry.inboxItem ? [entry.inboxItem] : []
  return items.some((item) => ATTENTION_KINDS[attention].has(item.kind))
}
