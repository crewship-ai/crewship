"use client"

import { useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  AlertCircle,
  Bell,
  CheckCircle2,
  ChevronRight,
  Clock,
  Inbox as InboxIcon,
  ScrollText,
  Sparkles,
  XCircle,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { useInbox, type InboxItem } from "@/hooks/use-inbox"
import { useWorkspace } from "@/hooks/use-workspace"
import { Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
import { toast } from "sonner"
import { WaitpointRunDetail } from "./waitpoint-run-detail"

// InboxList — the /inbox page surface. Linear-Triage UX: three states
// (unread → read → resolved) with no archive / flag / snooze. List on
// the left, detail on the right. Action buttons in the detail are
// kind-specific (Approve waitpoint, Resolve escalation, Retry failed
// run, …) and call the source-of-truth endpoint, then flip the inbox
// row to resolved.

type StateFilter = "unread" | "all" | "resolved"

interface KindMeta {
  label: string
  icon: React.ComponentType<{ className?: string }>
  accent: string
}

const KIND_META: Record<InboxItem["kind"], KindMeta> = {
  waitpoint: { label: "Waitpoint", icon: Clock, accent: "text-amber-300" },
  escalation: { label: "Escalation", icon: AlertCircle, accent: "text-rose-300" },
  failed_run: { label: "Failed run", icon: XCircle, accent: "text-rose-400" },
  message: { label: "Message", icon: Sparkles, accent: "text-blue-300" },
}

export function InboxList() {
  const { workspaceId } = useWorkspace()
  const [stateFilter, setStateFilter] = useState<StateFilter>("unread")
  const [selectedId, setSelectedId] = useState<string | null>(null)

  const filterParam = stateFilter === "all" ? "all" : stateFilter
  const { items, unreadCount, loading, error, patch, refresh } = useInbox(workspaceId, filterParam)

  const selected = useMemo(
    () => items.find((it) => it.id === selectedId) ?? null,
    [items, selectedId],
  )

  const counts = useMemo(() => {
    const unread = items.filter((i) => i.state === "unread").length
    const read = items.filter((i) => i.state === "read").length
    const resolved = items.filter((i) => i.state === "resolved").length
    return { unread, read, resolved, total: items.length }
  }, [items])

  return (
    <div className="flex h-[calc(100vh-48px)] bg-background">
      {/* ── List panel ─────────────────────────────────────────── */}
      <div className="flex w-[420px] shrink-0 flex-col border-r border-white/[0.06] bg-card">
        {/* Header */}
        <div className="flex shrink-0 items-center gap-2 border-b border-white/[0.06] px-3 py-2">
          <InboxIcon className="h-4 w-4 text-muted-foreground/60" />
          <span className="text-sm font-medium">Inbox</span>
          {unreadCount > 0 && (
            <span className="ml-auto rounded-full bg-blue-500/15 px-2 py-0.5 text-[10px] font-semibold text-blue-300">
              {unreadCount} unread
            </span>
          )}
        </div>

        {/* State filter */}
        <div className="flex shrink-0 gap-0 border-b border-white/[0.06]">
          <FilterTab
            label="Unread"
            count={stateFilter === "unread" ? items.length : counts.unread}
            active={stateFilter === "unread"}
            onClick={() => setStateFilter("unread")}
          />
          <FilterTab
            label="All"
            count={stateFilter === "all" ? items.length : null}
            active={stateFilter === "all"}
            onClick={() => setStateFilter("all")}
          />
          <FilterTab
            label="Resolved"
            count={stateFilter === "resolved" ? items.length : null}
            active={stateFilter === "resolved"}
            onClick={() => setStateFilter("resolved")}
          />
        </div>

        {/* List */}
        <div className="flex-1 overflow-y-auto">
          {loading && items.length === 0 ? (
            <div className="space-y-1 p-3">
              {[0, 1, 2].map((i) => (
                <Skeleton key={i} className="h-14 w-full rounded-md" />
              ))}
            </div>
          ) : error ? (
            <div className="p-6 text-center text-xs text-rose-300">
              Inbox unavailable: {error}
            </div>
          ) : items.length === 0 ? (
            <EmptyState filter={stateFilter} />
          ) : (
            <ul className="divide-y divide-white/[0.04]">
              {items.map((item) => (
                <InboxRow
                  key={item.id}
                  item={item}
                  selected={selectedId === item.id}
                  onSelect={() => {
                    setSelectedId(item.id)
                    if (item.state === "unread") {
                      patch(item.id, "read")
                    }
                  }}
                />
              ))}
            </ul>
          )}
        </div>
      </div>

      {/* ── Detail panel ───────────────────────────────────────── */}
      <div className="flex-1 overflow-y-auto bg-background">
        {selected ? (
          <InboxDetail
            item={selected}
            onResolve={async (action) => {
              await patch(selected.id, "resolved", action)
              toast.success(`Marked as ${action}`)
              refresh()
            }}
            onMarkUnread={() => patch(selected.id, "unread")}
          />
        ) : (
          <DetailEmpty />
        )}
      </div>
    </div>
  )
}

function FilterTab({
  label,
  count,
  active,
  onClick,
}: {
  label: string
  count: number | null
  active: boolean
  onClick: () => void
}) {
  return (
    <button
      onClick={onClick}
      className={cn(
        "flex flex-1 items-center justify-center gap-1.5 px-3 py-2 text-xs font-medium border-b-2 transition-colors",
        active
          ? "border-blue-400 text-blue-400"
          : "border-transparent text-muted-foreground hover:text-foreground/80",
      )}
    >
      <span>{label}</span>
      {count !== null && (
        <span className="rounded bg-white/[0.06] px-1.5 py-0.5 text-[10px] tabular-nums text-foreground/50">
          {count}
        </span>
      )}
    </button>
  )
}

function InboxRow({
  item,
  selected,
  onSelect,
}: {
  item: InboxItem
  selected: boolean
  onSelect: () => void
}) {
  const meta = KIND_META[item.kind]
  const Icon = meta.icon
  return (
    <motion.li
      layout
      onClick={onSelect}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault()
          onSelect()
        }
      }}
      className={cn(
        "flex cursor-pointer items-start gap-2 px-3 py-2.5 transition-colors",
        selected
          ? "border-l-2 border-blue-500 bg-blue-500/10"
          : "border-l-2 border-transparent hover:bg-white/[0.02]",
        item.state === "resolved" && "opacity-60",
      )}
    >
      {/* unread dot — left of icon */}
      <span
        className={cn(
          "mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full",
          item.state === "unread" ? "bg-blue-400" : "bg-transparent",
        )}
      />
      <Icon className={cn("mt-0.5 h-4 w-4 shrink-0", meta.accent)} />
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline gap-2">
          <span
            className={cn(
              "truncate text-xs",
              item.state === "unread" ? "font-medium text-foreground" : "text-foreground/70",
            )}
          >
            {item.title}
          </span>
        </div>
        <div className="mt-0.5 flex items-center gap-1.5 text-[10px] text-muted-foreground/70">
          <span>{meta.label}</span>
          {item.sender_name && <><span>·</span><span>{item.sender_name}</span></>}
          <span>·</span>
          <span>{relTime(item.created_at)}</span>
        </div>
      </div>
      <ChevronRight className="h-3 w-3 shrink-0 text-muted-foreground/30" />
    </motion.li>
  )
}

