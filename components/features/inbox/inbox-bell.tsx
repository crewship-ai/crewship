"use client"

import { useRouter } from "next/navigation"

import { toast } from "sonner"

import { useInbox } from "@/hooks/use-inbox"
import { useWorkspace } from "@/hooks/use-workspace"
import { inboxBulk } from "@/lib/api/inbox"

import { InboxBellView } from "./inbox-bell-new"
import type { WorkspaceRole } from "./inbox-derive"

// InboxBell — top-right actionable-items badge. Lives next to the
// NotificationBell so the surfaces stay distinct: bell = informational
// notifications, inbox = "you need to do something".
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
      onOpenItem={() => router.push("/inbox")}
      onOpenInbox={() => router.push("/inbox")}
      onMarkAllRead={async (ids) => {
        if (!workspaceId || ids.length === 0) return
        // Chunked to the backend's 500-id cap, same as the list's bulk bar.
        const CHUNK = 500
        let updated = 0
        for (let i = 0; i < ids.length; i += CHUNK) {
          const res = await inboxBulk(workspaceId, ids.slice(i, i + CHUNK), "read")
          if (!res.ok) {
            toast.error(res.error)
            return
          }
          updated += res.result.updated
        }
        toast.success(`${updated} marked read`)
        await refresh()
      }}
    />
  )
}
