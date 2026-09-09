"use client"

import * as React from "react"
import { Eye, EyeOff, KeyRound } from "lucide-react"
import { CreateSurface, CreateSurfaceHeader, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceSection } from "@/components/layout/create-surface"
import { Textarea } from "@/components/ui/textarea"
import { credentialEditPresentation } from "@/lib/credentials/edit-presentation"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"

export interface RotationDialogProps {
  workspaceId: string
  credentialId: string
  credentialName: string
  credentialType?: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onRotated: () => void
}

// The rotate endpoint preserves the separate credential.rotate capability.
// The customer surface only replaces a supplied value, with no grace overlap.
// Provider-side issuance/revocation and advanced overlap remain backend concerns.
export function RotationDialog({
  workspaceId, credentialId, credentialName, credentialType = "SECRET", open, onOpenChange, onRotated,
}: RotationDialogProps) {
  const presentation = credentialEditPresentation(credentialType)
  const [value, setValue] = React.useState("")
  const [showValue, setShowValue] = React.useState(false)
  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)

  React.useEffect(() => {
    setValue("")
    setShowValue(false)
    setError(null)
  }, [open, credentialId])

  const replace = async () => {
    if (!value.trim() || submitting) return
    setSubmitting(true)
    setError(null)
    try {
      const res = await apiFetch(`/api/v1/credentials/${encodeURIComponent(credentialId)}/rotate?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ value, grace_seconds: 0 }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        setError(typeof data.error === "string" ? data.error : "Could not replace the value")
        return
      }
      setValue("")
      onRotated()
      onOpenChange(false)
    } catch {
      setError("Network error")
    } finally {
      setSubmitting(false)
    }
  }

  const close = (next: boolean) => { if (!submitting) onOpenChange(next) }
  return (
    <CreateSurface open={open} onOpenChange={close} dirty={Boolean(value)} discardLabel="this replacement" size="md" onSubmit={replace}>
      <CreateSurfaceHeader concept="credentials" context={credentialName} title="Replace secret value"
        description="Create or change the value at your provider first, then paste it here. Crewship does not change or revoke it at the provider."
        onClose={() => close(false)} />
      <CreateSurfaceBody>
        <CreateSurfaceSection title="New value" icon={KeyRound} accent="amber">
          <label htmlFor="replacement-value" className="sr-only">New value</label>
          <div className="relative">
            {presentation.multiline ? <Textarea id="replacement-value" autoFocus rows={6}
              value={value} onChange={(e) => setValue(e.target.value)} autoComplete="off" spellCheck={false}
              placeholder="Paste the new value…" disabled={submitting}
              className={`pr-12 font-mono ${showValue ? "" : "[-webkit-text-security:disc]"}`} /> : <Input id="replacement-value" autoFocus type={showValue ? "text" : "password"}
              value={value} onChange={(e) => setValue(e.target.value)} autoComplete="off"
              placeholder="Paste the new value…" className="pr-12 font-mono" disabled={submitting} />}
            <Button type="button" size="icon" variant="ghost" className="absolute right-0 top-0 h-full"
              aria-label={showValue ? "Hide new value" : "Show new value"} onClick={() => setShowValue((v) => !v)}>
              {showValue ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
            </Button>
          </div>
          <p className="mt-2 text-xs text-muted-foreground">{presentation.hint}</p>
          <p className="mt-2 text-xs text-muted-foreground">The replacement has not been tested. Saving replaces the stored value immediately.</p>
        </CreateSurfaceSection>
        {error && <p role="alert" className="mt-4 text-sm text-destructive">{error}</p>}
      </CreateSurfaceBody>
      <CreateSurfaceFooter onCancel={() => close(false)} primaryLabel="Replace value" onPrimary={replace}
        primaryDisabled={!value.trim()} busy={submitting} hint="Only the value stored in Crewship changes." />
    </CreateSurface>
  )
}
