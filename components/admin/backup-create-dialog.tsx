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
import { useCreateBackup, type CreateBackupScope } from "@/hooks/use-backups"

export function BackupCreateDialog({ workspaceId }: { workspaceId: string | undefined }) {
  const dialog = useBackupStore((s) => s.dialog)
  const close = useBackupStore((s) => s.close)
  const create = useCreateBackup(workspaceId)
  const open = dialog === "create"

  const [scope, setScope] = useState<CreateBackupScope>("workspace")
  const [crewId, setCrewId] = useState("")
  const [encryption, setEncryption] = useState<"passphrase" | "recipient" | "none">("passphrase")
  const [passphrase, setPassphrase] = useState("")
  const [recipient, setRecipient] = useState("")
  const [outputDir, setOutputDir] = useState("")

  // Centralises sensitive-field cleanup so every close path — Cancel
  // button, dialog overlay click, ESC, success handler — wipes
  // passphrase / recipient. Keeping wipe logic in onSubmit only
  // (previous behaviour) left secrets in state if the user dismissed
  // the dialog mid-edit.
  function resetAndClose() {
    setPassphrase("")
    setRecipient("")
    setCrewId("")
    setOutputDir("")
    setScope("workspace")
    setEncryption("passphrase")
    close()
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    // Trim crewId + recipient so a whitespace-only input fails the
    // required-ness check instead of reaching the server as a padded
    // value. Passphrase stays verbatim — a passphrase the user
    // explicitly typed with leading/trailing spaces must match the
    // same bytes at restore.
    const crewIdTrimmed = crewId.trim()
    const recipientTrimmed = recipient.trim()
    if (scope === "crew" && !crewIdTrimmed) {
      toast.error("Crew ID or slug is required for crew scope")
      return
    }
    if (encryption === "passphrase" && !passphrase) {
      toast.error("Passphrase required")
      return
    }
    if (encryption === "recipient" && !recipientTrimmed.startsWith("age1")) {
      toast.error("Recipient must be an age1… public key")
      return
    }
    try {
      const res = await create.mutateAsync({
        scope,
        crew_id: scope === "crew" ? crewIdTrimmed : undefined,
        passphrase: encryption === "passphrase" ? passphrase : undefined,
        recipient: encryption === "recipient" ? recipientTrimmed : undefined,
        no_encrypt: encryption === "none",
        output_dir: outputDir.trim() || undefined,
      })
      toast.success(`Backup created: ${res.path}`)
      resetAndClose()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to create backup")
    }
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && resetAndClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Create backup</DialogTitle>
          <DialogDescription>
            Produces a <span className="font-mono">.tar.zst</span> bundle under{" "}
            <span className="font-mono">~/.crewship/backups/</span>. Encryption is strongly
            recommended — passphrase or an age1 public key.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label id="backup-scope-label">Scope</Label>
            <div className="flex gap-2" role="radiogroup" aria-labelledby="backup-scope-label">
              {(["workspace", "crew"] as CreateBackupScope[]).map((s) => (
                <Button
                  type="button"
                  key={s}
                  role="radio"
                  aria-checked={scope === s}
                  variant={scope === s ? "default" : "outline"}
                  size="sm"
                  onClick={() => setScope(s)}
                >
                  {s}
                </Button>
              ))}
            </div>
          </div>
          {scope === "crew" && (
            <div className="space-y-2">
              <Label htmlFor="crewId">Crew ID or slug</Label>
              <Input
                id="crewId"
                value={crewId}
                onChange={(e) => setCrewId(e.target.value)}
                placeholder="e.g. backend or cre_abc123"
                required
              />
            </div>
          )}
          <div className="space-y-2">
            <Label id="backup-encryption-label">Encryption</Label>
            <div
              className="flex gap-2 flex-wrap"
              role="radiogroup"
              aria-labelledby="backup-encryption-label"
            >
              {(
                [
                  ["passphrase", "Passphrase"],
                  ["recipient", "age1 recipient"],
                  ["none", "None (not recommended)"],
                ] as const
              ).map(([v, label]) => (
                <Button
                  type="button"
                  key={v}
                  role="radio"
                  aria-checked={encryption === v}
                  variant={encryption === v ? "default" : "outline"}
                  size="sm"
                  onClick={() => setEncryption(v)}
                >
                  {label}
                </Button>
              ))}
            </div>
          </div>
          {encryption === "passphrase" && (
            <div className="space-y-2">
              <Label htmlFor="passphrase">Passphrase</Label>
              <Input
                id="passphrase"
                type="password"
                autoComplete="new-password"
                value={passphrase}
                onChange={(e) => setPassphrase(e.target.value)}
                required
              />
            </div>
          )}
          {encryption === "recipient" && (
            <div className="space-y-2">
              <Label htmlFor="recipient">age1 public key</Label>
              <Input
                id="recipient"
                value={recipient}
                onChange={(e) => setRecipient(e.target.value)}
                placeholder="age1…"
                required
              />
            </div>
          )}
          <div className="space-y-2">
            <Label htmlFor="outputDir">Output directory (optional)</Label>
            <Input
              id="outputDir"
              value={outputDir}
              onChange={(e) => setOutputDir(e.target.value)}
              placeholder="~/.crewship/backups (default)"
            />
          </div>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={resetAndClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin mr-1" /> : null}
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
