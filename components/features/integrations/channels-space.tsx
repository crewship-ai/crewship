"use client"

import * as React from "react"
import { AlertTriangle, Check, Clock, Pause, RefreshCw, Send, ShieldCheck, Webhook } from "lucide-react"

import { PageEmpty, PageError, PageLoading, PageNoAccess, PageNoResults } from "@/components/layout/page-state"
import { StatusPill } from "@/components/ui/status-pill"
import type { NotificationDelivery } from "@/hooks/use-notification-deliveries"
import { NOTIFICATION_CATEGORY_GROUPS } from "@/lib/notification-categories"
import { formatRelativeShort } from "@/lib/time"
import type { StatusTone } from "@/lib/format-status"
import { ProviderMark } from "./provider-marks"
import type { ConnectionKind, ConnectionRow } from "./connection-model"
import { CellMain, HealthStrip, ResponsiveList, SpaceHead, type HealthItem, type ListItem } from "./integrations-ui"

/**
 * Channels and Webhooks — two spaces over one backend (notification channels):
 * chat apps, email, push and incident tools reach people; a `webhook` channel
 * sends signed JSON to a system. Same list anatomy, different columns.
 */

export type ChannelsView = "all" | "failing" | "paused" | "mine"
export type ChannelKindFilter = Exclude<ConnectionKind, "webhook" | "tools">
export type WhoseFilter = "workspace" | "personal" | null

export const CHANNEL_KINDS: { key: ChannelKindFilter; label: string }[] = [
  { key: "chat", label: "Chat apps" },
  { key: "email", label: "Email" },
  { key: "push", label: "Push" },
  { key: "incident", label: "Incident tools" },
]

const CATEGORY_LABEL = new Map(NOTIFICATION_CATEGORY_GROUPS.flatMap((g) => g.categories.map((c) => [c.key, c.label] as const)))

/** "routines.failed, agents.error +2"; empty = every category. */
export function eventsSummary(categories: string[]): string {
  if (categories.length === 0) return "Every event"
  return categories.length > 2 ? `${categories.slice(0, 2).join(", ")} +${categories.length - 2}` : categories.join(", ")
}

export function categoryLabel(key: string): string {
  return CATEGORY_LABEL.get(key) ?? key
}

const PRIORITY_LABEL: Record<string, string> = { low: "Low", medium: "Normal", high: "High", urgent: "Urgent" }
export function priorityLabel(p: string | undefined): string {
  return PRIORITY_LABEL[p ?? "low"] ?? p ?? "Low"
}

const STATE: Record<ConnectionRow["status"], { label: string; tone: StatusTone }> = {
  delivering: { label: "Delivering", tone: "success" },
  failing: { label: "Failing", tone: "danger" },
  disabled: { label: "Paused", tone: "muted" },
  never_used: { label: "Never used", tone: "muted" },
  unknown: { label: "Not checked", tone: "muted" },
}

export function ChannelState({ row, size = "sm" }: { row: ConnectionRow; size?: "sm" | "md" }) {
  const s = STATE[row.status]
  return <StatusPill tone={s.tone} label={s.label} size={size} live={row.status === "delivering"} />
}

export function ChannelMark({ row, size = 28 }: { row: ConnectionRow; size?: 24 | 28 | 30 | 52 }) {
  const cls = size === 52 ? "size-[52px]" : size === 30 ? "size-[30px]" : size === 28 ? "size-7" : "size-6"
  return <ProviderMark provider={row.provider} label={row.providerLabel} className={cls} />
}

export function whoseLabel(row: ConnectionRow, currentUserId: string | null): string {
  if (row.scope === "workspace") return "Workspace"
  if (row.readOnly) return "Another member's"
  return row.channel?.owner_user_id && row.channel.owner_user_id !== currentUserId ? "Personal" : "Yours"
}

function isToday(iso: string): boolean {
  const d = new Date(iso)
  const now = new Date()
  return d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate()
}

export function matchesView(row: ConnectionRow, view: ChannelsView, currentUserId: string | null): boolean {
  if (view === "failing") return row.status === "failing"
  if (view === "paused") return !row.enabled
  if (view === "mine") return row.scope === "personal" && !row.readOnly && (!row.channel?.owner_user_id || row.channel.owner_user_id === currentUserId)
  return true
}

const VIEW_TITLE: Record<ChannelsView, string> = { all: "Channels", failing: "Failing", paused: "Paused", mine: "My channels" }

