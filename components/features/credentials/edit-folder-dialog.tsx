"use client"

/**
 * Edit folder — the Folders section's settings action in the Vault panel, for
 * the folder that is open. Name and replace-reminder for anyone who may
 * organise folders; who can reveal and the lowest Keeper tier only for owners
 * and admins (the server refuses them to anyone else). Raising the tier lifts
 * the secrets already inside to it; nothing is ever lowered. Only the fields
 * that changed are sent.
 */

import * as React from "react"
import { FolderCog } from "lucide-react"

import { Input } from "@/components/ui/input"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
} from "@/components/layout/create-surface"
import type { FolderDraft } from "@/hooks/use-credential-folders"
import { FOLDER_REVEAL_LABEL, type CredentialFolder, type FolderRevealPolicy } from "@/lib/credentials/folders"

const SELECT =
  "h-9 w-full rounded-md border border-border bg-background px-2.5 text-control text-foreground outline-none focus:border-primary coarse:min-h-[44px]"

export function EditFolderDialog({
  folder,
  onOpenChange,
  canSetPolicy,
  onSave,
}: {
  /** The folder being edited; null keeps the dialog closed. */
  folder: CredentialFolder | null
  onOpenChange: (open: boolean) => void
  /** Owners and admins: may change who can reveal and the lowest tier. */
  canSetPolicy: boolean
  onSave: (id: string, patch: Partial<FolderDraft>) => Promise<CredentialFolder>
}) {
  const open = folder !== null
  const [name, setName] = React.useState("")
  const [reveal, setReveal] = React.useState<FolderRevealPolicy>("inherit")
  const [minTier, setMinTier] = React.useState(1)
  const [remind, setRemind] = React.useState(0)
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)

  React.useEffect(() => {
    if (!folder) return
    setName(folder.name)
    setReveal(folder.reveal_policy)
    setMinTier(folder.min_security_level)
    setRemind(folder.remind_after_days)
    setError(null)
  }, [folder])

  const patch: Partial<FolderDraft> = {}
  if (folder) {
    if (name.trim() !== folder.name) patch.name = name.trim()
    if (remind !== folder.remind_after_days) patch.remind_after_days = remind
    if (canSetPolicy && reveal !== folder.reveal_policy) patch.reveal_policy = reveal
    if (canSetPolicy && minTier !== folder.min_security_level) {
      patch.min_security_level = minTier
      // The server keeps the default tier at or above the floor.
      if (folder.default_security_level !== null && folder.default_security_level < minTier) patch.default_security_level = minTier
    }
  }
  const dirty = Object.keys(patch).length > 0
  const valid = name.trim().length > 0
  const raising = folder !== null && canSetPolicy && minTier > folder.min_security_level

  const submit = async () => {
    if (!folder || !valid || !dirty || busy) return
    setBusy(true)
    setError(null)
    try {
      await onSave(folder.id, patch)
      onOpenChange(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : "Couldn't save the folder.")
    } finally {
      setBusy(false)
    }
  }

  return (
    <CreateSurface open={open} onOpenChange={(o) => { if (!busy) onOpenChange(o) }} size="sm" dirty={dirty} discardLabel="these changes" onSubmit={() => void submit()}>
      <CreateSurfaceHeader
        icon={FolderCog}
        context="Vault"
        title="Edit folder"
        description="A subfolder is held to the stricter of its own policy and the one above it."
        onClose={() => { if (!busy) onOpenChange(false) }}
      />
      <CreateSurfaceBody className="space-y-4">
        <CreateSurfaceField label="Name" htmlFor="edit-folder-name">
          <Input id="edit-folder-name" value={name} maxLength={120} onChange={(e) => setName(e.target.value)} />
        </CreateSurfaceField>
        {canSetPolicy && (
          <>
            <CreateSurfaceField label="Who can reveal" htmlFor="edit-folder-reveal">
              <select id="edit-folder-reveal" value={reveal} onChange={(e) => setReveal(e.target.value as FolderRevealPolicy)} className={SELECT}>
                {(Object.keys(FOLDER_REVEAL_LABEL) as FolderRevealPolicy[]).map((k) => <option key={k} value={k}>{FOLDER_REVEAL_LABEL[k]}</option>)}
              </select>
            </CreateSurfaceField>
            <CreateSurfaceField label="Lowest allowed tier" htmlFor="edit-folder-tier">
              <select id="edit-folder-tier" value={minTier} onChange={(e) => setMinTier(Number(e.target.value))} className={SELECT}>
                {[1, 2, 3, 4].map((t) => <option key={t} value={t}>L{t}</option>)}
              </select>
            </CreateSurfaceField>
            <p className="text-label text-muted-foreground">
              {raising
                ? `Secrets here and in its subfolders below L${minTier} are raised to L${minTier} when you save.`
                : "Nobody can lower a secret here below it."}
            </p>
          </>
        )}
        <CreateSurfaceField label="Remind to replace values older than (days, 0 = off)" htmlFor="edit-folder-remind">
          <Input id="edit-folder-remind" type="number" min={0} max={3650} value={remind} onChange={(e) => setRemind(Math.max(0, Number(e.target.value) || 0))} />
        </CreateSurfaceField>
      </CreateSurfaceBody>
      <CreateSurfaceRefusal message={error} />
      <CreateSurfaceFooter
        onCancel={() => onOpenChange(false)}
        primaryLabel={busy ? "Saving…" : "Save"}
        onPrimary={() => void submit()}
        primaryDisabled={!valid || !dirty}
        busy={busy}
      />
    </CreateSurface>
  )
}
