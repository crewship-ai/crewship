"use client"

/**
 * Replace value (Harbor 2, S dialog). The person creates the new key at the
 * service and pastes it here; the old value keeps working for the grace
 * period they pick (No grace · 1 hour · 24 hours · 7 days — the server allows
 * 0 to 7 days, default 24 h), so running work does not break.
 *
 * `mode="patch"` is for a role that may edit the credential but not rotate it:
 * the value is replaced at once through PATCH, with no grace period.
 * The draft never leaves this component except in the request body.
 */

import * as React from "react"
import { Eye, EyeOff, RefreshCw } from "lucide-react"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceChoice,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import { Textarea } from "@/components/ui/textarea"
import { credentialEditPresentation } from "@/lib/credentials/edit-presentation"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"

export const GRACE_OPTIONS = [
  { value: "0", label: "No grace", seconds: 0 },
  { value: "3600", label: "1 hour", seconds: 3600 },
  { value: "86400", label: "24 hours", seconds: 86400 },
  { value: "604800", label: "7 days", seconds: 604800 },
] as const
export const DEFAULT_GRACE_SECONDS = 86400

export interface RotationDialogProps {
  workspaceId: string
  credentialId: string
  credentialName: string
  credentialType?: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onRotated: () => void
  mode?: "rotate" | "patch"
}

export function RotationDialog(props: RotationDialogProps) {
  return props.open ? <RotationDialogSession key={JSON.stringify([props.workspaceId, props.credentialId])} {...props} /> : null
}

function RotationDialogSession({
  workspaceId, credentialId, credentialName, credentialType = "SECRET", open, onOpenChange, onRotated, mode = "rotate",
}: RotationDialogProps) {
  const presentation = credentialEditPresentation(credentialType)
  const [value, setValue] = React.useState("")
  const [grace, setGrace] = React.useState<string>(String(DEFAULT_GRACE_SECONDS))
  const [showValue, setShowValue] = React.useState(false)
  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)
  const active = React.useRef(true)
  const pending = React.useRef(false)

  React.useEffect(() => {
    active.current = true
    return () => { active.current = false }
  }, [])

  const replace = async () => {
    if (!active.current || !value.trim() || pending.current) return
    pending.current = true
    setSubmitting(true)
    setError(null)
    try {
      const ws = encodeURIComponent(workspaceId)
      const id = encodeURIComponent(credentialId)
      const res = mode === "patch"
        ? await apiFetch(`/api/v1/credentials/${id}?workspace_id=${ws}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ value }),
        })
        : await apiFetch(`/api/v1/credentials/${id}/rotate?workspace_id=${ws}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ value, grace_seconds: Number(grace) }),
        })
      if (!active.current) return
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        if (!active.current) return
        setError(typeof data?.error === "string" ? data.error : "Could not replace the value")
        return
      }
      setValue("")
      onRotated()
      onOpenChange(false)
    } catch {
      if (active.current) setError("Network error")
    } finally {
      pending.current = false
      if (active.current) setSubmitting(false)
    }
  }

  const close = (next: boolean) => { if (!submitting) onOpenChange(next) }
  return (
    <CreateSurface open={open} onOpenChange={close} dirty={Boolean(value)} discardLabel="this replacement" size="sm" onSubmit={replace}>
      <CreateSurfaceHeader icon={RefreshCw} concept="credentials" context={credentialName} title="Replace value"
        description="Create the new key at the service first, then paste it here."
        onClose={() => close(false)} />
      <CreateSurfaceBody>
        <CreateSurfaceField label="New value" htmlFor="replacement-value" hint={presentation.hint}>
          <div className="relative">
            {presentation.multiline ? <Textarea id="replacement-value" autoFocus rows={6}
              value={value} onChange={(e) => setValue(e.target.value)} autoComplete="off" spellCheck={false}
              placeholder="Paste the new value…" disabled={submitting} aria-invalid={error ? true : undefined}
              className={`pr-12 font-mono ${showValue ? "" : "[-webkit-text-security:disc]"}`} /> : <Input id="replacement-value" autoFocus type={showValue ? "text" : "password"}
              value={value} onChange={(e) => setValue(e.target.value)} autoComplete="off" aria-invalid={error ? true : undefined}
              placeholder="Paste the new value…" className="pr-12 font-mono" disabled={submitting} />}
            <Button type="button" size="icon" variant="ghost" className="absolute right-0 top-0 h-full"
              aria-label={showValue ? "Hide new value" : "Show new value"} onClick={() => setShowValue((v) => !v)}>
              {showValue ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
            </Button>
          </div>
        </CreateSurfaceField>
        {error && <p role="alert" className="mt-2 text-label text-destructive">{error}</p>}
        {mode === "rotate" ? (
          <div className="mt-4">
            <CreateSurfaceField label="Old value keeps working for">
              <CreateSurfaceChoice ariaLabel="Old value keeps working for" value={grace} onChange={setGrace}
                options={GRACE_OPTIONS.map((o) => ({ value: o.value, label: o.label }))} />
            </CreateSurfaceField>
            <p className="mt-2 text-label text-muted-foreground">
              Running work finishes with the old value; new runs get the new one. You can end the grace early. Revoke the old key at the service afterwards.
            </p>
          </div>
        ) : (
          <p className="mt-3 text-label text-muted-foreground">The stored value is replaced at once; there is no grace period.</p>
        )}
        <p className="mt-2 text-label text-muted-foreground">The replacement has not been tested. Crewship does not change or revoke anything at the service.</p>
      </CreateSurfaceBody>
      <CreateSurfaceFooter onCancel={() => close(false)} primaryLabel="Replace" onPrimary={replace}
        primaryDisabled={!value.trim()} busy={submitting} />
    </CreateSurface>
  )
}
