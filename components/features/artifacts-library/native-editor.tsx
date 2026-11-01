"use client"

import { useEffect, useState, type ReactNode } from "react"
import { Button } from "@/components/ui/button"
import { useSessionSafe } from "@/hooks/use-auth"
import { apiFetch } from "@/lib/api-fetch"
import { NativeCraftEditorHost } from "@/components/features/pages/native-craft-editor-host"
import { NativeCraftPreview } from "@/components/features/pages/native-craft-preview"
import { NativeCraftReaderHistory } from "@/components/features/pages/native-craft-version-history"

type State = "loading" | "enabled" | "preview" | "readonly" | "disabled" | "error"

/**
 * A native Word or Sheet opens inside its Artifacts detail: the same
 * single-editor host /pages/craft/editor mounts, with its lease bar (who edits
 * now, ask for the handoff). The server's availability answer decides, exactly
 * as on that route; without a confirmed editor or preview the detail keeps
 * what it showed before (`fallback`) under a notice that says why, and
 * nothing is opened or written. An unconfirmed answer can be asked again.
 */
export function ArtifactNativeEditor({ workspaceId, pageId, fallback }: { workspaceId: string; pageId: string; fallback: ReactNode }) {
  const session = useSessionSafe()
  const humanId = session.status === "authenticated" ? session.data?.user?.id ?? null : null
  const scope = humanId ? JSON.stringify([humanId, workspaceId, pageId]) : null
  const [availability, setAvailability] = useState<{ scope: string; state: State }>()
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    if (!scope) return
    const abort = new AbortController()
    setAvailability({ scope, state: "loading" })
    const request = `/api/v1/pages/${encodeURIComponent("@" + pageId)}/craft/availability?${new URLSearchParams({ workspace_id: workspaceId })}`
    void apiFetch(request, { signal: abort.signal, cache: "no-store", redirect: "error" }).then(async response => {
      if (!response.ok) throw Error("native_availability_unconfirmed")
      const value: unknown = await response.json()
      const fields = value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {}
      const state: State = fields.enabled === true && fields.can_edit === true ? "enabled" : fields.preview_enabled === true ? "preview" : fields.enabled === true ? "readonly" : "disabled"
      if (!abort.signal.aborted) setAvailability({ scope, state })
    }).catch(() => { if (!abort.signal.aborted) setAvailability({ scope, state: "error" }) })
    return () => abort.abort()
  }, [scope, workspaceId, pageId, attempt])

  if (!scope || !humanId) return <>{fallback}</>
  // A previous person's or document's answer never authorizes this render.
  const state = availability?.scope === scope ? availability.state : "loading"
  if (state === "loading") return <p role="status" className="rounded-card border p-4 text-sm text-muted-foreground">Připojuji…</p>
  // The host's UI and draft journal are remounted for every person and document.
  if (state === "enabled") return <div data-testid="native-editor" className="flex min-h-[520px] flex-1 flex-col overflow-hidden rounded-card border">
    <NativeCraftEditorHost key={scope} workspaceId={workspaceId} pageId={pageId} humanId={humanId} enabled />
  </div>
  if (state === "preview") return <div className="flex min-h-[520px] flex-1 flex-col overflow-hidden rounded-card border">
    <NativeCraftReaderHistory key={scope} workspaceId={workspaceId} pageId={pageId} />
    <NativeCraftPreview key={scope} humanId={humanId} workspaceId={workspaceId} pageId={pageId} />
  </div>
  return <>
    {state === "error" ? <p role="status" className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
      Editing could not be confirmed right now.
      <Button type="button" size="sm" variant="outline" onClick={() => setAttempt(n => n + 1)}>Try again</Button>
    </p> : <p role="status" className="text-sm text-muted-foreground">
      {state === "readonly" ? "You can read this document but not edit it." : "Editing is not available here right now."}
    </p>}
    {state === "readonly" && <NativeCraftReaderHistory key={scope} workspaceId={workspaceId} pageId={pageId} />}
    {fallback}
  </>
}
