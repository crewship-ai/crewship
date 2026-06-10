"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { Bell, Check, CheckCheck } from "lucide-react"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Button } from "@/components/ui/button"
import { ScrollArea } from "@/components/ui/scroll-area"
import { formatRelativeTime } from "@/lib/time"
import { cn } from "@/lib/utils"
import { useWorkspace } from "@/hooks/use-workspace"
import { apiFetch } from "@/lib/api-fetch"
import type { Notification } from "@/lib/types/mission"

const ACTION_LABELS: Record<string, string> = {
  created: "created",
  updated: "updated",
  commented: "commented on",
  assigned: "assigned",
  completed: "completed",
  status_changed: "changed status of",
  priority_changed: "changed priority of",
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

  // Fetch list when dropdown opens
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

  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="h-8 w-8 relative"
          aria-label={unreadCount > 0 ? `${unreadCount} unread notifications` : "Notifications"}
        >
          <Bell className="h-4 w-4" />
          {unreadCount > 0 && (
            <span className="absolute -top-0.5 -right-0.5 flex h-4 w-4 items-center justify-center rounded-full bg-blue-500 text-[9px] font-bold text-white ring-2 ring-background">
              {unreadCount > 9 ? "9+" : unreadCount}
            </span>
          )}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-[360px] p-0">
        <div className="flex items-center justify-between px-3 py-2 border-b border-white/[0.06]">
          <span className="text-xs font-semibold">Notifications</span>
          {unreadCount > 0 && (
            <button
              onClick={markAllRead}
              className="flex items-center gap-1 text-[10px] text-blue-400 hover:text-blue-300 transition-colors"
            >
              <CheckCheck className="h-3 w-3" />
              Mark all read
            </button>
          )}
        </div>
        <ScrollArea className="max-h-[400px]">
          {loading && notifications.length === 0 ? (
            <div className="py-8 text-center text-xs text-muted-foreground-soft">Loading...</div>
          ) : notifications.length === 0 ? (
            <div className="py-8 text-center">
              <Bell className="h-5 w-5 mx-auto mb-2 text-muted-foreground/30" />
              <p className="text-xs text-muted-foreground-soft">No notifications yet</p>
            </div>
          ) : (
            <div className="py-1">
              {notifications.map((n) => {
                const isUnread = !n.read_at
                return (
                  <div
                    key={n.id}
                    role={isUnread ? "button" : undefined}
                    tabIndex={isUnread ? 0 : undefined}
                    aria-label={isUnread ? `Mark notification as read: ${n.actor_name || n.actor_type} ${ACTION_LABELS[n.action] || n.action} ${n.entity_title || ""}` : undefined}
                    className={cn(
                      "flex items-start gap-2.5 px-3 py-2 hover:bg-white/[0.04] transition-colors cursor-pointer group",
                      isUnread && "bg-blue-500/[0.03]",
                    )}
                    onClick={() => {
                      if (isUnread) markAsRead(n.id)
                    }}
                    onKeyDown={(e) => {
                      if (isUnread && (e.key === "Enter" || e.key === " ")) {
                        e.preventDefault()
                        markAsRead(n.id)
                      }
                    }}
                  >
                    {/* Unread dot */}
                    <div className="pt-1.5 shrink-0 w-2">
                      {isUnread && (
                        <div className="h-1.5 w-1.5 rounded-full bg-blue-400" />
                      )}
                    </div>

                    <div className="flex-1 min-w-0">
                      <p className="text-[11px] text-foreground/80 leading-relaxed">
                        <span className="font-medium">{n.actor_name || n.actor_type}</span>
                        {" "}
                        {ACTION_LABELS[n.action] || n.action}
                        {" "}
                        {n.entity_title && (
                          <span className="text-foreground/60">{n.entity_title}</span>
                        )}
                      </p>
                      <span className="text-[10px] text-muted-foreground-soft">
                        {formatRelativeTime(n.created_at)}
                      </span>
                    </div>

                    {/* Mark as read button */}
                    {isUnread && (
                      <button
                        onClick={(e) => {
                          e.stopPropagation()
                          markAsRead(n.id)
                        }}
                        className="opacity-0 group-hover:opacity-100 p-1 rounded hover:bg-white/[0.08] text-muted-foreground-soft hover:text-blue-400 transition-all shrink-0 mt-0.5"
                        aria-label="Mark as read"
                        title="Mark as read"
                      >
                        <Check className="h-3 w-3" />
                      </button>
                    )}
                  </div>
                )
              })}
            </div>
          )}
        </ScrollArea>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
