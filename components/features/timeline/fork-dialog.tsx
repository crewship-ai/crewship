"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { AlertCircle } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { apiFetch } from "@/lib/api-fetch"

interface ForkDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  missionId: string
  checkpointId: string | null
  checkpointLabel?: string
}

/**
 * "Fork from here" dialog.
 *
 * Forking is a CHECKPOINT operation: `POST /api/v1/checkpoints/{id}/fork`
 * (internal/api/router_orchestration.go, handler in cartographer_handler.go)
 * anchors a new mission at the source checkpoint's journal cursor and returns
 * `{new_mission_id, new_checkpoint_id}`. There is no mission-level fork route
 * — this dialog used to post to one, so every fork 404'd, and it read the 404
 * as "backend not shipped yet" and closed with an informational toast. That is
 * why the failure never looked like one.
 *
 * The mission id is not part of the request; it is carried only so the caller
 * can keep its own bookkeeping. The checkpoint the user clicked is the whole
 * input, mirroring `crewship checkpoint fork <id> --label`.
 */
export function ForkDialog({ open, onOpenChange, missionId, checkpointId, checkpointLabel }: ForkDialogProps) {
  const router = useRouter()
  const [label, setLabel] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Reset only when the dialog is opened, so a failed attempt keeps its draft
  // label and its error message on screen for the retry.
  useEffect(() => {
    if (open) {
      setLabel("")
      setError(null)
    }
  }, [open])

  async function handleConfirm() {
    if (!checkpointId || submitting) return
    setSubmitting(true)
    setError(null)
    try {
      const res = await apiFetch(`/api/v1/checkpoints/${encodeURIComponent(checkpointId)}/fork`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ label }),
      })
      if (!res.ok) {
        setError(await forkErrorMessage(res))
        return
      }
      const body = (await res.json().catch(() => null)) as { new_mission_id?: string } | null
      const newMissionId = typeof body?.new_mission_id === "string" ? body.new_mission_id : ""
      toast.success(
        newMissionId ? `Forked into mission ${newMissionId.slice(0, 8)}` : "Mission forked",
      )
      onOpenChange(false)
      // The fork is a different mission; staying on the source timeline hides
      // the thing that was just created.
      if (newMissionId) router.push(`/missions/${encodeURIComponent(newMissionId)}/timeline`)
    } catch {
      setError("Couldn't reach the server. Check your connection and try again.")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Fork from checkpoint</DialogTitle>
          <DialogDescription>
            Create a new mission from{" "}
            <span className="font-mono text-foreground">
              {checkpointLabel ?? checkpointId?.slice(0, 8) ?? "this checkpoint"}
            </span>
            {missionId && (
              <>
                {" "}
                in <span className="font-mono text-foreground">{missionId.slice(0, 8)}</span>
              </>
            )}
            . The source mission is left untouched.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-2">
          <Label htmlFor="fork-label" className="text-xs">
            Label
          </Label>
          <Input
            id="fork-label"
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            placeholder="e.g. alternative-approach"
            className="h-8 text-xs"
            disabled={submitting}
          />
          {!checkpointId && (
            <p className="text-[11px] text-muted-foreground">
              This entry doesn&apos;t reference a checkpoint, so there is nothing to fork from.
            </p>
          )}
        </div>
        {error && (
          <div
            role="alert"
            className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-2.5 py-2 text-[12px] text-destructive"
          >
            <AlertCircle className="h-3.5 w-3.5 mt-px shrink-0" />
            <span>{error}</span>
          </div>
        )}
        <DialogFooter>
          <Button variant="outline" size="sm" onClick={() => onOpenChange(false)} disabled={submitting}>
            Cancel
          </Button>
          <Button size="sm" onClick={handleConfirm} disabled={submitting || !checkpointId}>
            {submitting ? "Forking…" : "Fork"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/**
 * The handler answers with `{"error": "..."}` (replyError). Prefer that text —
 * "checkpoint not found" tells the operator their checkpoint was deleted,
 * where "Fork failed (404)" tells them nothing. The status-only forms below
 * are the fallbacks for a body that isn't ours (proxy error page, empty 502).
 */
async function forkErrorMessage(res: Response): Promise<string> {
  const detail = await res
    .json()
    .then((body: unknown) =>
      body && typeof body === "object" && typeof (body as { error?: unknown }).error === "string"
        ? (body as { error: string }).error
        : "",
    )
    .catch(() => "")
  if (detail) return detail
  if (res.status === 403) return "You don't have permission to fork this checkpoint."
  if (res.status === 404) return "Checkpoint not found — it may have been deleted."
  return `Fork failed (${res.status}).`
}
