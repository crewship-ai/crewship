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

/**
 * What the page knows about crews and agents, so a row can say "Ops · Riley"
 * with a colour dot and a face instead of a cuid and a slug. Built once by
 * the page from /crews and /agents; every row reads, none fetch.
 */
export interface InboxCrewRef {
  id: string
  name: string
  slug: string
  color: string | null
}

export interface InboxAgentRef {
  id: string
  name: string
  slug: string
  avatar_seed?: string | null
  avatar_style?: string | null
  avatar_url?: string | null
  role_title?: string | null
  crew?: { name: string; slug: string; color?: string | null } | null
}

export interface InboxLookup {
  crewById: ReadonlyMap<string, InboxCrewRef>
  agentBySlug: ReadonlyMap<string, InboxAgentRef>
  /** The approvals queue names agents by id, inbox rows by slug. */
  agentById: ReadonlyMap<string, InboxAgentRef>
  /** True once both lists answered — before that, an unresolved crew is
   *  "still loading", not "no crew". */
  ready: boolean
}

export const EMPTY_INBOX_LOOKUP: InboxLookup = { crewById: new Map(), agentBySlug: new Map(), agentById: new Map(), ready: false }

/** The agent behind a row, by whichever key the source used. */
export function resolveAgent(lookup: InboxLookup, ref: { slug: string | null; id?: string | null }): InboxAgentRef | null {
  if (ref.slug) {
    const bySlug = lookup.agentBySlug.get(ref.slug)
    if (bySlug) return bySlug
  }
  if (ref.id) return lookup.agentById.get(ref.id) ?? null
  return null
}
