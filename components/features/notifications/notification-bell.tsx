"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import Link from "next/link"
import { Bell } from "lucide-react"

import {
  BarMenu,
  BarMenuBody,
  BarMenuEmpty,
  BarMenuFooter,
  BarMenuFooterAction,
  BarMenuFooterLink,
  BarMenuHeader,
  BarMenuRow,
  BarMenuSection,
} from "@/components/layout/bar-menu"
import { ActorAvatar } from "@/components/features/inbox/inbox-actor"
import type { Actor, ActorKind } from "@/components/features/inbox/inbox-types"
import { formatRelativeTime } from "@/lib/time"
import { useWorkspace } from "@/hooks/use-workspace"
import { apiFetch } from "@/lib/api-fetch"
import type { Notification } from "@/lib/types/mission"

// NotificationBell — the FYI half of the pair. Inbox = "you need to do
// something"; this = "something happened, in case you care". The two sit one
// icon apart and are read in the same glance, so they are now the same object:
// the shared top-bar kit (components/layout/bar-menu.tsx) draws both.
//
// What changed with the kit, beyond the frame:
//   · 9+ became 99+. The old badge capped at nine, so an eleventh unread read
//     "9+" next to an Inbox reporting "10" for the same size of pile.
//   · The flat list became UNREAD / EARLIER. One undivided run of rows made a
//     week-old status change look exactly as new as a mention from a minute
//     ago; the Inbox had solved that with sections and this had not.
//   · The row grew a face. "casey commented on Fix the login redirect" was a
//     paragraph of the same weight as everything around it; the actor's own
//     avatar (square = machine, circle = person, the rule inbox-actor.tsx
//     applies everywhere) does that identification faster than the words.
//   · The hover "mark as read" tick is gone: clicking the row already did
//     exactly that, and the tick only appeared on hover, so it taught a
//     control that a touch user could never find.
//   · A footer. "Mark N read" on the left, notification settings on the right
//     — the panel had no way out to the place where these are configured.

const ACTION_LABELS: Record<string, string> = {
  created: "created",
  updated: "updated",
  commented: "commented on",
  assigned: "assigned",
  completed: "completed",
  status_changed: "changed status of",
  priority_changed: "changed priority of",
}

// Notifications carry three actor types; the inbox's Actor covers five. Map
// rather than re-implement, so a notification from casey draws the same tile
// as an inbox item from casey.
function actorOf(n: Notification): Actor {
  const kind: ActorKind = n.actor_type === "user" ? "user" : n.actor_type === "agent" ? "agent" : "system"
  const label = n.actor_name || n.actor_type
  return { kind, id: n.actor_id, label, seed: kind === "agent" ? label : undefined }
}

