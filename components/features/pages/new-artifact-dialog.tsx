"use client"

import { useQueryClient } from "@tanstack/react-query"
import { pagesKeys } from "@/hooks/use-pages"
import { useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "@/components/ui/dialog"
import { usePageCreationAccess } from "@/hooks/use-page-creation-access"

export function NewArtifactDialog({ workspaceId, onClose, onCreated }: {
  workspaceId: string; onClose: () => void; onCreated: (slug: string) => void
}) {
  const qc = useQueryClient()
  const allowed = usePageCreationAccess(workspaceId)
  const [name, setName] = useState("")
  const [slug, setSlug] = useState("")
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  async function create() {
    if (!allowed || busy || !name.trim() || !slug.trim()) return
    setBusy(true); setError(null)
    try {
      const response = await apiFetch(`/api/v1/pages?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ slug: slug.trim(), name: name.trim(), panels: [], allow_empty: true }) })
      const result = await response.json()
      if (!response.ok) throw new Error(typeof result?.error === "string" ? result.error : "Could not create HTML Page")
      void qc.invalidateQueries({ queryKey: pagesKeys.all(workspaceId) })
      onCreated(result.slug); onClose()
    } catch (failure) { setError(failure instanceof Error ? failure.message : "Could not create HTML Page") }
    finally { setBusy(false) }
  }
  return <Dialog open onOpenChange={open => { if (!open && !busy) onClose() }}><DialogContent><DialogHeader><DialogTitle>New HTML artifact</DialogTitle><DialogDescription>Create a private, human-owned Page, then upload or write HTML for its static preview.</DialogDescription></DialogHeader><label htmlFor="artifact-name">Name</label><Input id="artifact-name" value={name} onChange={event => setName(event.target.value)} disabled={busy} /><label htmlFor="artifact-slug">Slug</label><Input id="artifact-slug" value={slug} onChange={event => setSlug(event.target.value)} disabled={busy} />{error && <p role="alert">{error}</p>}{!allowed && <p role="alert">You do not currently have permission to create Pages.</p>}<div className="flex gap-2"><Button className="coarse:min-h-12" onClick={() => void create()} disabled={busy || !allowed || !name.trim() || !slug.trim()}>Create HTML Page</Button><Button className="coarse:min-h-12" variant="outline" onClick={onClose} disabled={busy}>Cancel</Button></div></DialogContent></Dialog>
}
