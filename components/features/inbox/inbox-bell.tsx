"use client"

import { useRouter } from "next/navigation"

import { toast } from "sonner"

import { useInbox } from "@/hooks/use-inbox"
import { useWorkspace } from "@/hooks/use-workspace"
import { inboxBulk } from "@/lib/api/inbox"

import { InboxBellView } from "./inbox-bell-new"
import type { WorkspaceRole } from "./inbox-derive"

// InboxBell — top-right actionable-items badge. It sits beside the
// ActivityBell, and the split between them is who is waiting: Activity is
// what the machines are doing, this is what a human is being asked for.
// (A third "informational notifications" bell used to sit here; it read a
// table nothing ever wrote to. See app-toolbar.tsx.)
//
// It reads the ACTIVE list, not the unread one. The list it used to read was
// state=unread, so a blocking escalation that had merely been opened dropped
// out of the bell while the agent was still parked on it — the /inbox page had
// already been fixed for exactly this and the bell was never brought along.
//
// Ordering, sectioning and the badge live in InboxBellView; this is the data
// end of it.
export function InboxBell() {
  const router = useRouter()
  const { workspaceId, role } = useWorkspace()
  const { items, refresh } = useInbox(workspaceId, "active")

  return (
    <InboxBellView
      items={items}
      role={(role as WorkspaceRole | null) ?? null}
      onOpenItem={(id) => router.push(`/inbox?item=${encodeURIComponent(id)}`)}
      onOpenInbox={() => router.push("/inbox")}
      onMarkAllRead={async (ids) => {
        if (!workspaceId || ids.length === 0) return
        // Chunked to the backend's 500-id cap, same as the list's bulk bar.
        const CHUNK = 500
        let updated = 0
        let failure: string | null = null
        for (let i = 0; i < ids.length; i += CHUNK) {
          const res = await inboxBulk(workspaceId, ids.slice(i, i + CHUNK), "read")
          if (!res.ok) {
            failure = res.error
            break
          }
          updated += res.result.updated
        }
        // Refresh whatever landed. Returning early on a mid-loop failure left
        // the server ahead of the screen: the first chunk was read, the badge
        // still counted it, and nothing corrected that until the next event.
        if (updated > 0) await refresh()
        if (failure) toast.error(updated > 0 ? `${updated} marked read, then: ${failure}` : failure)
        else toast.success(`${updated} marked read`)
      }}
    />
  )
}
