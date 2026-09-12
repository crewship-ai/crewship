"use client"

/**
 * Replay (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §4, §9).
 *
 * §9 asks for three things here and all three are the same point: a replay is
 * not a retry.
 *
 *   - It shows the ORIGINAL INPUT. Not the input itself — the read API never
 *     returns it, by design, because for a chat or assignment source that IS
 *     the conversation. What it shows is the input's immutable fingerprint,
 *     which is what tells two work items apart without reading either.
 *   - It shows the TARGET REVISION, and leaving the field alone inherits the
 *     original. Running against a different revision has to be explicit; a
 *     blank that quietly means "latest" is the failure mode §4 names.
 *   - It says, in words, that this CREATES NEW WORK carrying the current
 *     caller's authorization — a new work id with replay_of set, not a second
 *     go at the old one.
 *
 * And the fourth thing, which is why this component takes an `availability`
 * rather than deciding for itself: when the payload has passed retention there
 * is no button. §9 forbids offering one that cannot work, so the reason
 * replaces the affordance instead of arriving after the click as a 409.
 */

import * as React from "react"
import { AlertTriangle, RotateCcw } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { FieldLabel } from "@/components/ui/detail"
import type { ReplayAvailability, WorkItemDetail } from "@/hooks/use-work-items"

export interface WorkReplayDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  item: WorkItemDetail
  availability: ReplayAvailability
  pending: boolean
  error?: string | null
  onConfirm: (input: { reason: string; targetRevision: string }) => void
}

export function WorkReplayDialog({
  open, onOpenChange, item, availability, pending, error, onConfirm,
}: WorkReplayDialogProps) {
  const [reason, setReason] = React.useState("")
  const [targetRevision, setTargetRevision] = React.useState(item.target_revision ?? "")

  // Reopening on a different item must not carry the previous item's revision
  // into this one — that is precisely the "explicit, never inherited by
  // accident" rule, applied to the form's own state.
  React.useEffect(() => {
    if (open) {
      setReason("")
      setTargetRevision(item.target_revision ?? "")
    }
  }, [open, item.id, item.target_revision])

  const blocked = availability.state !== "available"

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Replay this work</DialogTitle>
          <DialogDescription>
            This creates NEW work. It does not re-run the original item: a new work id is minted,
            carrying <span className="font-mono">replay_of</span> back to this one and YOUR
            authorization rather than the original caller&apos;s. The original stays exactly as it
            finished.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="rounded-lg border border-hairline bg-surface-subtle/60 px-3 py-2.5 text-[11px]">
            <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
              <span className="text-muted-foreground">Original input</span>
              <span className="font-mono text-foreground/80" data-testid="replay-input-fingerprint">
                {item.input_sha256 ? `sha256:${item.input_sha256.slice(0, 16)}…` : "not recorded"}
              </span>
            </div>
            <p className="mt-1 leading-relaxed text-muted-foreground">
              The accepted input is never returned by the read API — for a chat or assignment
              source it is the conversation itself. The fingerprint is what identifies it.
            </p>
          </div>

          <div>
            <FieldLabel>Target revision</FieldLabel>
            <Input
              value={targetRevision}
              onChange={(e) => setTargetRevision(e.target.value)}
              placeholder={item.target_revision || "inherits the original"}
              className="mt-1.5 h-9 font-mono text-sm coarse:h-12"
              disabled={blocked || pending}
            />
            <p className="mt-1.5 text-[11px] leading-relaxed text-muted-foreground">
              Left alone, the replay runs against the revision the original was accepted against
              ({item.target_revision ? <span className="font-mono">{item.target_revision}</span> : "none recorded"}).
              Changing it is an explicit re-target — it is never resolved to &ldquo;latest&rdquo; for you.
            </p>
          </div>

          <div>
            <FieldLabel>Reason</FieldLabel>
            <Textarea
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder="Why is this being run again?"
              rows={2}
              className="mt-1.5 text-sm"
              disabled={blocked || pending}
            />
            <p className="mt-1.5 text-[11px] leading-relaxed text-muted-foreground">
              Recorded in the ledger beside the new work item.
            </p>
          </div>

          {blocked && (
            <div
              role="status"
              data-testid="replay-blocked-reason"
              className="flex gap-2.5 rounded-lg border border-warn/30 bg-warn/[0.06] px-3 py-2.5 text-[11px] leading-relaxed text-warn"
            >
              <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span>{availability.reason}</span>
            </div>
          )}

          {error && (
            <div className="rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-[11px] text-destructive">
              {error}
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} className="h-9 coarse:h-12">
            Cancel
          </Button>
          {/* No button at all when the payload is gone: §9 forbids offering a
              dead one, and a disabled button beside a sentence saying why is
              the honest form of "this is not available". */}
          <Button
            onClick={() => onConfirm({ reason, targetRevision })}
            disabled={blocked || pending}
            className="h-9 gap-1.5 coarse:h-12"
            data-testid="replay-confirm"
          >
            <RotateCcw className="h-3.5 w-3.5" />
            {pending ? "Creating…" : "Create new work"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** The trigger, so the list and the detail cannot disagree about when a replay
 *  is offered. Disabled with the reason on it, never absent without one. */
export function ReplayButton({
  availability,
  onClick,
  className,
}: {
  availability: ReplayAvailability
  onClick: () => void
  className?: string
}) {
  const blocked = availability.state !== "available"
  return (
    <Button
      variant="outline"
      size="sm"
      onClick={onClick}
      disabled={blocked}
      title={availability.reason || undefined}
      aria-describedby={blocked ? "replay-unavailable-reason" : undefined}
      data-testid="replay-button"
      className={className}
    >
      <RotateCcw className="h-3 w-3" />
      Replay
    </Button>
  )
}

/** The sentence that has to exist wherever a disabled ReplayButton does. */
export function ReplayUnavailableReason({ availability }: { availability: ReplayAvailability }) {
  if (availability.state === "available" || !availability.reason) return null
  return (
    <p
      id="replay-unavailable-reason"
      data-testid="replay-unavailable-reason"
      className="text-[11px] leading-relaxed text-muted-foreground"
    >
      {availability.reason}
    </p>
  )
}
