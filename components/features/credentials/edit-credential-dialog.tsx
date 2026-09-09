"use client"

// Metadata-first editing in the shared Routines create/edit shell.
// Secret replacement is explicit; an empty replacement never reaches PATCH.

import * as React from "react"
import { CreateSurface, CreateSurfaceHeader } from "@/components/layout/create-surface"
import { CredentialForm, type CredentialFormValues, type CredentialType } from "./credential-form"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "sonner"
import { CredentialExtraFieldsEditor, type EditableCredentialField } from "./credential-extra-fields-editor"

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
  isProviderLogin?: boolean
  username?: string | null
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
  const [dirty, setDirty] = React.useState(false)
  const [fieldsDirty, setFieldsDirty] = React.useState(false)
  const [fields, setFields] = React.useState<EditableCredentialField[]>([])
  const [fieldsError, setFieldsError] = React.useState(false)
  const [fieldsVersion, setFieldsVersion] = React.useState(0)
  React.useEffect(() => { setFields([]); setFieldsDirty(false) }, [open, credential.id])
  React.useEffect(() => {
    if (!open || credential.isProviderLogin) return
    let cancelled = false
    setFieldsError(false)
    apiFetch(`/api/v1/credentials/${encodeURIComponent(credential.id)}/fields?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then(async (r) => { if (!r.ok) throw new Error(); const rows = await r.json(); if (!Array.isArray(rows)) throw new Error(); return rows })
      .then((rows) => { if (!cancelled) setFields(rows) })
      .catch(() => { if (!cancelled) setFieldsError(true) })
    return () => { cancelled = true }
  }, [open, credential.id, credential.isProviderLogin, workspaceId, fieldsVersion])
  React.useEffect(() => { if (!open) setDirty(false) }, [open])
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
    username: credential.username ?? "",
  }), [credential])

  const handleSubmit = async (values: CredentialFormValues) => {
    if (fieldsDirty) return "Save or cancel your Additional fields changes first. Those fields are saved separately."
    const body: Record<string, unknown> = {
      name: values.name,
      description: values.description,
      scope: values.scope,
      tags: values.tags,
    }
    if (!credential.isProviderLogin) body.provider = values.provider
    if (credential.type === "USERPASS" && values.username !== initial.username) body.username = values.username?.trim()
    // Older API responses may omit the tier. A metadata-only save must not
    // turn an unknown tier into L1 just because the form needs a display default.
    if (!credential.isProviderLogin && (credential.security_level != null || values.securityLevel !== 1)) {
      body.security_level = values.securityLevel
    }
    if (values.value && !credential.isProviderLogin) body.value = values.value
    body.crew_ids = values.scope === "CREW" ? values.crewIds : []
    // Preserve the full token timestamp when its date was not edited.
    if (!credential.isProviderLogin && values.expiresAt !== initial.expiresAt) {
      body.token_expires_at = values.expiresAt ? new Date(values.expiresAt).toISOString() : null
    }

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
      toast.success("Changes saved")
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
    <CreateSurface open={open} onOpenChange={onOpenChange} dirty={dirty || fieldsDirty} discardLabel="these credential changes" size="md">
        <CreateSurfaceHeader concept="credentials" context={credential.name}
          title={credential.isProviderLogin ? "Edit provider" : "Edit credential"}
          description={credential.isProviderLogin ? "Update the name, tags and access. To change the connected account, use Re-login in its detail." : "Update its details and access. Your existing secret stays unchanged unless you enter a replacement."}
          onClose={() => onOpenChange(false)} />
        {open && (
          <CredentialForm
            key={credential.id}
            surface
            hideValue={credential.isProviderLogin}
            lockProvider={credential.isProviderLogin}
            fieldKeys={fields.map((f) => f.key)}
            additionalFields={!credential.isProviderLogin && <>
              {fieldsError && <p role="alert" className="text-xs text-warn">Additional fields could not be refreshed. Previously loaded fields may be out of date.</p>}
              <CredentialExtraFieldsEditor fields={fields} credentialId={credential.id} workspaceId={workspaceId} onDirtyChange={setFieldsDirty} onSaved={() => { setFieldsVersion((v) => v + 1); onSuccess() }} />
            </>}
            onDirtyChange={setDirty}
            workspaceId={workspaceId}
            mode="edit"
            initial={initial}
            onSubmit={handleSubmit}
            onCancel={() => onOpenChange(false)}
            submitLabel="Save changes"
            knownTags={knownTags}
          />
        )}
    </CreateSurface>
  )
}