export function ChannelsList({
  webhooks,
  rows,
  allRows,
  view,
  kind,
  whose,
  search,
  deliveries,
  canSeeDeliveries,
  currentUserId,
  loading,
  error,
  onRetry,
  onOpen,
  onAdd,
  onView,
  onClearFilters,
}: {
  /** true = the Webhooks space. */
  webhooks: boolean
  /** Already filtered by view, kind, whose and search. */
  rows: ConnectionRow[]
  /** Every row of this space, for the health strip. */
  allRows: ConnectionRow[]
  view: ChannelsView
  kind: ChannelKindFilter | null
  whose: WhoseFilter
  search: string
  deliveries: NotificationDelivery[]
  canSeeDeliveries: boolean
  currentUserId: string | null
  loading: boolean
  error: string | null
  onRetry: () => void
  onOpen: (id: string) => void
  onAdd: () => void
  onView: (view: ChannelsView) => void
  onClearFilters: () => void
}) {
  const ids = React.useMemo(() => new Set(allRows.map((r) => r.id)), [allRows])
  const today = React.useMemo(() => deliveries.filter((d) => ids.has(d.channel_id) && isToday(d.created_at)), [deliveries, ids])
  const sentToday = today.filter((d) => d.status === "sent").length
  const failedToday = today.filter((d) => d.status === "failed").length
  const lastSent = React.useMemo(() => {
    const m = new Map<string, string>()
    for (const d of deliveries) if (d.status === "sent" && (m.get(d.channel_id) ?? "") < d.created_at) m.set(d.channel_id, d.created_at)
    return m
  }, [deliveries])

  const filtered = view !== "all" || kind !== null || whose !== null || search.trim() !== ""
  const adminOnly = "admins only"

  const health: HealthItem[] = webhooks
    ? [
        { key: "n", tone: "ok", icon: Webhook, label: "Webhooks", value: allRows.length, sub: "signed JSON", onClick: () => onView("all") },
        { key: "sent", tone: "muted", icon: Check, label: "Sent today", value: canSeeDeliveries ? sentToday : "—", sub: canSeeDeliveries ? "events delivered" : adminOnly },
        { key: "failed", tone: failedToday > 0 ? "bad" : "muted", icon: AlertTriangle, label: "Failed today", value: canSeeDeliveries ? failedToday : "—", sub: canSeeDeliveries ? (failedToday > 0 ? "after 3 tries" : "all delivered") : adminOnly, onClick: () => onView("failing") },
        { key: "retries", tone: "muted", icon: RefreshCw, label: "Retries", value: "3 tries", sub: "then it stops and logs the failure" },
        { key: "signed", tone: "muted", icon: ShieldCheck, label: "Signed", value: "HMAC", sub: "X-Crewship-Signature" },
      ]
    : [
        {
          key: "n",
          tone: "ok",
          icon: Send,
          label: "Channels",
          value: allRows.length,
          sub: `${allRows.filter((r) => r.scope === "workspace").length} workspace · ${allRows.filter((r) => r.scope === "personal").length} personal`,
          onClick: () => onView("all"),
        },
        { key: "sent", tone: "muted", icon: Check, label: "Sent today", value: canSeeDeliveries ? sentToday : "—", sub: canSeeDeliveries ? "messages" : adminOnly },
        { key: "failed", tone: failedToday > 0 ? "bad" : "muted", icon: AlertTriangle, label: "Failed today", value: canSeeDeliveries ? failedToday : "—", sub: canSeeDeliveries ? (failedToday > 0 ? "after 3 tries" : "all delivering") : adminOnly, onClick: () => onView("failing") },
        { key: "paused", tone: "muted", icon: Pause, label: "Paused", value: allRows.filter((r) => !r.enabled).length, sub: "not sending", onClick: () => onView("paused") },
      ]

  const title = webhooks ? (view === "all" ? "Webhooks" : VIEW_TITLE[view]) : kind ? CHANNEL_KINDS.find((k) => k.key === kind)!.label : VIEW_TITLE[view]
  const description = webhooks
    ? "Crewship sends events to your systems as signed JSON: a run finished, an issue moved, an agent needs approval. For people, use Channels."
    : "Where Crewship reaches people: chat apps, email, phones and incident tools."

  let body: React.ReactNode
  if (loading && allRows.length === 0) body = <PageLoading rows={5} />
  else if (error && allRows.length === 0)
    body = <PageError title={webhooks ? "Webhooks could not load" : "Channels could not load"} description="Triggers and Tools are not affected." onRetry={onRetry} detail={error} />
  else if (allRows.length === 0)
    body = webhooks ? (
      <PageEmpty
        icon={Webhook}
        title="No webhooks yet"
        description="Send signed JSON to your own systems when a run finishes, an issue moves or an agent needs approval."
        primary={{ label: "Add webhook", onClick: onAdd }}
        cli="crewship notifychannel add --type webhook --url https://…"
      />
    ) : (
      <PageEmpty
        icon={Send}
        title="No channels yet"
        description="Add Slack, email or a phone so Crewship can reach people."
        primary={{ label: "Add channel", onClick: onAdd }}
        cli="crewship notifychannel add"
      />
    )
  else if (rows.length === 0)
    body = (
      <PageNoResults
        filters={[view !== "all" && VIEW_TITLE[view], kind && CHANNEL_KINDS.find((k) => k.key === kind)?.label, whose && (whose === "workspace" ? "Whose: Workspace" : "Whose: Personal"), search.trim() && `“${search.trim()}”`].filter(Boolean).join(" · ")}
        onClear={onClearFilters}
      />
    )
  else {
    const items: ListItem[] = rows.map((r) => {
      const last = lastSent.get(r.id)
      const lastCell = canSeeDeliveries ? (last ? formatRelativeShort(last) : "—") : "—"
      return webhooks
        ? {
            id: r.id,
            label: `Open ${r.name}`,
            lead: <ChannelMark row={r} size={30} />,
            title: r.name,
            sub: eventsSummary(r.categories),
            state: <ChannelState row={r} />,
            cells: [
              <CellMain key="n" lead={<ChannelMark row={r} />} title={r.name} mono sub={canSeeDeliveries ? `${today.filter((d) => d.channel_id === r.id && d.status === "sent").length} sent today` : undefined} />,
              <span key="e" className="font-mono text-label text-muted-foreground">{eventsSummary(r.categories)}</span>,
              <span key="l" className="font-mono text-label text-muted-foreground">{lastCell}</span>,
              <ChannelState key="s" row={r} />,
            ],
          }
        : {
            id: r.id,
            label: `Open ${r.name}`,
            lead: <ChannelMark row={r} size={30} />,
            title: r.name,
            sub: `${r.providerLabel} · ${eventsSummary(r.categories)}`,
            state: <ChannelState row={r} />,
            cells: [
              <CellMain key="n" lead={<ChannelMark row={r} />} title={r.name} sub={r.providerLabel} />,
              <span key="e" className="font-mono text-label text-muted-foreground">{eventsSummary(r.categories)}</span>,
              <span key="p" className="text-label text-muted-foreground">{priorityLabel(r.channel?.min_priority)} and up</span>,
              <span key="w" className="text-label text-muted-foreground">{whoseLabel(r, currentUserId)}</span>,
              <span key="l" className="font-mono text-label text-muted-foreground">{lastCell}</span>,
              <ChannelState key="s" row={r} />,
            ],
          }
    })
    body = (
      <>
        <p className="text-label text-muted-foreground">
          {rows.length} {webhooks ? (rows.length === 1 ? "webhook" : "webhooks") : rows.length === 1 ? "channel" : "channels"}
        </p>
        <ResponsiveList
          caption={webhooks ? "Webhooks" : "Channels"}
          columns={
            webhooks
              ? [
                  { key: "n", label: "Endpoint" },
                  { key: "e", label: "Sends" },
                  { key: "l", label: "Last sent" },
                  { key: "s", label: "State" },
                ]
              : [
                  { key: "n", label: "Channel" },
                  { key: "e", label: "What goes there" },
                  { key: "p", label: "Priority" },
                  { key: "w", label: "Whose" },
                  { key: "l", label: "Last sent" },
                  { key: "s", label: "State" },
                ]
          }
          items={items}
          onOpen={onOpen}
        />
      </>
    )
  }

  return (
    <div className="grid min-w-0 gap-3.5">
      <SpaceHead title={title} description={description} />
      {!filtered && allRows.length > 0 && <HealthStrip items={health} label={webhooks ? "Webhooks at a glance" : "Channels at a glance"} />}
      {error && allRows.length > 0 && <PageError title="Some channels may be out of date" onRetry={onRetry} detail={error} />}
      {body}
    </div>
  )
}

