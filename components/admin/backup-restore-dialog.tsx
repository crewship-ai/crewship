"use client"

import { useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useBackupStore } from "@/stores/backup-store"
import { useRestoreBackup } from "@/hooks/use-backups"

export function BackupRestoreDialog({ workspaceId }: { workspaceId: string | undefined }) {
  const dialog = useBackupStore((s) => s.dialog)
  const selectedPath = useBackupStore((s) => s.selectedPath)
  const close = useBackupStore((s) => s.close)
  const restore = useRestoreBackup(workspaceId)
  const open = dialog === "restore" && Boolean(selectedPath)

  const [passphrase, setPassphrase] = useState("")
  const [asWorkspace, setAsWorkspace] = useState("")
  const [asCrew, setAsCrew] = useState("")
  const [dryRun, setDryRun] = useState(false)

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!selectedPath) return
    try {
      await restore.mutateAsync({
        path: selectedPath,
        passphrase: passphrase || undefined,
        as_workspace: asWorkspace || undefined,
        as_crew: asCrew || undefined,
        dry_run: dryRun,
      })
      toast.success(dryRun ? "Dry-run passed — no writes performed" : "Restore completed")
      close()
      setPassphrase("")
    } catch (err) {
      toast.error((err as Error).message)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && close()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Restore backup</DialogTitle>
          <DialogDescription>
            <span className="font-mono text-xs">{selectedPath}</span>
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="passphrase">
              Passphrase (leave empty for unencrypted bundles)
            </Label>
            <Input
              id="passphrase"
              type="password"
              autoComplete="current-password"
              value={passphrase}
              onChange={(e) => setPassphrase(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="asWorkspace">Rename workspace (optional)</Label>
            <Input
              id="asWorkspace"
              value={asWorkspace}
              onChange={(e) => setAsWorkspace(e.target.value)}
              placeholder="new-slug"
            />
            <p className="text-[11px] text-muted-foreground">
              Regenerates every primary key so the restore does not collide with the
              existing workspace. Docker phase is skipped — run crew provisioning manually
              afterwards.
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="asCrew">Rename crew (optional, crew-scope only)</Label>
            <Input
              id="asCrew"
              value={asCrew}
              onChange={(e) => setAsCrew(e.target.value)}
              placeholder="new-crew-slug"
            />
          </div>
          <label className="flex items-center gap-2 text-xs">
            <input
              type="checkbox"
              checked={dryRun}
              onChange={(e) => setDryRun(e.target.checked)}
            />
            Dry run (validate checksum + compat, no writes)
          </label>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={close}>
              Cancel
            </Button>
            <Button type="submit" disabled={restore.isPending}>
              {restore.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin mr-1" /> : null}
              {dryRun ? "Validate" : "Restore"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
