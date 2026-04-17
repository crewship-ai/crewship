"use client"

import { useEffect, useState } from "react"
import { cn } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { KindBadge } from "./kind-badge"
import { formatRelativeTime } from "@/lib/time"
import type { ApprovalRow } from "@/lib/types/approvals"

interface ApprovalCardProps {
  row: ApprovalRow
  active: boolean
  onSelect: () => void
}

const STATUS_CLASS: Record<string, string> = {
  pending: "bg-amber-500/15 text-amber-300 border-amber-500/40",
  approved: "bg-emerald-500/15 text-emerald-300 border-emerald-500/40",
  denied: "bg-red-500/15 text-red-300 border-red-500/40",
  timeout: "bg-slate-500/15 text-slate-300 border-slate-500/40",
}

/**
 * One row in the approvals list. Clickable — selection opens the detail
 * sheet. Pending rows show a live countdown to `timeout_at`.
 */
export function ApprovalCard({ row, active, onSelect }: ApprovalCardProps) {
  const statusCls = STATUS_CLASS[row.status.toLowerCase()] ?? "bg-muted text-muted-foreground border-border"
  return (
    <button
      type="button"
      onClick={onSelect}
      className={cn(
        "w-full text-left rounded-lg border bg-card px-3 py-2.5 transition-colors hover:border-border",
        active ? "border-primary/60 ring-1 ring-primary/20" : "border-border/50",
      )}
    >
      <div className="flex items-center gap-2 flex-wrap">
        <KindBadge kind={row.kind} />
        <Badge variant="outline" className={cn("text-[10px] border", statusCls)}>
          {row.status}
        </Badge>
        {row.status === "pending" && row.timeout_at && (
          <TimeoutCountdown iso={row.timeout_at} />
        )}
        <span className="ml-auto text-[11px] text-muted-foreground font-mono tabular-nums">
          {formatRelativeTime(row.created_at)}
        </span>
      </div>
      <p className="mt-1.5 text-sm text-foreground/90 leading-snug line-clamp-2">
        {row.reason || <span className="text-muted-foreground italic">(no reason)</span>}
      </p>
      {row.requested_by && (
        <p className="mt-1 text-[11px] text-muted-foreground">
          requested by <span className="font-mono">{row.requested_by}</span>
        </p>
      )}
    </button>
  )
}

/** Live MM:SS countdown until `iso`. Ticks once per second, stops at expiry. */
function TimeoutCountdown({ iso }: { iso: string }) {
  // Parse once up front — a malformed timestamp would otherwise NaN every tick.
  const targetMs = new Date(iso).getTime()
  const isValid = Number.isFinite(targetMs)
  const [remaining, setRemaining] = useState(() =>
    isValid ? Math.max(0, targetMs - Date.now()) : 0,
  )

  useEffect(() => {
    if (!isValid) return
    // Don't even start the interval if already expired.
    if (targetMs - Date.now() <= 0) {
      setRemaining(0)
      return
    }
    const id = setInterval(() => {
      const next = Math.max(0, targetMs - Date.now())
      setRemaining(next)
      // Stop ticking the moment we hit zero — no work after expiry.
      if (next === 0) clearInterval(id)
    }, 1000)
    return () => clearInterval(id)
  }, [targetMs, isValid])

  const expired = remaining === 0
  const totalSec = Math.floor(remaining / 1000)
  const min = Math.floor(totalSec / 60)
  const sec = totalSec % 60
  return (
    <Badge
      variant="outline"
      className={cn(
        "text-[10px] font-mono border",
        expired
          ? "bg-red-500/15 text-red-300 border-red-500/40"
          : "bg-muted/40 text-muted-foreground border-border/60",
      )}
    >
      {expired ? "expired" : `${min}:${sec.toString().padStart(2, "0")}`}
    </Badge>
  )
}