function InboxDetail({
  item,
  onResolve,
  onMarkUnread,
}: {
  item: InboxItem
  onResolve: (action: string) => void | Promise<void>
  onMarkUnread: () => void
}) {
  const meta = KIND_META[item.kind]
  const Icon = meta.icon
  const isResolved = item.state === "resolved"

  return (
    <div className="flex h-full flex-col">
      <div className="shrink-0 border-b border-white/[0.06] px-6 py-4">
        <div className="flex items-center gap-2 text-[11px] uppercase tracking-wider text-muted-foreground/60">
          <Icon className={cn("h-3.5 w-3.5", meta.accent)} />
          <span>{meta.label}</span>
          {item.priority !== "medium" && (
            <span className={cn(
              "rounded px-1.5 py-0.5 text-[9px] font-semibold",
              item.priority === "urgent" && "bg-rose-500/15 text-rose-300",
              item.priority === "high" && "bg-amber-500/15 text-amber-300",
              item.priority === "low" && "bg-white/[0.06] text-muted-foreground",
            )}>
              {item.priority}
            </span>
          )}
        </div>
        <h1 className="mt-1.5 text-base font-semibold">{item.title}</h1>
        <div className="mt-2 flex items-center gap-2 text-xs text-muted-foreground">
          {item.sender_name && <span>From: {item.sender_name}</span>}
          {item.sender_name && <span>·</span>}
          <span>{relTime(item.created_at)}</span>
          {isResolved && item.resolved_action && (
            <>
              <span>·</span>
              <span className="text-emerald-400">resolved · {item.resolved_action}</span>
            </>
          )}
        </div>
      </div>

      {item.body_md && (
        <div className="border-b border-white/[0.06] px-6 py-4 text-sm text-foreground/80 whitespace-pre-wrap">
          {item.body_md}
        </div>
      )}

      {/* Kind-specific actions */}
      <div className="px-6 py-4">
        <KindActions item={item} onResolve={onResolve} disabled={isResolved} />
      </div>

      {/* Rich run progress for waitpoints — fetches the underlying
        * pipeline_run + DSL definition so the user can see exactly
        * which step is paused and what each preceding step produced.
        * Only meaningful when the payload has a pipeline_run_id. */}
      {item.kind === "waitpoint" && (() => {
        const runID = item.payload?.pipeline_run_id
        if (typeof runID !== "string" || runID === "") return null
        return (
          <div className="border-t border-white/[0.06] px-6 py-4">
            <div className="mb-2 text-[10px] font-medium uppercase tracking-wider text-muted-foreground/60">
              Run progress
            </div>
            <WaitpointRunDetail
              workspaceId={item.workspace_id}
              pipelineRunId={runID}
            />
          </div>
        )
      })()}

      {/* Payload (debug / advanced) */}
      {item.payload && Object.keys(item.payload).length > 0 && (
        <div className="border-t border-white/[0.06] px-6 py-4 text-[11px]">
          <div className="mb-2 text-[10px] uppercase tracking-wider text-muted-foreground/60">
            Context
          </div>
          <pre className="overflow-auto rounded border border-white/[0.06] bg-card/40 p-2 text-[11px] font-mono">
{JSON.stringify(item.payload, null, 2)}
          </pre>
        </div>
      )}

      <div className="mt-auto border-t border-white/[0.06] bg-card/20 px-6 py-3">
        {!isResolved ? (
          <Button size="sm" variant="ghost" onClick={onMarkUnread} className="text-xs">
            <Bell className="mr-1.5 h-3 w-3" />
            Mark unread
          </Button>
        ) : (
          <span className="text-[11px] text-muted-foreground">
            Resolved {relTime(item.resolved_at ?? item.updated_at)}
            {item.resolved_action && ` · ${item.resolved_action}`}
          </span>
        )}
      </div>
    </div>
  )
}

