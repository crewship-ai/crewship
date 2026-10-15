"use client"

import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { pagesKeys } from "@/hooks/use-pages"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@/components/ui/dialog"

export function NewArtifactDialog({ workspaceId, onClose, onCreated }: {
  workspaceId: string
  onClose: () => void
  onCreated: (slug: string) => void
}) {
  const [initializeID] = useState(() => crypto.randomUUID())
  const [createdSlug, setCreatedSlug] = useState<string>()
  const [kind, setKind] = useState("artifact")
  const [name, setName] = useState("")
  const [slug, setSlug] = useState("")
  const client = useQueryClient()
  const documentAvailability = useQuery({ queryKey: ["document-status", workspaceId], queryFn: async () => { const response = await apiFetch(`/api/v1/documents/status?workspace_id=${encodeURIComponent(workspaceId)}`); if (!response.ok) return false; const body = await response.json(); return body.enabled === true }, retry: false })
  const create = useMutation({
    mutationFn: async () => {
      if (!createdSlug) {
      const response = await apiFetch(`/api/v1/pages?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        // Omitting owner uses the authenticated human; no crew delegation.
        body: JSON.stringify({ name: name.trim(), slug, panels: [], allow_empty: true }),
      })
      const body = await response.json().catch(() => null)
      if (!response.ok) throw new Error(body?.error ?? "Could not create the artifact page.")
      setCreatedSlug(slug)
      }
      if (kind === "document") {
        const initialized = await apiFetch(`/api/v1/pages/${encodeURIComponent(slug)}/document/initialize?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: initializeID, schema: 2, body: { type: "doc", content: [{ type: "paragraph", attrs: { id: crypto.randomUUID() } }] } }) })
        if (!initialized.ok) throw new Error("Page created, but document initialization failed. Retry creation to initialize it.")
      }
      return slug
    },
    onSuccess: async saved => {
      await client.invalidateQueries({ queryKey: pagesKeys.all(workspaceId) })
      onCreated(saved); onClose()
    },
  })
  return <Dialog open onOpenChange={open => { if (!open && !create.isPending) onClose() }}>
    <DialogContent>
      <DialogHeader>
        <DialogTitle>New page</DialogTitle>
        <DialogDescription>Create a page owned by you. Choose a collaborative Doc or an HTML artifact.</DialogDescription>
      </DialogHeader>
      <form className="space-y-4" onSubmit={event => { event.preventDefault(); create.mutate() }}>
        <div className="space-y-2"><label htmlFor="page-kind" className="text-sm font-medium">Content</label><select id="page-kind" className="w-full rounded-md border bg-background p-2 text-sm" value={kind} disabled={Boolean(createdSlug)} onChange={event => setKind(event.target.value)}><option value="artifact">HTML artifact</option><option value="document" disabled={!documentAvailability.data}>Doc{documentAvailability.data ? "" : " (unavailable)"}</option></select></div>
        <div className="space-y-2"><label htmlFor="artifact-name" className="text-sm font-medium">Name</label><Input id="artifact-name" disabled={Boolean(createdSlug)} autoFocus required value={name} onChange={event => {
          const next = event.target.value
          setName(next)
          if (!slug || slug === name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "")) setSlug(next.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, ""))
        }} /></div>
        <div className="space-y-2"><label htmlFor="artifact-slug" className="text-sm font-medium">Page address</label><Input id="artifact-slug" disabled={Boolean(createdSlug)} required pattern="[a-z0-9]+(-[a-z0-9]+)*" value={slug} onChange={event => setSlug(event.target.value)} /><p className="text-xs text-muted-foreground">Lowercase letters, numbers and hyphens.</p></div>
        {create.error && <p role="alert" className="text-sm text-destructive">{create.error.message}</p>}
        <DialogFooter><Button type="button" variant="ghost" disabled={create.isPending} onClick={onClose}>Cancel</Button><Button type="submit" disabled={create.isPending || !name.trim() || !slug}>{create.isPending ? "Creating…" : kind === "document" ? "Create Doc page" : "Create artifact page"}</Button></DialogFooter>
      </form>
    </DialogContent>
  </Dialog>
}
