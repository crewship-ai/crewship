"use client"

// EditCredentialDialog — uses the same CredentialForm as Add. Same
// fields, same layout, same keyboard behaviour. The only behavioural
// difference is "leave Value empty to preserve the existing secret".

import * as React from "react"
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from "@/components/ui/sheet"
import { CredentialForm, type CredentialFormValues, type CredentialType } from "./credential-form"

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
}

interface EditCredentialDialogProps {
  workspaceId: string
  credential: CredentialData
  open: boolean
  onOpenChange: (open: boolean) => void
  onSuccess: () => void
}


export function EditCredentialDialog({
  workspaceId, credential, open, onOpenChange, onSuccess,
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
  }), [credential])

  const handleSubmit = async (values: CredentialFormValues) => {
    const body: Record<string, unknown> = {
      name: values.name,
      description: values.description,
      provider: values.provider,
      scope: values.scope,
      tags: values.tags,
    }
    if (values.value) body.value = values.value
    body.crew_ids = values.scope === "CREW" ? values.crewIds : []
    body.token_expires_at = values.expiresAt
      ? new Date(values.expiresAt).toISOString()
      : null

    try {
      const res = await fetch(`/api/v1/credentials/${credential.id}?workspace_id=${workspaceId}`, {
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

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="sm:max-w-[480px] p-0 flex flex-col">
        <SheetHeader className="px-5 pt-4 pb-3 border-b border-white/10">
          <SheetTitle className="text-base font-mono">{credential.name}</SheetTitle>
          <SheetDescription className="text-xs">
            Update metadata or paste a new value to rotate the secret.
          </SheetDescription>
        </SheetHeader>

        <div className="flex-1 overflow-y-auto px-5 py-4">
          <CredentialForm
            workspaceId={workspaceId}
            mode="edit"
            initial={initial}
            onSubmit={handleSubmit}
            onCancel={() => onOpenChange(false)}
            submitLabel="Save changes"
          />
        </div>
      </SheetContent>
    </Sheet>
  )
}
