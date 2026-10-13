"use client"

import { useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"

interface CreationAuthority {
  run_id: string
  can_authorize: boolean
  authorized: boolean
  remaining: number
  expires_at: string
  revoked: boolean
}
/** The server resolves the live principal; streaming alone grants no authority. */
export function ArtifactCreationControl({ workspaceId, sessionId, isStreaming }: { workspaceId: string | null; sessionId: string; isStreaming: boolean }) {
  const scope = JSON.stringify([workspaceId, sessionId])
  const [snapshot, setSnapshot] = useState<{ scope: string; value: CreationAuthority } | null>(null)
  const [refresh, setRefresh] = useState(0)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const data = snapshot?.scope === scope ? snapshot.value : null
  useEffect(() => {
    if (!workspaceId || !sessionId) return
    const controller = new AbortController()
    const read = async () => {
      try {
        const response = await apiFetch(`/api/v1/chats/${encodeURIComponent(sessionId)}/artifact-creation-authorization?workspace_id=${encodeURIComponent(workspaceId)}`, { signal: controller.signal })
        if (!response.ok) throw new Error("unavailable")
        const value = await response.json() as CreationAuthority
        if (!controller.signal.aborted) setSnapshot({ scope, value })
      } catch { if (!controller.signal.aborted) setSnapshot(null) }
    }
    void read()
    const timer = isStreaming ? setInterval(() => void read(), 5000) : null
    return () => { controller.abort(); if (timer) clearInterval(timer) }
  }, [workspaceId, sessionId, isStreaming, scope, refresh])
  if (!data?.run_id || !data.can_authorize) return null
  const allowed = data.authorized && !data.revoked && data.remaining > 0
  const consumed = data.authorized && !data.revoked && data.remaining === 0
  return <div className="space-y-1 border-b px-4 py-2 md:px-6" aria-label="Artifact creation permission">
    <div className="flex flex-wrap items-center gap-2">
      <Button variant="outline" size="sm" className="coarse:h-12" disabled={pending || consumed} onClick={async () => {
        setPending(true); setError(null)
        try {
          const response = await apiFetch(`/api/v1/artifact-creation-authorizations/${encodeURIComponent(data.run_id)}?workspace_id=${encodeURIComponent(workspaceId!)}`, { method: allowed ? "DELETE" : "POST", headers: { "Content-Type": "application/json" }, ...(allowed ? {} : { body: "{}" }), signal: AbortSignal.timeout(20000) })
          if (!response.ok) { const body = await response.json().catch(() => null); throw new Error(body?.error ?? "Could not update creation permission.") }
        } catch (failure) { setError(failure instanceof Error ? failure.message : "Could not confirm creation permission. Refresh before retrying.") }
        finally { setPending(false); setRefresh(value => value + 1) }
      }}>{pending ? "Updating…" : consumed ? "Artifact creation used" : allowed ? "Revoke artifact creation" : "Allow artifact creation"}</Button>
      <span className="text-xs text-muted-foreground">{allowed ? "This live run may create one private artifact." : consumed ? "This run’s one-artifact allowance has been used." : "Opt in to one private artifact from this live run. Existing pages are not included."}</span>
    </div>
    {allowed && data.expires_at && <p className="text-xs text-muted-foreground">Expires {new Date(data.expires_at).toLocaleString()}.</p>}
    {error && <p role="alert" className="text-xs text-destructive">{error}</p>}
  </div>
}
