"use client"

// AddSecretSheet — the default Add flow. Single-page Sheet with the
// unified CredentialForm. Replaces the 4-step wizard as the primary
// "+" action; the legacy wizard is kept around for OAuth/setup-token
// flows that still need provider-first selection.

import * as React from "react"
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from "@/components/ui/sheet"
import { CredentialForm } from "./credential-form"

interface AddSecretSheetProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onSuccess: () => void
}

export function AddSecretSheet({ workspaceId, open, onOpenChange, onSuccess }: AddSecretSheetProps) {
  const handleSubmit = async (values: Parameters<NonNullable<React.ComponentProps<typeof CredentialForm>["onSubmit"]>>[0]) => {
    const body: Record<string, unknown> = {
      name: values.name,
      value: values.value,
      type: values.type,
      provider: values.provider,
      scope: values.scope,
      tags: values.tags,
    }
    if (values.description) body.description = values.description
    if (values.expiresAt) body.token_expires_at = new Date(values.expiresAt).toISOString()
    if (values.scope === "CREW") body.crew_ids = values.crewIds

    try {
      const res = await fetch(`/api/v1/credentials?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        return typeof data.error === "string" ? data.error : "Failed to create credential"
      }
      onSuccess()
      onOpenChange(false)
      return null
    } catch {
      return "Network error"
    }
  }

  const handleTest = async (values: Parameters<NonNullable<React.ComponentProps<typeof CredentialForm>["onTest"]>>[0]) => {
    try {
      const res = await fetch(`/api/v1/credentials/test`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          provider: values.provider,
          type: values.type,
          value: values.value,
        }),
      })
      if (!res.ok) return { valid: false, error: "Test request failed" }
      const data = await res.json()
      return { valid: !!data.valid, error: data.error }
    } catch {
      return { valid: false, error: "Network error" }
    }
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="sm:max-w-[480px] p-0 flex flex-col">
        <SheetHeader className="px-5 pt-4 pb-3 border-b border-white/10">
          <SheetTitle className="text-base">Add secret</SheetTitle>
          <SheetDescription className="text-xs">
            Paste any API key, token, or password. The agent reads it from the env var name you choose.
          </SheetDescription>
        </SheetHeader>

        <div className="flex-1 overflow-y-auto px-5 py-4">
          <CredentialForm
            workspaceId={workspaceId}
            mode="create"
            onSubmit={handleSubmit}
            onCancel={() => onOpenChange(false)}
            onTest={handleTest}
            submitLabel="Save secret"
          />
        </div>
      </SheetContent>
    </Sheet>
  )
}
