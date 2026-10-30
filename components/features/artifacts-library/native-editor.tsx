"use client"

import { useEffect, useState, type ReactNode } from "react"
import { useSessionSafe } from "@/hooks/use-auth"
import { apiFetch } from "@/lib/api-fetch"
import { NativeCraftEditorHost } from "@/components/features/pages/native-craft-editor-host"
import { NativeCraftPreview } from "@/components/features/pages/native-craft-preview"

type State = "loading" | "enabled" | "preview" | "disabled" | "error"

/**
 * A native Word or Sheet opens inside its Artifacts detail: the same
 * single-editor host /pages/craft/editor mounts, with its lease bar (who edits
 * now, ask for the handoff). The server's availability answer decides, exactly
 * as on that route; without a confirmed editor or preview the detail keeps
 * what it showed before (`fallback`), and nothing is opened or written.
 */
export function ArtifactNativeEditor({ workspaceId, pageId, fallback }: { workspaceId: string; pageId: string; fallback: ReactNode }) {
  const session = useSessionSafe()
  const humanId = session.status === "authenticated" ? session.data?.user?.id ?? null : null
  const scope = humanId ? JSON.stringify([humanId, workspaceId, pageId]) : null
  const [availability, setAvailability] = useState<{ scope: string; state: State }>()
  useEffect(() => {
    if (!scope) return
    const abort = new AbortController()
    setAvailability({ scope, state: "loading" })
    const request = `/api/v1/pages/${encodeURIComponent("@" + pageId)}/craft/availability?${new URLSearchParams({ workspace_id: workspaceId })}`
    void apiFetch(request, { signal: abort.signal, cache: "no-store", redirect: "error" }).then(async response => {
      if (!response.ok) throw Error("native_availability_unconfirmed")
      const value: unknown = await response.json()
      const fields = value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {}
      const state: State = fields.enabled === true && fields.can_edit === true ? "enabled" : fields.preview_enabled === true ? "preview" : "disabled"
      if (!abort.signal.aborted) setAvailability({ scope, state })
    }).catch(() => { if (!abort.signal.aborted) setAvailability({ scope, state: "error" }) })
    return () => abort.abort()
  }, [scope, workspaceId, pageId])

  if (!scope || !humanId) return <>{fallback}</>
  // A previous person's or document's answer never authorizes this render.
  const state = availability?.scope === scope ? availability.state : "loading"
  if (state === "loading") return <p role="status" className="rounded-card border p-4 text-sm text-muted-foreground">Připojuji…</p>
  // The host's UI and draft journal are remounted for every person and document.
  if (state === "enabled") return <div data-testid="native-editor" className="flex min-h-[520px] flex-1 flex-col overflow-hidden rounded-card border">
    <NativeCraftEditorHost key={scope} workspaceId={workspaceId} pageId={pageId} humanId={humanId} enabled />
  </div>
  if (state === "preview") return <div className="flex min-h-[520px] flex-1 flex-col overflow-hidden rounded-card border">
    <NativeCraftPreview key={scope} humanId={humanId} workspaceId={workspaceId} pageId={pageId} />
  </div>
  return <>{fallback}</>
}
