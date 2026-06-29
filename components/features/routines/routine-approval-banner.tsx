"use client"

import { useEffect, useState } from "react"
import { CheckCircle2, XCircle, Clock, MessageSquare, ChevronDown } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import type { PendingWaitpoint } from "@/hooks/use-pending-approval"

// RoutineApprovalBanner — the inline "this run is parked on YOU" card pinned
// to the top of a routine run's activity panel. It is the primary approval
// affordance on the routine page: the user clicks Run, the run hits an
// approval gate, and Approve / Reject show up right here instead of only in
// the workspace-wide Wait points tab or /inbox.

interface Props {
  waitpoint: PendingWaitpoint
  deciding: boolean
  onDecide: (approved: boolean, comment?: string) => Promise<boolean>
  className?: string
}

export function RoutineApprovalBanner({ waitpoint, deciding, onDecide, className }: Props) {
  const [comment, setComment] = useState("")
  const [showComment, setShowComment] = useState(false)
  const [remaining, setRemaining] = useState(() => fmtRemaining(waitpoint.timeout_at))

  // Live countdown, ticking once a minute — cheap and accurate enough for a
  // 24h default window. Resets when the waitpoint (token) changes. We also clear
  // the local comment draft and collapse the comment field on a token change so
  // a different pending waitpoint replacing this one (while the banner stays
  // mounted) can't carry the previous decision's comment/expanded state into the
  // new one.
  useEffect(() => {
    setComment("")
    setShowComment(false)
    setRemaining(fmtRemaining(waitpoint.timeout_at))
    const id = setInterval(() => setRemaining(fmtRemaining(waitpoint.timeout_at)), 60_000)
    return () => clearInterval(id)
  }, [waitpoint.timeout_at, waitpoint.token])

  const decide = async (approved: boolean) => {
    const ok = await onDecide(approved, comment)
    if (ok) toast.success(approved ? "Approved" : "Rejected")
    else toast.error("Decision failed")
  }

  const urgent = isUrgent(waitpoint.timeout_at)

  return (
    <div
      className={cn(
        "rounded-lg border border-amber-500/30 bg-amber-500/[0.06] p-3.5",
        className,
      )}
      role="region"
      aria-label="Approval needed"
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="inline-flex items-center gap-1.5 rounded-full bg-amber-500/15 px-2.5 py-1 text-[11px] font-semibold text-amber-300">
          <span className="h-1.5 w-1.5 rounded-full bg-amber-400 animate-pulse" />
          Approval needed
        </span>
        <span className="font-mono text-[11px] text-muted-foreground">step {waitpoint.step_id}</span>
        <span className="ml-auto inline-flex items-center gap-1 text-[11px] text-muted-foreground">
          <Clock className="h-3 w-3" />
          expires in{" "}
          <span className={cn("text-foreground/85", urgent && "font-semibold text-rose-400")}>
            {remaining}
          </span>
        </span>
      </div>

      {waitpoint.prompt && (
        <div className="mt-2.5 flex items-start gap-2 rounded-md border border-amber-500/15 bg-background/40 px-3 py-2.5">
          <MessageSquare className="mt-0.5 h-3.5 w-3.5 shrink-0 text-amber-300/70" />
          <p className="whitespace-pre-wrap text-[13px] leading-relaxed text-foreground/90">
            {waitpoint.prompt}
          </p>
        </div>
      )}

      {showComment && (
        <textarea
          autoFocus
          aria-label="Decision comment"
          placeholder="Decision comment (optional, sent to the parked run as the waitpoint payload)…"
          value={comment}
          onChange={(e) => setComment(e.target.value)}
          className="mt-2.5 h-16 w-full resize-none rounded-md border border-white/[0.1] bg-background p-2.5 text-[13px] leading-relaxed placeholder:text-muted-foreground-soft"
        />
      )}

      <div className="mt-3 flex items-center gap-2">
        <Button
          size="sm"
          onClick={() => decide(true)}
          disabled={deciding}
          className="h-8 gap-1.5 bg-amber-500 px-3.5 text-xs font-semibold text-amber-950 hover:bg-amber-400"
        >
          {deciding ? <Spinner className="h-3 w-3" /> : <CheckCircle2 className="h-3.5 w-3.5" />}
          Approve
        </Button>
        <Button
          size="sm"
          variant="outline"
          onClick={() => decide(false)}
          disabled={deciding}
          className="h-8 gap-1.5 px-3.5 text-xs"
        >
          <XCircle className="h-3.5 w-3.5" />
          Reject
        </Button>
        {!showComment && (
          <button
            type="button"
            onClick={() => setShowComment(true)}
            className="ml-1 inline-flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground/80"
          >
            <ChevronDown className="h-3 w-3" />
            add comment
          </button>
        )}
      </div>
    </div>
  )
}

// fmtRemaining renders the time left until timeout as "23h 58m" / "45m" /
// "expired". Mirrors the urgency math the Wait points tab uses.
function fmtRemaining(timeoutAt: string): string {
  const ms = new Date(timeoutAt).getTime() - Date.now()
  if (!Number.isFinite(ms) || ms <= 0) return "expired"
  const totalMins = Math.floor(ms / 60_000)
  const hrs = Math.floor(totalMins / 60)
  const mins = totalMins % 60
  if (hrs >= 1) return `${hrs}h ${mins}m`
  return `${mins}m`
}

function isUrgent(timeoutAt: string): boolean {
  const ms = new Date(timeoutAt).getTime() - Date.now()
  return Number.isFinite(ms) && ms < 2 * 3600_000
}