function KindActions({
  item,
  onResolve,
  disabled,
}: {
  item: InboxItem
  onResolve: (action: string) => void | Promise<void>
  disabled: boolean
}) {
  const [busy, setBusy] = useState<string | null>(null)
  const wrap = async (action: string, fn: () => Promise<void>) => {
    setBusy(action)
    try {
      await fn()
    } finally {
      setBusy(null)
    }
  }

  switch (item.kind) {
    case "waitpoint":
      return (
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            disabled={disabled || busy !== null}
            onClick={() =>
              wrap("approved", async () => {
                const token = item.source_id
                const res = await fetch(
                  `/api/v1/workspaces/${encodeURIComponent(item.workspace_id)}/pipelines/waitpoints/${encodeURIComponent(token)}/approve`,
                  { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" },
                )
                if (!res.ok) {
                  const body = await res.json().catch(() => null)
                  toast.error(body?.error ?? "Approve failed")
                  return
                }
                await onResolve("approved")
              })
            }
            className="gap-1.5 bg-emerald-500/20 text-emerald-300 hover:bg-emerald-500/30"
          >
            <CheckCircle2 className="h-3 w-3" />
            {busy === "approved" ? "Approving…" : "Approve"}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={disabled || busy !== null}
            onClick={() => wrap("denied", async () => onResolve("denied"))}
            className="gap-1.5"
          >
            <XCircle className="h-3 w-3" />
            Deny
          </Button>
        </div>
      )
    case "escalation":
      return (
        <Button
          size="sm"
          disabled={disabled || busy !== null}
          onClick={() => wrap("acknowledged", async () => onResolve("acknowledged"))}
          className="gap-1.5"
        >
          <CheckCircle2 className="h-3 w-3" />
          Mark resolved
        </Button>
      )
    case "failed_run":
      return (
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            disabled={disabled || busy !== null}
            onClick={() => wrap("retried", async () => onResolve("retried"))}
            className="gap-1.5"
          >
            <ScrollText className="h-3 w-3" />
            Retry
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={disabled || busy !== null}
            onClick={() => wrap("cancelled", async () => onResolve("cancelled"))}
            className="gap-1.5"
          >
            Cancel
          </Button>
        </div>
      )
    default:
      return (
        <Button
          size="sm"
          disabled={disabled || busy !== null}
          onClick={() => wrap("dismissed", async () => onResolve("dismissed"))}
          className="gap-1.5"
        >
          Dismiss
        </Button>
      )
  }
}

function EmptyState({ filter }: { filter: StateFilter }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center">
      <InboxIcon className="h-8 w-8 text-muted-foreground/30" />
      <div className="text-sm font-medium">
        {filter === "unread" ? "All caught up" : filter === "resolved" ? "No resolved items yet" : "Nothing here"}
      </div>
      <p className="max-w-xs text-xs text-muted-foreground">
        {filter === "unread"
          ? "Agents have nothing waiting on you. New waitpoints, escalations, and failed runs will appear here."
          : "Items will show up as they get resolved."}
      </p>
    </div>
  )
}

function DetailEmpty() {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-12 text-center">
      <InboxIcon className="h-10 w-10 text-muted-foreground/20" />
      <div className="text-sm text-muted-foreground/60">Select an item to see details</div>
    </div>
  )
}

function relTime(iso?: string) {
  if (!iso) return "—"
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return "—"
  const diff = Date.now() - d.getTime()
  if (Math.abs(diff) < 60_000) return "just now"
  const mins = Math.round(Math.abs(diff) / 60_000)
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.round(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  const days = Math.round(hrs / 24)
  return `${days}d ago`
}

// Suppress unused warning when AnimatePresence is only used on motion.li
void AnimatePresence
