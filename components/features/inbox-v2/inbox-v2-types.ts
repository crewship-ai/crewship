import type { InboxItem } from "@/hooks/use-inbox"
import type { ApprovalRow } from "@/lib/types/approvals"
import type { Mission, MissionTask } from "@/lib/types/mission"

export type InboxV2View = "action" | "updates" | "history"
export type InboxV2Source = "inbox" | "approval" | "mission" | "group"

export interface InboxV2Entry {
  key: string
  source: InboxV2Source
  title: string
  summary: string
  subject: string
  category: string
  priority: "urgent" | "high" | "medium" | "low"
  createdAt: string
  deadlineAt?: string | null
  unread: boolean
  actionable: boolean
  historical: boolean
  outcome?: string | null
  inboxItem?: InboxItem
  approval?: ApprovalRow
  mission?: Mission
  task?: MissionTask
  groupedItems?: InboxItem[]
}

export interface InboxV2Confirmation {
  entry: InboxV2Entry
  action: string
  at: string
}
