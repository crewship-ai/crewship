"use client"

import { useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { AlertTriangle, CheckCircle2, Lock, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useBackupStatus, useForceUnlock } from "@/hooks/use-backups"
import { cn } from "@/lib/utils"

interface BackupStatusBannerProps {
  workspaceId: string | undefined
}

const STUCK_THRESHOLD_MS = 60 * 60 * 1000 // 1h — matches DefaultLockTTL

/**
 * Live indicator at the top of the backups tab. Three states:
 *
 *   1. idle  — no lock held, all good (emerald)
 *   2. running — lock held within TTL window, backup actively in flight (amber)
 *   3. stuck — lock held PAST its TTL with no apparent owner (red); offers
 *      a force-unlock affordance behind a type-to-confirm dialog.
 *
 * The 1h stuck threshold is hardcoded because the backend's DefaultLockTTL
 * is 1h; if that ever becomes configurable, the banner should consume it
 * from the status response instead of magic-numbering here.
 */
export function BackupStatusBanner({ workspaceId }: BackupStatusBannerProps) {
  const { data, isLoading } = useBackupStatus(workspaceId)
  const forceUnlock = useForceUnlock(workspaceId)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [confirmText, setConfirmText] = useState("")

  if (isLoading || !data) return null

  // Idle — every common case. Banner stays visible (small + green) so the
  // operator has a positive signal that monitoring is working, rather
  // than wondering why the banner disappeared.
  if (!data.held) {
    return (
      <motion.div
        initial={{ opacity: 0, y: -4 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.16 }}
        className={cn(
          "flex items-center gap-2 px-3 py-2 rounded-md border text-xs",
          "bg-green-500/5 border-green-500/20 text-green-300",
        )}
      >
        <CheckCircle2 className="h-3.5 w-3.5" />
        <span>Idle — no backup in progress</span>
      </motion.div>
    )
  }

  // Detect a stuck lock by comparing acquired_at to the 1h TTL. If
  // acquired_at is missing (older server), fall back to "running" treatment
  // because we can't prove it's stuck.
  const acquiredAt = data.acquired_at ? new Date(data.acquired_at).getTime() : null
  const isStuck =
    acquiredAt !== null && !Number.isNaN(acquiredAt) &&
    Date.now() - acquiredAt > STUCK_THRESHOLD_MS

  async function onConfirmUnlock() {
    if (confirmText !== "force-unlock") {
      toast.error("Type 'force-unlock' exactly to confirm")
      return
    }
    try {
      await forceUnlock.mutateAsync()
      toast.success("Lock released")
      setConfirmOpen(false)
      setConfirmText("")
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to release lock")
    }
  }

  if (isStuck) {
    const heldFor = formatDuration(Date.now() - acquiredAt!)
    return (
      <>
        <motion.div
          role="alert"
          aria-live="assertive"
          initial={{ opacity: 0, y: -4 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.16 }}
          className={cn(
            "flex items-center gap-3 px-3 py-2.5 rounded-md border text-xs",
            "bg-destructive/5 border-destructive/30 text-destructive",
          )}
        >
          <AlertTriangle className="h-4 w-4 shrink-0" />
          <div className="min-w-0 flex-1">
            <div className="font-medium">
              Stuck lock detected — held for {heldFor}
            </div>
            <div className="opacity-80 mt-0.5">
              Lock has exceeded the {Math.round(STUCK_THRESHOLD_MS / 60000)}m TTL and
              no active backup is reporting progress. Safe to force-release if no
              backup is actually running.
            </div>
          </div>
          <Button
            size="sm"
            variant="destructive"
            onClick={() => setConfirmOpen(true)}
            className="shrink-0"
          >
            Force unlock
          </Button>
        </motion.div>

        <Dialog open={confirmOpen} onOpenChange={(v) => { if (!v) { setConfirmOpen(false); setConfirmText("") } }}>
          <DialogContent className="max-w-md">
            <DialogHeader>
              <DialogTitle className="text-destructive">
                Force-release backup lock
              </DialogTitle>
              <DialogDescription>
                Emergency operation. Only use if you've verified no backup process
                is actually running. Releasing the lock during an active backup
                may corrupt the in-progress bundle.
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-3">
              <div className="rounded-md bg-muted/50 border border-border/60 p-3 text-xs space-y-1 font-mono">
                <div>
                  <span className="text-muted-foreground">Holder:</span>{" "}
                  <span>{data.acquired_by || "(unknown)"}</span>
                </div>
                <div>
                  <span className="text-muted-foreground">Acquired:</span>{" "}
                  <span>{data.acquired_at ? new Date(data.acquired_at).toLocaleString() : "—"}</span>
                </div>
                <div>
                  <span className="text-muted-foreground">Held for:</span>{" "}
                  <span>{heldFor}</span>
                </div>
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="force-unlock-confirm" className="text-xs">
                  Type <code className="font-mono bg-muted px-1 rounded">force-unlock</code> to confirm
                </Label>
                <Input
                  id="force-unlock-confirm"
                  value={confirmText}
                  onChange={(e) => setConfirmText(e.target.value)}
                  placeholder="force-unlock"
                  autoComplete="off"
                />
              </div>
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={() => { setConfirmOpen(false); setConfirmText("") }}>
                Cancel
              </Button>
              <Button
                variant="destructive"
                disabled={forceUnlock.isPending || confirmText !== "force-unlock"}
                onClick={onConfirmUnlock}
              >
                {forceUnlock.isPending ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin mr-1" />
                ) : null}
                Force unlock
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </>
    )
  }

  // Within-TTL "running" state. Pulsing dot draws the eye without being
  // alarming.
  return (
    <AnimatePresence>
      <motion.div
        key="running-banner"
        role="status"
        aria-live="polite"
        initial={{ opacity: 0, y: -4 }}
        animate={{ opacity: 1, y: 0 }}
        exit={{ opacity: 0, y: -4 }}
        transition={{ duration: 0.16 }}
        className={cn(
          "flex items-center gap-2 px-3 py-2 rounded-md border text-xs",
          "bg-amber-500/5 border-amber-500/20 text-amber-300",
        )}
      >
        <Lock className="h-3.5 w-3.5" />
        <span>
          Backup in progress — locked by {data.acquired_by || "unknown user"}
          {data.expires_at
            ? ` (expires ${new Date(data.expires_at).toLocaleTimeString()})`
            : ""}
        </span>
      </motion.div>
    </AnimatePresence>
  )
}

function formatDuration(ms: number): string {
  const sec = Math.floor(ms / 1000)
  if (sec < 60) return `${sec}s`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min}m`
  const hr = Math.floor(min / 60)
  const remMin = min % 60
  return remMin > 0 ? `${hr}h ${remMin}m` : `${hr}h`
}
