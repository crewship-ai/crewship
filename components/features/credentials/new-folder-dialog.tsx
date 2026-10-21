"use client"

/**
 * New folder — the Folders section's "+" in the Vault panel. A name, an
 * optional parent (folders nest two levels) and, for owners and admins, the
 * folder's policy: who may reveal its secrets, the lowest Keeper tier a secret
 * in it may hold, and a replace-reminder. Policy is an access decision, so
 * managers only see the name and parent.
 */

import * as React from "react"
import { FolderPlus } from "lucide-react"

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

export function NewFolderDialog({
  open,
  onOpenChange,
  folders,
  canSetPolicy,
  onCreate,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  folders: CredentialFolder[]
  /** Owners and admins: may set the reveal policy and tier floor. */
  canSetPolicy: boolean
  onCreate: (draft: FolderDraft) => Promise<CredentialFolder>
  onCreated?: (folder: CredentialFolder) => void
}) {
  const [name, setName] = React.useState("")
  const [parent, setParent] = React.useState("")
  const [reveal, setReveal] = React.useState<FolderRevealPolicy>("inherit")
  const [minTier, setMinTier] = React.useState(1)
  const [remind, setRemind] = React.useState(0)
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)

  React.useEffect(() => {
    if (!open) { setName(""); setParent(""); setReveal("inherit"); setMinTier(1); setRemind(0); setError(null) }
  }, [open])

  const roots = folders.filter((f) => !f.parent_id)
  const valid = name.trim().length > 0

  const submit = async () => {
    if (!valid || busy) return
    setBusy(true)
    setError(null)
    try {
      const draft: FolderDraft = { name: name.trim(), remind_after_days: remind }
      if (parent) draft.parent_id = parent
      if (canSetPolicy) {
        draft.reveal_policy = reveal
        draft.min_security_level = minTier
      }
      const created = await onCreate(draft)
      onOpenChange(false)
      onCreated?.(created)
    } catch (err) {
      setError(err instanceof Error ? err.message : "Couldn't create the folder.")
    } finally {
      setBusy(false)
    }
  }

  return (
    <CreateSurface open={open} onOpenChange={(o) => { if (!busy) onOpenChange(o) }} size="sm" dirty={Boolean(name)} discardLabel="this folder" onSubmit={() => void submit()}>
      <CreateSurfaceHeader
        icon={FolderPlus}
        context="Vault"
        title="New folder"
        description="Group secrets that belong together. A folder can carry a reveal policy, a tier floor and a replace-reminder."
        onClose={() => { if (!busy) onOpenChange(false) }}
      />
      <CreateSurfaceBody className="space-y-4">
        <CreateSurfaceField label="Name" htmlFor="new-folder-name">
          <Input id="new-folder-name" value={name} maxLength={120} onChange={(e) => setName(e.target.value)} placeholder="Production" />
        </CreateSurfaceField>
        <CreateSurfaceField label="Inside" htmlFor="new-folder-parent">
          <select id="new-folder-parent" value={parent} onChange={(e) => setParent(e.target.value)} className={SELECT}>
            <option value="">Top level</option>
            {roots.map((f) => <option key={f.id} value={f.id}>{f.name}</option>)}
          </select>
        </CreateSurfaceField>
        {canSetPolicy && (
          <>
            <CreateSurfaceField label="Who can reveal values" htmlFor="new-folder-reveal">
              <select id="new-folder-reveal" value={reveal} onChange={(e) => setReveal(e.target.value as FolderRevealPolicy)} className={SELECT}>
                {(Object.keys(FOLDER_REVEAL_LABEL) as FolderRevealPolicy[]).map((k) => <option key={k} value={k}>{FOLDER_REVEAL_LABEL[k]}</option>)}
              </select>
            </CreateSurfaceField>
            <CreateSurfaceField label="Lowest Keeper tier" htmlFor="new-folder-tier">
              <select id="new-folder-tier" value={minTier} onChange={(e) => setMinTier(Number(e.target.value))} className={SELECT}>
                {[1, 2, 3, 4].map((t) => <option key={t} value={t}>L{t}</option>)}
              </select>
            </CreateSurfaceField>
          </>
        )}
        <CreateSurfaceField label="Remind to replace after (days, 0 = off)" htmlFor="new-folder-remind">
          <Input id="new-folder-remind" type="number" min={0} max={3650} value={remind} onChange={(e) => setRemind(Math.max(0, Number(e.target.value) || 0))} />
        </CreateSurfaceField>
      </CreateSurfaceBody>
      <CreateSurfaceRefusal message={error} />
      <CreateSurfaceFooter
        onCancel={() => onOpenChange(false)}
        primaryLabel={busy ? "Creating…" : "Create folder"}
        onPrimary={() => void submit()}
        primaryDisabled={!valid}
        busy={busy}
      />
    </CreateSurface>
  )
}