/** Why a delivery did or did not arrive, in words. */
export function reasonFor(d: NotificationDelivery): string {
  if (d.error) return d.error
  switch (d.status) {
    case "sent":
      return "Delivered"
    case "pending":
      return d.attempts > 0 ? `Retrying — attempt ${d.attempts}` : "Queued"
    case "dropped_pref":
      return "The recipient has this kind of event muted"
    case "dropped_rate":
      return "Rate limit — folded into a digest"
    case "failed":
      return "The destination refused it after 3 tries"
    default:
      return d.status
  }
}

export function DeliveryRows({ deliveries, channelName, onOpenChannel, caption }: { deliveries: NotificationDelivery[]; channelName?: (id: string) => string; onOpenChannel?: (id: string) => void; caption: string }) {
  const items: ListItem[] = deliveries.map((d) => {
    const pill = <StatusPill status={d.status} />
    const channel = channelName?.(d.channel_id)
    return {
      id: d.id,
      label: `${d.category} ${formatRelativeShort(d.created_at)}${channel ? ` to ${channel}` : ""}`,
      lead: <Clock className="size-4 shrink-0 text-muted-foreground" aria-hidden />,
      title: d.title || d.category,
      sub: `${formatRelativeShort(d.created_at)} · ${d.category}${channel ? ` · ${channel}` : ""}`,
      meta: reasonFor(d),
      state: pill,
      cells: [
        <span key="t" className="whitespace-nowrap font-mono text-label text-muted-foreground">{formatRelativeShort(d.created_at)}</span>,
        <span key="e" className="font-mono text-label text-foreground">{d.category}</span>,
        ...(channelName ? [<span key="c" className="block max-w-[14rem] truncate text-label text-muted-foreground">{channel}</span>] : []),
        pill,
        <span key="a" className="font-mono text-label text-muted-foreground">{d.attempts}</span>,
        <span key="w" className="block max-w-[22rem] truncate text-label text-muted-foreground" title={reasonFor(d)}>{reasonFor(d)}</span>,
      ],
    }
  })
  return (
    <ResponsiveList
      caption={caption}
      columns={[
        { key: "t", label: "Sent" },
        { key: "e", label: "Event" },
        ...(channelName ? [{ key: "c", label: "Channel" }] : []),
        { key: "r", label: "Result" },
        { key: "a", label: "Tries" },
        { key: "w", label: "Why" },
      ]}
      items={items}
      onOpen={(id) => {
        const d = deliveries.find((x) => x.id === id)
        if (d && onOpenChannel) onOpenChannel(d.channel_id)
      }}
    />
  )
}

