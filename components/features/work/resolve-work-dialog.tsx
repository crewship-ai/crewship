"use client"

// Settling work whose outcome nobody knows (#3012).
//
// An item in needs_reconciliation holds its agent's only slot: everything the
// agent is sent waits behind it. The Work queue offers the three ways out —
// mark it failed, mark it done, or mark it failed and run its input again —
// and this dialog asks for what the server requires before it records any of
// them: that you checked the runtime has stopped, and why you decided so.

import * as React from "react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Textarea } from "@/components/ui/textarea"
import { useReplayWorkItem, useResolveWorkItem, type WorkItem } from "@/hooks/use-work-items"
import { agentName, workSubject } from "@/lib/work-ledger"

export type ResolveMode = "failed" | "succeeded" | "retry"

const COPY: Record<ResolveMode, { title: string; action: string; body: string }> = {
  failed: {
    title: "Mark this work as failed",
    action: "Mark as failed",
    body: "Records that it did not happen. The agent's queue moves on; nothing is re-run.",
  },
  succeeded: {
    title: "Mark this work as done",
    action: "Mark as done",
    body: "Records that it happened — use this when you checked its effect landed.",
  },
  retry: {
    title: "Mark as failed and run it again",
    action: "Retry",
    body: "Records it as failed, then creates new work with the same input under your name.",
  },
}

export function ResolveWorkDialog({
  workspaceId,
  item,
  mode,
  onOpenChange,
}: {
  workspaceId: string
  item: WorkItem | null
  mode: ResolveMode
  onOpenChange: (open: boolean) => void
}) {
  const [reason, setReason] = React.useState("")
  const [stopped, setStopped] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)
  const resolve = useResolveWorkItem(workspaceId)
  const replay = useReplayWorkItem(workspaceId)
  const open = item != null

  React.useEffect(() => {
    if (open) {
      setReason("")
      setStopped(false)
      setError(null)
    }
  }, [open, item?.id, mode])

  const copy = COPY[mode]
  const pending = resolve.isPending || replay.isPending

  async function submit() {
    if (!item) return
    setError(null)
    try {
      await resolve.mutateAsync({
        workItemId: item.id,
        state: mode === "succeeded" ? "succeeded" : "failed",
        generation: item.generation,
        reason: reason.trim(),
      })
      if (mode === "retry") {
        await replay.mutateAsync({ workItemId: item.id, reason: reason.trim() })
        toast.success("Marked as failed and sent again")
      } else {
        toast.success(mode === "succeeded" ? "Marked as done" : "Marked as failed")
      }
      onOpenChange(false)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{copy.title}</DialogTitle>
          <DialogDescription>
            {item ? (
              <>
                <span className="font-mono">{workSubject(item)}</span> → {agentName(item.agent)}. {copy.body}
              </>
            ) : null}
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <Textarea
            aria-label="What you checked"
            placeholder="What you checked — e.g. the export is not in the ERP"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            rows={3}
          />
          <label className="flex items-start gap-2 text-xs text-muted-foreground">
            <Checkbox checked={stopped} onCheckedChange={(v) => setStopped(v === true)} className="mt-0.5" />
            I checked that nothing is still running for this work.
          </label>
          {error && <p className="text-xs text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button disabled={!stopped || reason.trim() === "" || pending} onClick={() => void submit()}>
            {copy.action}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
