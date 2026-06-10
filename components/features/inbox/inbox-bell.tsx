"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import {
  AlertCircle,
  Clock,
  Inbox as InboxIcon,
  Sparkles,
  XCircle,
} from "lucide-react"
import { motion, AnimatePresence } from "motion/react"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Button } from "@/components/ui/button"
import { ScrollArea } from "@/components/ui/scroll-area"
import { useInbox, type InboxItem } from "@/hooks/use-inbox"
import { useWorkspace } from "@/hooks/use-workspace"
import { cn } from "@/lib/utils"

// InboxBell — top-right actionable-items badge. Lives next to the
// existing NotificationBell so the surface stays distinct: bell =
// informational notifications, inbox = "you need to do something."
//
// Click → dropdown with the 5 most-recent unread items + a footer
// link to /inbox. No inline actions in the dropdown — that path is
// reserved for the full /inbox surface where the user has space to
// see context. Keeps the bell ruthlessly read-only, matching Linear's
// notification dropdown.
export function InboxBell() {
  const router = useRouter()
  const { workspaceId } = useWorkspace()
  const [open, setOpen] = useState(false)
  const { items, unreadCount } = useInbox(workspaceId, "unread")

  const recent = items.slice(0, 5)

  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="relative h-8 w-8"
          aria-label={`Inbox: ${unreadCount} unread`}
        >
          <InboxIcon className="h-4 w-4" />
          <AnimatePresence>
            {unreadCount > 0 && (
              <motion.span
                initial={{ scale: 0 }}
                animate={{ scale: 1 }}
                exit={{ scale: 0 }}
                className="absolute -right-0.5 -top-0.5 flex h-4 min-w-[16px] items-center justify-center rounded-full bg-blue-500 px-1 text-[9px] font-semibold text-white"
              >
                {unreadCount > 99 ? "99+" : unreadCount}
              </motion.span>
            )}
          </AnimatePresence>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-[360px] p-0">
        <div className="flex items-center justify-between border-b border-white/[0.06] px-3 py-2">
          <span className="text-xs font-medium">Inbox</span>
          <span className="text-[10px] text-muted-foreground">
            {unreadCount} unread
          </span>
        </div>
        <ScrollArea className="max-h-[400px]">
          {recent.length === 0 ? (
            <div className="flex flex-col items-center justify-center gap-2 p-6 text-center">
              <InboxIcon className="h-6 w-6 text-muted-foreground/30" />
              <span className="text-xs text-muted-foreground">All caught up</span>
            </div>
          ) : (
            <ul className="divide-y divide-white/[0.04]">
              {recent.map((item) => (
                <BellRow
                  key={item.id}
                  item={item}
                  onClick={() => {
                    setOpen(false)
                    router.push("/inbox")
                  }}
                />
              ))}
            </ul>
          )}
        </ScrollArea>
        <div className="border-t border-white/[0.06] p-2">
          <Link
            href="/inbox"
            onClick={() => setOpen(false)}
            className="block w-full rounded px-2 py-1.5 text-center text-xs text-blue-400 hover:bg-white/[0.04]"
          >
            View all in inbox →
          </Link>
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function BellRow({ item, onClick }: { item: InboxItem; onClick: () => void }) {
  const Icon =
    item.kind === "waitpoint"
      ? Clock
      : item.kind === "escalation"
        ? AlertCircle
        : item.kind === "failed_run"
          ? XCircle
          : Sparkles
  const accent =
    item.kind === "waitpoint"
      ? "text-amber-300"
      : item.kind === "escalation"
        ? "text-rose-300"
        : item.kind === "failed_run"
          ? "text-rose-400"
          : "text-blue-300"

  return (
    <li
      onClick={onClick}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault()
          onClick()
        }
      }}
      className="flex cursor-pointer items-start gap-2 px-3 py-2 hover:bg-white/[0.04]"
    >
      <Icon className={cn("mt-0.5 h-3.5 w-3.5 shrink-0", accent)} />
      <div className="min-w-0 flex-1">
        <div className="truncate text-xs font-medium text-foreground">{item.title}</div>
        <div className="mt-0.5 text-[10px] text-muted-foreground">
          {item.sender_name ? `${item.sender_name} · ` : ""}
          {relTime(item.created_at)}
        </div>
      </div>
    </li>
  )
}

function relTime(iso?: string) {
  if (!iso) return ""
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ""
  const diff = Date.now() - d.getTime()
  if (Math.abs(diff) < 60_000) return "just now"
  const mins = Math.round(Math.abs(diff) / 60_000)
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.round(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  return `${Math.round(hrs / 24)}d ago`
}