/** Channels › Deliveries: the workspace log, ADMIN/OWNER only. */
export function DeliveriesLog({
  deliveries,
  rows,
  loading,
  error,
  forbidden,
  onRetry,
  onOpenChannel,
}: {
  deliveries: NotificationDelivery[]
  rows: ConnectionRow[]
  loading: boolean
  error: string | null
  forbidden: boolean
  onRetry: () => void
  onOpenChannel: (id: string) => void
}) {
  const names = React.useMemo(() => new Map(rows.map((r) => [r.id, r.name])), [rows])
  let body: React.ReactNode
  if (forbidden)
    body = (
      <>
        <PageNoAccess area="the delivery log" needed="Admin" />
        <p className="text-center text-label text-muted-foreground">The delivery log is admin-only: it spans every recipient in this workspace. Your own channels and notifications are in the other views.</p>
      </>
    )
  else if (loading && deliveries.length === 0) body = <PageLoading rows={6} />
  else if (error && deliveries.length === 0) body = <PageError title="Deliveries could not load" onRetry={onRetry} detail={error} />
  else if (deliveries.length === 0)
    body = (
      <PageEmpty
        icon={Clock}
        title="Nothing sent yet"
        description="Every send lands here, including the muted and throttled ones, so a quiet channel can be told apart from a broken one."
        cli="crewship notifychannel deliveries"
      />
    )
  else body = <DeliveryRows deliveries={deliveries} channelName={(id) => names.get(id) ?? id} onOpenChannel={onOpenChannel} caption="Deliveries" />
  return (
    <div className="grid min-w-0 gap-3.5">
      <SpaceHead title="Deliveries" description="Why a notification did or did not arrive. Crewship tries each one up to 3 times." />
      {body}
    </div>
  )
}
