"use client"

import * as React from "react"
import { Clock, Lock } from "lucide-react"

import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
import type { NotificationDelivery } from "@/hooks/use-notification-deliveries"
import type { ConnectionRow } from "../connection-model"

/**
 * The delivery log — "why didn't my notification arrive?".
 *
 * Every status gets a plain-language reason, because the raw values are the
 * whole reason this view exists: `dropped_pref` means the recipient muted the
 * category and `dropped_rate` means we throttled it, and neither is a failure
 * — but on the old page both were invisible, so a muted category looked
 * exactly like a broken webhook.
 */

const STATUS_STYLE: Record<string, string> = {
  sent: "border-success/30 bg-success/10 text-success",
  failed: "border-destructive/35 bg-destructive/10 text-destructive",
  pending: "border-info/25 bg-info/10 text-info",
  dropped_pref: "border-warn/30 bg-warn/10 text-warn",
  dropped_rate: "border-warn/30 bg-warn/10 text-warn",
}

const STATUS_LABEL: Record<string, string> = {
  sent: "sent",
  failed: "failed",
  pending: "pending",
  dropped_pref: "muted",
  dropped_rate: "rate-gated",
}

function reasonFor(d: NotificationDelivery): string {
  if (d.error) return d.error
  switch (d.status) {
    case "sent":
      return d.sent_at ? `delivered ${relative(d.sent_at)}` : "delivered"
    case "pending":
      return d.attempts > 0 ? `retrying — attempt ${d.attempts}` : "queued"
    case "dropped_pref":
      return "recipient has this category muted"
    case "dropped_rate":
      return "rate limit — folded into a digest"
    case "failed":
      return "the destination rejected it"
    default:
      return d.status
  }
}

interface DeliveriesViewProps {
  deliveries: NotificationDelivery[]
  rows: ConnectionRow[]
  loading: boolean
  error: string | null
  forbidden: boolean
}

export function DeliveriesView({
  deliveries,
  rows,
  loading,
  error,
  forbidden,
}: DeliveriesViewProps) {
  const channelName = React.useMemo(() => {
    const m = new Map<string, string>()
    for (const r of rows) m.set(r.id, r.name)
    return m
  }, [rows])

  if (forbidden) {
    return (
      <div className="flex flex-col items-center justify-center px-6 py-20 text-center">
        <div className="mb-3 flex h-10 w-10 items-center justify-center rounded-lg bg-white/[0.04]">
          <Lock className="h-4 w-4 text-muted-foreground/60" />
        </div>
        <div className="text-sm font-medium text-foreground/85">The delivery log is admin-only</div>
        <p className="mt-1 max-w-sm text-xs leading-relaxed text-muted-foreground">
          It spans every recipient in this workspace, not just you, so it needs the ADMIN or OWNER
          role. Your own connections and preferences are on the other tabs.
        </p>
      </div>
    )
  }

  if (loading) {
    return (
      <div className="p-4 md:p-6">
        <Skeleton className="h-[280px] rounded-xl" />
      </div>
    )
  }

  return (
    <div className="space-y-4 p-4 md:p-6">
      {error && (
        <div className="rounded-lg border border-destructive/30 bg-destructive/[0.06] px-3 py-2 text-xs text-destructive">
          {error}
        </div>
      )}

      {deliveries.length === 0 ? (
        <div className="flex flex-col items-center justify-center rounded-xl border border-white/[0.08] bg-card px-6 py-14 text-center">
          <div className="mb-3 flex h-10 w-10 items-center justify-center rounded-lg bg-white/[0.04]">
            <Clock className="h-4 w-4 text-muted-foreground/60" />
          </div>
          <div className="text-sm font-medium text-foreground/85">Nothing sent yet</div>
          <p className="mt-1 max-w-sm text-xs text-muted-foreground">
            Every send lands here — including the ones that were muted or throttled, so a quiet
            channel can be told apart from a broken one.
          </p>
        </div>
      ) : (
        <div className="overflow-hidden rounded-xl border border-white/[0.08] bg-card">
          <div className="flex items-center gap-2 border-b border-white/[0.06] px-4 py-2.5">
            <span className="text-[10px] font-semibold uppercase tracking-wider text-foreground/50">
              Deliveries
            </span>
            <span className="font-mono text-[10px] text-muted-foreground/60">
              {deliveries.length} most recent
            </span>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-xs">
              <thead>
                <tr className="border-b border-white/[0.06]">
                  <Th>When</Th>
                  <Th>Category</Th>
                  <Th>Connection</Th>
                  <Th>Outcome</Th>
                  <Th>Detail</Th>
                </tr>
              </thead>
              <tbody>
                {deliveries.map((d) => (
                  <tr
                    key={d.id}
                    className="border-b border-white/[0.04] last:border-0 hover:bg-white/[0.02]"
                  >
                    <td className="whitespace-nowrap px-4 py-2 font-mono text-[11px] tabular-nums text-muted-foreground">
                      {relative(d.created_at)}
                    </td>
                    <td className="px-4 py-2 font-mono text-[11px] text-foreground/80">
                      {d.category}
                    </td>
                    <td className="max-w-[14rem] px-4 py-2 text-[11px] text-muted-foreground">
                      <span className="block truncate">
                        {channelName.get(d.channel_id) ?? d.channel_id}
                      </span>
                    </td>
                    <td className="px-4 py-2">
                      <span
                        className={cn(
                          "inline-flex items-center rounded-full border px-2 py-0.5 font-mono text-[10px]",
                          STATUS_STYLE[d.status] ?? "border-white/10 bg-white/[0.03] text-muted-foreground",
                        )}
                      >
                        {STATUS_LABEL[d.status] ?? d.status}
                      </span>
                    </td>
                    <td className="max-w-[22rem] px-4 py-2 text-[11px] text-muted-foreground">
                      <span className="block truncate" title={reasonFor(d)}>
                        {reasonFor(d)}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  )
}

function Th({ children }: { children: React.ReactNode }) {
  return (
    <th className="whitespace-nowrap px-4 py-2 text-left text-[10px] font-semibold uppercase tracking-wider text-foreground/45">
      {children}
    </th>
  )
}

/** "2 min", "3 h", "5 d" — compact enough for a log column. */
function relative(iso: string): string {
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return iso
  const sec = Math.floor((Date.now() - t) / 1000)
  if (sec < 60) return "just now"
  if (sec < 3600) return `${Math.floor(sec / 60)} min`
  if (sec < 86400) return `${Math.floor(sec / 3600)} h`
  return `${Math.floor(sec / 86400)} d`
}
