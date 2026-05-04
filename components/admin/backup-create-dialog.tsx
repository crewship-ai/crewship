"use client"

import { useState } from "react"
import { Check, ChevronsUpDown, Loader2 } from "lucide-react"
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
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { cn } from "@/lib/utils"
import { useBackupStore } from "@/stores/backup-store"
import {
  useCreateBackup,
  useCrewsForBackup,
  type CreateBackupScope,
} from "@/hooks/use-backups"

export function BackupCreateDialog({ workspaceId }: { workspaceId: string | undefined }) {
  const dialog = useBackupStore((s) => s.dialog)
  const close = useBackupStore((s) => s.close)
  const create = useCreateBackup(workspaceId)
  const open = dialog === "create"

  // Lazy-fetch the crew list only while the dialog is open AND scope=crew.
  // Skips the network call for the common workspace-scope case where the
  // picker is not even rendered.
  const crewsQuery = useCrewsForBackup(open ? workspaceId : undefined)

  const [scope, setScope] = useState<CreateBackupScope>("workspace")
  const [crewId, setCrewId] = useState("")
  const [crewPickerOpen, setCrewPickerOpen] = useState(false)
  const [encryption, setEncryption] = useState<"passphrase" | "recipient" | "none">("passphrase")
  const [passphrase, setPassphrase] = useState("")
  const [recipient, setRecipient] = useState("")
  const [outputDir, setOutputDir] = useState("")

  // Resolve the picked crew's display info from the cached list. Falls
  // back to the raw id/slug the user pasted in case they bypassed the
  // picker (rare — but the input still accepts a free-form value to
  // preserve the previous "type a slug from memory" workflow).
  const selectedCrew = crewsQuery.data?.find(
    (c) => c.id === crewId || c.slug === crewId,
  )

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
    if (encryption === "passphrase" && !passphrase.trim()) {
      // Reject whitespace-only passphrases up front (a "   " input
      // would otherwise reach the server and surface as a confusing
      // "decryption failed" later). The check is on the trimmed
      // value; we still send the ORIGINAL bytes verbatim so the user
      // gets exactly what they typed at restore time.
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
              <Label htmlFor="crewId">Crew</Label>
              <Popover open={crewPickerOpen} onOpenChange={setCrewPickerOpen}>
                <PopoverTrigger asChild>
                  <Button
                    type="button"
                    variant="outline"
                    role="combobox"
                    aria-expanded={crewPickerOpen}
                    aria-label="Select crew"
                    className="w-full justify-between font-normal"
                    disabled={crewsQuery.isLoading}
                  >
                    {crewsQuery.isLoading ? (
                      <span className="flex items-center gap-2 text-muted-foreground">
                        <Loader2 className="h-3.5 w-3.5 animate-spin" />
                        Loading crews…
                      </span>
                    ) : selectedCrew ? (
                      <span className="flex items-center gap-2 min-w-0">
                        <span className="truncate">{selectedCrew.name}</span>
                        <span className="font-mono text-xs text-muted-foreground truncate">
                          {selectedCrew.slug}
                        </span>
                      </span>
                    ) : crewId ? (
                      // Free-form value (paste or fallback) — show as-is so
                      // the user can still confirm what's about to be sent.
                      <span className="font-mono text-xs">{crewId}</span>
                    ) : (
                      <span className="text-muted-foreground">Pick a crew…</span>
                    )}
                    <ChevronsUpDown className="ml-2 h-3.5 w-3.5 shrink-0 opacity-50" />
                  </Button>
                </PopoverTrigger>
                <PopoverContent className="p-0 w-[--radix-popover-trigger-width]" align="start">
                  <Command>
                    <CommandInput placeholder="Search crews by name or slug…" />
                    <CommandList>
                      <CommandEmpty>
                        {crewsQuery.isError
                          ? "Failed to load crews"
                          : "No crews match"}
                      </CommandEmpty>
                      <CommandGroup>
                        {(crewsQuery.data ?? []).map((c) => (
                          <CommandItem
                            key={c.id}
                            // value drives cmdk's fuzzy match — concat
                            // name+slug+id so all three are searchable.
                            value={`${c.name} ${c.slug} ${c.id}`}
                            onSelect={() => {
                              setCrewId(c.id)
                              setCrewPickerOpen(false)
                            }}
                            className="flex items-center gap-2"
                          >
                            <Check
                              className={cn(
                                "h-3.5 w-3.5",
                                c.id === crewId || c.slug === crewId
                                  ? "opacity-100"
                                  : "opacity-0",
                              )}
                            />
                            <span className="flex-1 truncate">{c.name}</span>
                            <span className="font-mono text-xs text-muted-foreground">
                              {c.slug}
                            </span>
                          </CommandItem>
                        ))}
                      </CommandGroup>
                    </CommandList>
                  </Command>
                </PopoverContent>
              </Popover>
              <p className="text-xs text-muted-foreground">
                {(crewsQuery.data?.length ?? 0)} crews available
              </p>
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
              placeholder="/var/backups/crewship (absolute path; default ~/.crewship/backups)"
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
