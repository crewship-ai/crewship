"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { toast } from "@/lib/toast"

import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { toastSaveError } from "@/components/ui/page-save-bar"
import { SettingsRow } from "@/components/features/settings/shared"
import { WorkspaceMark } from "@/components/layout/workspace-switcher"
import { refreshWorkspaceSettings } from "@/hooks/use-workspace"
import { apiFetch } from "@/lib/api-fetch"

// The same guardrails as the profile picture (#889), mirrored from the server
// so a wrong file is refused before it is sent.
const LOGO_MAX_BYTES = 2 * 1024 * 1024
const LOGO_TYPES = ["image/png", "image/jpeg", "image/webp"]

/**
 * Settings › General › Workspace logo (#3005): the profile picture's twin. An
 * upload or a removal happens at once (it is not a typed-in value, so it does
 * not join the page's Save bar); a server refusal is a corner toast. Below
 * ADMIN the row only shows the logo.
 */
export function WorkspaceLogoRow({ workspaceId, name, logoUrl, canEdit, onChange }: {
  workspaceId: string
  name: string
  logoUrl: string | null | undefined
  canEdit: boolean
  onChange: (logoUrl: string | null) => void
}) {
  const [url, setUrl] = useState<string | null>(logoUrl ?? null)
  useEffect(() => setUrl(logoUrl ?? null), [logoUrl])
  const input = useRef<HTMLInputElement>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const landed = useCallback((next: string | null, message: string) => {
    setUrl(next)
    onChange(next)
    toast.success(message)
    // The switcher draws the logo too; it reads the workspace list.
    void refreshWorkspaceSettings()
  }, [onChange])

  const send = useCallback(async (init: RequestInit, failure: string) => {
    setBusy(true)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${workspaceId}/logo`, init)
      const body = await res.json().catch(() => null)
      if (!res.ok) {
        toastSaveError("Workspace logo", (body?.detail ?? body?.error) || failure)
        return undefined
      }
      return (body?.logo_url ?? null) as string | null
    } catch {
      toastSaveError("Workspace logo", failure)
      return undefined
    } finally {
      setBusy(false)
    }
  }, [workspaceId])

  const onPick = useCallback(async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    // Reset so picking the same file again fires onChange.
    e.target.value = ""
    if (!file) return
    setError(null)
    if (!LOGO_TYPES.includes(file.type)) {
      setError("Must be a PNG, JPEG, or WebP image")
      return
    }
    if (file.size > LOGO_MAX_BYTES) {
      setError("Image must be 2MB or smaller")
      return
    }
    const form = new FormData()
    form.append("file", file)
    // No Content-Type header: the browser sets the multipart boundary.
    const next = await send({ method: "POST", body: form }, "Upload failed.")
    if (next !== undefined) landed(next, "Workspace logo updated")
  }, [send, landed])

  const remove = useCallback(async () => {
    setError(null)
    const next = await send({ method: "DELETE" }, "Could not remove the logo.")
    if (next !== undefined) landed(null, "Workspace logo removed")
  }, [send, landed])

  return (
    // The rule (and a refusal) is the row's hint, under the label: an upload
    // saves at once, so there is nothing for the save bar.
    <SettingsRow
      label="Workspace logo"
      lock={canEdit ? undefined : "Admins and owners"}
      description={!canEdit
        ? "Shown in the workspace switcher and Admin"
        : error
          ? <span role="alert" className="text-destructive">{error}</span>
          : "PNG, JPEG, or WebP · max 2MB · saves at once"}
    >
      <WorkspaceMark name={name} logoUrl={url}
        className="flex h-10 w-10 items-center justify-center rounded-lg bg-primary text-xs font-semibold text-primary-foreground ring-2 ring-border" />
      {canEdit && (
        <>
          <input ref={input} type="file" accept={LOGO_TYPES.join(",")} className="hidden" onChange={onPick} aria-label="Upload workspace logo" />
          <Button size="sm" variant="outline" onClick={() => input.current?.click()} disabled={busy}>
            {busy ? <Spinner className="h-3 w-3" /> : url ? "Change" : "Upload"}
          </Button>
          {url && (
            <Button size="sm" variant="ghost" className="text-muted-foreground hover:text-destructive" onClick={() => void remove()} disabled={busy}>
              Remove
            </Button>
          )}
        </>
      )}
    </SettingsRow>
  )
}