export function NotificationBell() {
  const { workspaceId } = useWorkspace()
  const [notifications, setNotifications] = useState<Notification[]>([])
  const [unreadCount, setUnreadCount] = useState(0)
  const [loading, setLoading] = useState(false)
  const [open, setOpen] = useState(false)
  const countRequestSeq = useRef(0)
  const listRequestSeq = useRef(0)

  // Fetch unread count
  const fetchCount = useCallback(async () => {
    if (!workspaceId) return
    const seq = ++countRequestSeq.current
    try {
      const res = await apiFetch(`/api/v1/notifications/count?workspace_id=${encodeURIComponent(workspaceId)}`)
      if (res.ok && seq === countRequestSeq.current) {
        const data = await res.json()
        setUnreadCount(data.unread ?? data.count ?? 0)
      }
    } catch {
      // silent
    }
  }, [workspaceId])

  // Fetch notification list
  const fetchNotifications = useCallback(async () => {
    if (!workspaceId) return
    const seq = ++listRequestSeq.current
    setLoading(true)
    try {
      const res = await apiFetch(`/api/v1/notifications?workspace_id=${encodeURIComponent(workspaceId)}&limit=20`)
      if (res.ok && seq === listRequestSeq.current) {
        const data = await res.json()
        setNotifications(Array.isArray(data) ? data : data.notifications ?? [])
      }
    } catch {
      // silent
    } finally {
      if (seq === listRequestSeq.current) {
        setLoading(false)
      }
    }
  }, [workspaceId])

  // Poll for unread count every 30s
  useEffect(() => {
    fetchCount()
    const interval = setInterval(fetchCount, 30000)
    return () => clearInterval(interval)
  }, [fetchCount])

  // Fetch list when the panel opens
  useEffect(() => {
    if (open) {
      fetchNotifications()
    }
  }, [open, fetchNotifications])

  const markAsRead = useCallback(
    async (notificationId: string) => {
      if (!workspaceId) return
      try {
        const res = await apiFetch(`/api/v1/notifications/${encodeURIComponent(notificationId)}/read?workspace_id=${encodeURIComponent(workspaceId)}`, {
          method: "POST",
        })
        if (!res.ok) return
        setNotifications((prev) =>
          prev.map((n) => (n.id === notificationId ? { ...n, read_at: new Date().toISOString() } : n)),
        )
        setUnreadCount((c) => Math.max(0, c - 1))
      } catch {
        // silent
      }
    },
    [workspaceId],
  )

  const markAllRead = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await apiFetch(`/api/v1/notifications/read-all?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "POST",
      })
      if (!res.ok) return
      setNotifications((prev) =>
        prev.map((n) => ({ ...n, read_at: n.read_at ?? new Date().toISOString() })),
      )
      setUnreadCount(0)
    } catch {
      // silent
    }
  }, [workspaceId])

  // Newest first inside each bucket, unread above seen. Arrival order alone
  // put a read status change from Tuesday above a mention from a minute ago.
  const { unread, earlier } = useMemo(() => {
    const byNewest = (a: Notification, b: Notification) =>
      Date.parse(b.created_at) - Date.parse(a.created_at)
    return {
      unread: notifications.filter((n) => !n.read_at).sort(byNewest),
      earlier: notifications.filter((n) => n.read_at).sort(byNewest),
    }
  }, [notifications])

  return (
    <BarMenu
      icon={Bell}
      ariaLabel={unreadCount > 0 ? `${unreadCount} unread notifications` : "Notifications"}
      badge={{ count: unreadCount, tone: "info" }}
      open={open}
      onOpenChange={setOpen}
      testId="notifications"
    >
      <BarMenuHeader
        title="Notifications"
        meta={unreadCount > 0 ? `${unreadCount} unread` : "all read"}
      />

      <BarMenuBody>
        {loading && notifications.length === 0 ? (
          <p className="type-meta py-8 text-center text-muted-foreground-soft">Loading…</p>
        ) : notifications.length === 0 ? (
          <BarMenuEmpty icon={Bell} message="No notifications yet" />
        ) : (
          <>
            {unread.length > 0 && (
              <BarMenuSection label="Unread" count={unread.length}>
                {unread.map((n) => (
                  <NotificationRow key={n.id} n={n} onClick={() => markAsRead(n.id)} />
                ))}
              </BarMenuSection>
            )}
            {earlier.length > 0 && (
              <BarMenuSection label="Earlier" count={earlier.length}>
                {earlier.map((n) => (
                  <NotificationRow key={n.id} n={n} />
                ))}
              </BarMenuSection>
            )}
          </>
        )}
      </BarMenuBody>

      <BarMenuFooter>
        {unreadCount > 0 && (
          <BarMenuFooterAction onClick={markAllRead}>Mark {unreadCount} read</BarMenuFooterAction>
        )}
        <BarMenuFooterLink asChild onClick={() => setOpen(false)}>
          <Link href="/integrations?tab=notifications">Notification settings →</Link>
        </BarMenuFooterLink>
      </BarMenuFooter>
    </BarMenu>
  )
}

// One notification, in the bar's row skeleton: who, in the identity slot;
// what happened, on the title line; what it was about and its kind, on the
// meta line; when, on the right. A read row has nothing left to do, so it is
// not clickable — the old one was a focusable control that no-oped.
function NotificationRow({ n, onClick }: { n: Notification; onClick?: () => void }) {
  const actor = actorOf(n)
  const action = ACTION_LABELS[n.action] || n.action

  return (
    <BarMenuRow
      testId={`notification-row-${n.id}`}
      onClick={onClick}
      leading={<ActorAvatar actor={actor} size={24} />}
      // The actor is the face on the left and the name on the meta line, so
      // the title is what HAPPENED — the same split the inbox row uses
      // ("Skill review sk_469…" over "Skill Curator · agents.escalation").
      title={n.entity_title ? `${action} ${n.entity_title}` : `${action} ${n.entity_type}`}
      meta={
        <>
          <span className="truncate">{actor.label}</span>
          <span>·</span>
          <span className="truncate font-mono">{n.entity_type}</span>
        </>
      }
      trailing={
        <span className="type-meta text-muted-foreground-soft">{formatRelativeTime(n.created_at)}</span>
      }
    />
  )
}
