"use client"

import { useState } from "react"
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

interface ForkDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  missionId: string
  checkpointId: string | null
  checkpointLabel?: string
}

/**
 * "Fork from here" dialog. The POST endpoint isn't shipped yet — we try
 * the fetch anyway and toast "not yet wired" on 404 so the UI is complete
 * today and the backend can fill in later without a UI change.
 */
export function ForkDialog({ open, onOpenChange, missionId, checkpointId, checkpointLabel }: ForkDialogProps) {
  const [label, setLabel] = useState("")
  const [submitting, setSubmitting] = useState(false)

  async function handleConfirm() {
    if (!checkpointId) return
    setSubmitting(true)
    try {
      const res = await fetch(`/api/v1/missions/${encodeURIComponent(missionId)}/fork`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ checkpoint_id: checkpointId, label }),
      })
      if (res.status === 404) {
        toast.info("Not yet wired to backend")
      } else if (!res.ok) {
        toast.error(`Fork failed (${res.status})`)
      } else {
        toast.success("Mission forked")
      }
    } catch {
      toast.error("Fork failed")
    } finally {
      setSubmitting(false)
      setLabel("")
      onOpenChange(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Fork from checkpoint</DialogTitle>
          <DialogDescription>
            Create a new mission branch from{" "}
            <span className="font-mono text-foreground">{checkpointLabel ?? checkpointId?.slice(0, 8)}</span>.
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
          />
        </div>
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
