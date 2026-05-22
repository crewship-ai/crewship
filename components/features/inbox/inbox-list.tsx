"use client"

import { useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  AlertCircle,
  Bell,
  CheckCircle2,
  ChevronRight,
  CircleDot,
  Clock,
  Inbox as InboxIcon,
  ScrollText,
  Sparkles,
  XCircle,
} from "lucide-react"
import Link from "next/link"
import { cn } from "@/lib/utils"
import { useInbox, type InboxItem } from "@/hooks/use-inbox"
import { useWorkspace } from "@/hooks/use-workspace"
import { Button } from "@/components/ui/button"
import { ListRow } from "@/components/ui/list-row"
import { TabBar } from "@/components/ui/tab-bar"
import { ListRowSkeleton } from "@/components/ui/skeletons"
import { EmptyState as PageEmptyState } from "@/components/layout/empty-state"
import { toast } from "sonner"
import { WaitpointRunDetail } from "./waitpoint-run-detail"
import { waitpointDecide } from "@/lib/api/waitpoints"

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
        <TabBar
          value={stateFilter}
          onValueChange={(v) => setStateFilter(v as StateFilter)}
          layoutId="inbox-filter-indicator"
          ariaLabel="Filter inbox by state"
          className="shrink-0 [&>button]:flex-1"
        >
          <TabBar.Item
            value="unread"
            count={stateFilter === "unread" ? items.length : counts.unread}
          >
            Unread
          </TabBar.Item>
          <TabBar.Item value="all" count={stateFilter === "all" ? items.length : null}>
            All
          </TabBar.Item>
          <TabBar.Item
            value="resolved"
            count={stateFilter === "resolved" ? items.length : null}
          >
            Resolved
          </TabBar.Item>
        </TabBar>

        {/* List */}
        <div className="flex-1 overflow-y-auto">
          {loading && items.length === 0 ? (
            <ListRowSkeleton rows={3} className="p-3" />
          ) : error ? (
            <div className="p-6 text-center text-xs text-rose-300">
              Inbox unavailable: {error}
            </div>
          ) : items.length === 0 ? (
            <InboxEmpty filter={stateFilter} />
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
        <AnimatePresence mode="wait">
          {selected ? (
            <motion.div
              key={selected.id}
              initial={{ opacity: 0, y: 6 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0, y: -6 }}
              transition={{ duration: 0.18 }}
              className="h-full"
            >
              <InboxDetail
                item={selected}
                onResolve={async (action) => {
                  await patch(selected.id, "resolved", action)
                  toast.success(`Marked as ${action}`)
                  refresh()
                }}
                onMarkUnread={() => patch(selected.id, "unread")}
                // onRefresh lets source-managed actions (e.g. PR-D
                // approve-hire, which resolves the inbox row server-
                // side via inbox.ResolveBySource) repaint the list
                // without going through the inbox PATCH (which 409s
                // on kind=waitpoint for any state other than 'read').
                onRefresh={refresh}
              />
            </motion.div>
          ) : (
            <motion.div
              key="empty"
              initial={{ opacity: 0 }}
              animate={{ opacity: 1 }}
              exit={{ opacity: 0 }}
              transition={{ duration: 0.15 }}
              className="h-full"
            >
              <DetailEmpty />
            </motion.div>
          )}
        </AnimatePresence>
      </div>
    </div>
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
    <ListRow
      selected={selected}
      onSelect={onSelect}
      className={cn(
        "items-start gap-2 px-3 py-2.5",
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
    </ListRow>
  )
}

function InboxDetail({
  item,
  onResolve,
  onMarkUnread,
  onRefresh,
}: {
  item: InboxItem
  onResolve: (action: string) => void | Promise<void>
  onMarkUnread: () => void
  onRefresh: () => void | Promise<void>
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
        <KindActions item={item} onResolve={onResolve} onRefresh={onRefresh} disabled={isResolved} />
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
              inboxResolved={item.state === "resolved"}
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
  onRefresh,
  disabled,
}: {
  item: InboxItem
  onResolve: (action: string) => void | Promise<void>
  onRefresh: () => void | Promise<void>
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
    case "waitpoint": {
      // PR-D hire waitpoints share the inbox kind='waitpoint' shape
      // but live on a different source: source_id is an agent_id, not
      // a pipeline_waitpoints token, and the approve endpoint is
      // /agents/{id}/approve-hire (which resolves the inbox row
      // server-side via inbox.ResolveBySource). The generic
      // waitpointDecide() helper would 404 against the pipeline
      // waitpoints route for these. Disambiguated by payload.kind,
      // which writeInboxItem sets to "hire" for both blocking and
      // non-blocking hire surfaces (blocking lands as kind=waitpoint).
      if (item.payload?.kind === "hire") {
        return (
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              disabled={disabled || busy !== null}
              onClick={() =>
                wrap("approved", async () => {
                  // fetch() rejects on network failure (offline, DNS,
                  // CORS preflight). Without try/catch the user sees
                  // no toast and the action looks like silent success.
                  let res: Response
                  try {
                    res = await fetch(
                      `/api/v1/agents/${encodeURIComponent(item.source_id)}/approve-hire`,
                      {
                        method: "POST",
                        headers: { "Content-Type": "application/json" },
                      },
                    )
                  } catch (e) {
                    toast.error(e instanceof Error ? `Approve failed: ${e.message}` : "Approve failed (network error)")
                    return
                  }
                  if (!res.ok) {
                    const body = (await res.json().catch(() => null)) as
                      | { error?: string; reason?: string }
                      | null
                    toast.error(body?.error ?? `Approve failed (${res.status})`)
                    return
                  }
                  toast.success("Hire approved — agent is live")
                  await onRefresh()
                })
              }
              className="gap-1.5 bg-emerald-500/20 text-emerald-300 hover:bg-emerald-500/30"
            >
              <CheckCircle2 className="h-3 w-3" />
              {busy === "approved" ? "Approving…" : "Approve hire"}
            </Button>
            {/* No deny counterpart exists for approve-hire yet — the
                PENDING_REVIEW agent stays put until the operator fires
                it from the crew. Surface that explicitly so the
                missing button doesn't read as broken UI. */}
            <span className="text-[11px] text-muted-foreground">
              To deny, fire the agent from its crew page.
            </span>
          </div>
        )
      }
      // Both Approve and Deny hit the same /approve endpoint —
      // the body's `approved` boolean is what disambiguates. An empty
      // body decoded to approved=false because Go's JSON unmarshal
      // gives bools their zero value when absent, so a "{}" body was
      // silently equivalent to denying. The earlier "already decided
      // or expired" complaint was the second click hitting the
      // already-denied row.
      return (
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            disabled={disabled || busy !== null}
            onClick={() =>
              wrap("approved", async () => {
                const res = await waitpointDecide(item.workspace_id, item.source_id, true)
                if (!res.ok) {
                  toast.error(res.error)
                  return
                }
                // Server-side CompleteApproval already cascades
                // inbox state via inbox.ResolveBySource — the local
                // onResolve mostly ensures the optimistic UI
                // matches before the WS event arrives.
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
            onClick={() =>
              wrap("denied", async () => {
                const res = await waitpointDecide(item.workspace_id, item.source_id, false)
                if (!res.ok) {
                  toast.error(res.error)
                  return
                }
                await onResolve("denied")
              })
            }
            className="gap-1.5"
          >
            <XCircle className="h-3 w-3" />
            {busy === "denied" ? "Denying…" : "Deny"}
          </Button>
        </div>
      )
    }
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
      // Retry actually re-fires the routine: POST /pipelines/{slug}/run
      // with the same inputs that produced the failure (replayed from
      // the run's inputs_json so dynamic context is preserved). The
      // payload carries the slug + inputs the writer captured at
      // failure time. If the slug is missing we fall back to just
      // marking the inbox item resolved so the user isn't stuck.
      return (
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            disabled={disabled || busy !== null}
            onClick={() =>
              wrap("retried", async () => {
                const slug = (item.payload?.pipeline_slug ??
                  item.sender_name) as string | undefined
                const inputs = (item.payload?.inputs ?? {}) as Record<string, unknown>
                if (!slug) {
                  toast.error("Cannot retry — pipeline slug missing in payload")
                  await onResolve("cancelled")
                  return
                }
                // Same try/catch pattern as approve-hire above: fetch()
                // rejects on network failure (offline, DNS, CORS); the
                // wrap() helper clears busy state on return, so without
                // explicit error handling the user sees no toast and
                // the retry appears to silently succeed.
                let res: Response
                try {
                  res = await fetch(
                    `/api/v1/workspaces/${encodeURIComponent(item.workspace_id)}/pipelines/${encodeURIComponent(slug)}/run`,
                    {
                      method: "POST",
                      headers: { "Content-Type": "application/json" },
                      body: JSON.stringify({ inputs, triggered_via: "manual" }),
                    },
                  )
                } catch (e) {
                  toast.error(e instanceof Error ? `Retry failed: ${e.message}` : "Retry failed (network error)")
                  return
                }
                if (!res.ok) {
                  const body = await res.json().catch(() => null)
                  toast.error(body?.error ?? "Retry failed")
                  return
                }
                toast.success(`Routine ${slug} re-queued — see /activity`)
                await onResolve("retried")
              })
            }
            className="gap-1.5"
          >
            <ScrollText className="h-3 w-3" />
            {busy === "retried" ? "Retrying…" : "Retry"}
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
    case "message":
      // Messages from the orchestrator (e.g. "ENG-1 ready for review")
      // carry the issue identifier in payload so the inbox can offer
      // a one-click jump to the issue. Without this the user reads
      // the title and has nowhere to go.
      return (
        <div className="flex items-center gap-2">
          {typeof item.payload?.issue_identifier === "string" && (
            <Button asChild size="sm" className="gap-1.5">
              <Link
                href={`/issues/${encodeURIComponent(item.payload.issue_identifier as string)}`}
              >
                <CircleDot className="h-3 w-3" />
                Open {item.payload.issue_identifier}
              </Link>
            </Button>
          )}
          <Button
            size="sm"
            variant="ghost"
            disabled={disabled || busy !== null}
            onClick={() => wrap("dismissed", async () => onResolve("dismissed"))}
            className="gap-1.5"
          >
            Dismiss
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

function InboxEmpty({ filter }: { filter: StateFilter }) {
  return (
    <PageEmptyState
      size="inline"
      icon={InboxIcon}
      title={
        filter === "unread"
          ? "All caught up"
          : filter === "resolved"
            ? "No resolved items yet"
            : "Nothing here"
      }
      description={
        filter === "unread"
          ? "Agents have nothing waiting on you. New waitpoints, escalations, and failed runs will appear here."
          : "Items will show up as they get resolved."
      }
    />
  )
}

function DetailEmpty() {
  return (
    <PageEmptyState
      size="inline"
      icon={InboxIcon}
      title="Select an item"
      description="Pick a waitpoint, escalation, failed run, or message on the left to see details."
    />
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

