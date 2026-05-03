"use client"

import { Inbox } from "lucide-react"
import Link from "next/link"

export interface InboxBannerProps {
  /** Agent ID — used to deep-link the journal view to this agent. */
  agentId: string
  /** Total pending items count. When 0, the banner does not render. */
  count: number
  /** Optional human summary line (e.g. "1 escalation from Lucie · 1 peer assignment from Tomas"). */
  summary?: string
}

/**
 * Yellow strip surfaced above the agent canvas when an agent has pending
 * inbox items (escalations, peer assignments, approval requests). Links
 * to /journal?agent_id=<id> — that view supports per-agent filtering
 * and renders escalation/approval entries from the journal stream.
 *
 * (Approvals/escalations also live at /orchestration but the latter
 * doesn't yet accept a per-agent query param, so we deep-link via
 * /journal which does.)
 *
 * Renders nothing when count is 0.
 */
export function InboxBanner({ agentId, count, summary }: InboxBannerProps) {
  if (count <= 0) return null

  return (
    <section className="rounded-xl border border-amber-500/30 bg-amber-500/5 px-4 py-3 flex items-center gap-3">
      <Inbox className="h-4 w-4 text-amber-300 shrink-0" />
      <div className="flex-1 min-w-0">
        <div className="text-sm text-amber-200">
          {count} item{count === 1 ? "" : "s"} waiting in inbox
        </div>
        {summary && <div className="text-xs text-muted-foreground truncate">{summary}</div>}
      </div>
      <Link
        href={`/journal?agent_id=${encodeURIComponent(agentId)}&entry_type=escalation,approval.requested,peer.message`}
        className="text-xs px-3 py-1.5 rounded bg-amber-500/20 hover:bg-amber-500/30 text-amber-200 border border-amber-500/30 shrink-0"
      >
        Open inbox
      </Link>
    </section>
  )
}
