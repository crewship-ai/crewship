"use client"

import { useEffect, useState } from "react"
import { CheckCircle2, XCircle, Clock, MessageSquare } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import type { PendingWaitpoint } from "@/hooks/use-pending-approval"

// RoutineApprovalBanner — the "this run is parked on YOU" strip. Primary
// approval affordance on the routine page: click Run, the run hits a gate,
// and Approve / Reject are right here rather than only in /inbox.
//
// It used to render the entire prompt inline. Approval prompts are change
// plans — the real one is a rollback procedure with a maintenance window and
// an escalation path — so the strip grew into a wall of text that pushed the
// routine it belonged to off screen, in order to ask a yes/no question.
//
// Now: one line to recognise the decision by, the countdown, and the two
// buttons. The full request opens in a dialog, where a long document belongs
// and where there is room for the comment box.

interface Props {
  waitpoint: PendingWaitpoint
  deciding: boolean
  onDecide: (approved: boolean, comment?: string) => Promise<boolean>
  className?: string
}

export function RoutineApprovalBanner({ waitpoint, deciding, onDecide, className }: Props) {
  const [comment, setComment] = useState("")
  const [open, setOpen] = useState(false)
  const [remaining, setRemaining] = useState(() => fmtRemaining(waitpoint.timeout_at))

  // Live countdown, ticking once a minute — cheap and accurate enough for a
  // 24h default window. Resets when the waitpoint (token) changes. We also clear
  // the local comment draft and collapse the comment field on a token change so
  // a different pending waitpoint replacing this one (while the banner stays
  // mounted) can't carry the previous decision's comment/expanded state into the
  // new one.
  useEffect(() => {
    setComment("")
    setOpen(false)
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

  const firstLine = (waitpoint.prompt ?? "").trim().split("\n")[0]

  return (
    <div
      className={cn(
        "flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-warn/30 bg-warn/[0.06] px-3 py-2.5",
        className,
      )}
      role="region"
      aria-label="Approval needed"
    >
      <span className="inline-flex shrink-0 items-center gap-1.5 rounded-full bg-warn/15 px-2.5 py-0.5 text-[11px] font-semibold text-warn">
        <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-warn" />
        Approval needed
      </span>
      <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
        step {waitpoint.step_id}
      </span>

      {/* One line, so the strip is not anonymous — you can tell WHICH
          decision this is without opening anything. */}
      {firstLine && (
        <span className="min-w-0 flex-1 truncate text-[12px] text-foreground/85">{firstLine}</span>
      )}

      <span className="inline-flex shrink-0 items-center gap-1 text-[11px] text-muted-foreground">
        <Clock className="h-3 w-3" />
        expires in{" "}
        <span className={cn("text-foreground/85", urgent && "font-semibold text-destructive")}>
          {remaining}
        </span>
      </span>

      <div className="flex shrink-0 items-center gap-1.5">
        <Button
          size="sm"
          onClick={() => decide(true)}
          disabled={deciding}
          className="h-7 gap-1.5 bg-warn px-3 text-[11px] font-semibold text-background hover:bg-warn/90"
        >
          {deciding ? <Spinner className="h-3 w-3" /> : <CheckCircle2 className="h-3.5 w-3.5" />}
          Approve
        </Button>
        <Button
          size="sm"
          variant="outline"
          onClick={() => decide(false)}
          disabled={deciding}
          className="h-7 gap-1.5 px-3 text-[11px]"
        >
          <XCircle className="h-3.5 w-3.5" />
          Reject
        </Button>
        <Dialog open={open} onOpenChange={setOpen}>
          <DialogTrigger asChild>
            <button
              type="button"
              className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
            >
              <MessageSquare className="h-3 w-3" />
              View request
            </button>
          </DialogTrigger>
          <DialogContent className="max-w-2xl">
            <DialogHeader>
              <DialogTitle>Approval request</DialogTitle>
              <DialogDescription>
                Step <span className="font-mono">{waitpoint.step_id}</span> · expires in {remaining}
              </DialogDescription>
            </DialogHeader>
            <div className="max-h-[50vh] overflow-auto rounded-md border border-border/60 bg-background/40 px-3 py-2.5">
              <p className="whitespace-pre-wrap text-[13px] leading-relaxed text-foreground/90">
                {waitpoint.prompt}
              </p>
            </div>
            <textarea
              aria-label="Decision comment"
              placeholder="Decision comment (optional, sent to the parked run as the waitpoint payload)…"
              value={comment}
              onChange={(e) => setComment(e.target.value)}
              className="h-20 w-full resize-none rounded-md border border-white/[0.1] bg-background p-2.5 text-[13px] leading-relaxed placeholder:text-muted-foreground-soft"
            />
            <DialogFooter>
              <Button
                variant="outline"
                onClick={() => {
                  setOpen(false)
                  void decide(false)
                }}
                disabled={deciding}
                className="gap-1.5"
              >
                <XCircle className="h-3.5 w-3.5" />
                Reject
              </Button>
              <Button
                onClick={() => {
                  setOpen(false)
                  void decide(true)
                }}
                disabled={deciding}
                className="gap-1.5 bg-warn text-background hover:bg-warn/90"
              >
                <CheckCircle2 className="h-3.5 w-3.5" />
                Approve
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
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
