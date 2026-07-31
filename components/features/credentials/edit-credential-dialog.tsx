"use client"

// EditCredentialDialog — uses the same CredentialForm as Add. Same
// fields, same layout, same keyboard behaviour. The only behavioural
// difference is "leave Value empty to preserve the existing secret".

import * as React from "react"
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription,
} from "@/components/ui/dialog"
import { CredentialForm, type CredentialFormValues, type CredentialType } from "./credential-form"
import { apiFetch } from "@/lib/api-fetch"

export interface CredentialData {
  id: string
  name: string
  description: string | null
  type: string
  provider: string
  scope: "WORKSPACE" | "CREW"
  crew_id: string | null
  crew_ids: string[]
  tags?: string[]
  token_expires_at?: string | null
  /** Keeper tier, 1–4. Absent on an older API response → treated as L1, which is
   *  the column's default. */
  security_level?: number
}

interface EditCredentialDialogProps {
  workspaceId: string
  credential: CredentialData
  open: boolean
  onOpenChange: (open: boolean) => void
  onSuccess: () => void
  knownTags?: string[]
}


export function EditCredentialDialog({
  workspaceId, credential, open, onOpenChange, onSuccess, knownTags,
}: EditCredentialDialogProps) {
  const initial = React.useMemo<Partial<CredentialFormValues>>(() => ({
    name: credential.name,
    description: credential.description ?? "",
    type: (credential.type as CredentialType) ?? "API_KEY",
    provider: credential.provider ?? "NONE",
    scope: credential.scope,
    crewIds: credential.crew_ids?.length
      ? credential.crew_ids
      : (credential.crew_id ? [credential.crew_id] : []),
    tags: credential.tags ?? [],
    expiresAt: credential.token_expires_at
      ? credential.token_expires_at.slice(0, 10)
      : "",
    securityLevel: credential.security_level ?? 1,
  }), [credential])

  const handleSubmit = async (values: CredentialFormValues) => {
    const body: Record<string, unknown> = {
      name: values.name,
      description: values.description,
      provider: values.provider,
      scope: values.scope,
      tags: values.tags,
      security_level: values.securityLevel,
    }
    if (values.value) body.value = values.value
    body.crew_ids = values.scope === "CREW" ? values.crewIds : []
    body.token_expires_at = values.expiresAt
      ? new Date(values.expiresAt).toISOString()
      : null

    try {
      const res = await apiFetch(`/api/v1/credentials/${credential.id}?workspace_id=${workspaceId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        return typeof data.error === "string" ? data.error : "Failed to update credential"
      }
      onSuccess()
      onOpenChange(false)
      return null
    } catch {
      return "Network error"
    }
  }

  // Centred like every other create/edit surface. This one was NAMED Dialog
  // and rendered as a side sheet, so Edit slid out from the right even after
  // the detail view stopped doing it — the name hid the inconsistency from
  // anyone grepping for it.
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[560px] max-h-[85vh] p-0 overflow-hidden flex flex-col">
        <DialogHeader className="px-5 pt-4 pb-3 border-b border-white/10">
          <DialogTitle className="text-base font-mono">{credential.name}</DialogTitle>
          <DialogDescription className="text-xs">
            Update metadata or paste a new value to rotate the secret.
          </DialogDescription>
        </DialogHeader>

        <div className="flex-1 overflow-y-auto px-5 py-4">
          <CredentialForm
            workspaceId={workspaceId}
            mode="edit"
            initial={initial}
            onSubmit={handleSubmit}
            onCancel={() => onOpenChange(false)}
            submitLabel="Save changes"
            knownTags={knownTags}
          />
        </div>
      </DialogContent>
    </Dialog>
  )
}
