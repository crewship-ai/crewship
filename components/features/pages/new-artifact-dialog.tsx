"use client"

import { useRef, useState } from "react"
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
  const createIntent = useRef<{ name: string; slug: string; kind: string } | undefined>(undefined)
  const documentInitialize = useRef<object | undefined>(undefined)
  const [intentFrozen, setIntentFrozen] = useState(false)
  const createdPage = useRef<string | undefined>(undefined)
  const slideInitialize = useRef<object | undefined>(undefined)
  const [createdSlug, setCreatedSlug] = useState<string>()
  const [kind, setKind] = useState("artifact")
  const [name, setName] = useState("")
  const [slug, setSlug] = useState("")
  const client = useQueryClient()
  const documentAvailability = useQuery({ queryKey: ["document-status", workspaceId], queryFn: async () => { const response = await apiFetch(`/api/v1/documents/status?workspace_id=${encodeURIComponent(workspaceId)}`); if (!response.ok) return false; const body = await response.json(); return body.enabled === true }, retry: false })
  const create = useMutation({
    mutationFn: async () => {
      const intent = createIntent.current ??= { name: name.trim(), slug, kind }
      setIntentFrozen(true)
      if (!createdSlug) {
      const response = await apiFetch(`/api/v1/pages?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        // Omitting owner uses the authenticated human; no crew delegation.
        body: JSON.stringify({ name: intent.name, slug: intent.slug, panels: [], allow_empty: true }),
      })
      const body = await response.json().catch(() => null)
      if (!response.ok) {
        if (response.status >= 400 && response.status < 500 && ![408, 429].includes(response.status)) { createIntent.current = undefined; setIntentFrozen(false) }
        throw new Error(body?.error ?? "Could not create the page. No existing Page will be adopted by address.")
      }
      if (intent.kind === "slides" && typeof body?.id !== "string") throw new Error("Page creation did not return its identity. Reopen the Page before initializing content.")
      createdPage.current = body.id
      setCreatedSlug(intent.slug)
      }
      if (intent.kind === "document") {
        documentInitialize.current ??= { id: initializeID, schema: 2, body: { type: "doc", content: [{ type: "paragraph", attrs: { id: crypto.randomUUID() } }] } }
        const initialized = await apiFetch(`/api/v1/pages/${encodeURIComponent(intent.slug)}/document/initialize?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(documentInitialize.current) })
        if (!initialized.ok) throw new Error("Page created, but document initialization failed. Retry creation to initialize it.")
      }
      if (intent.kind === "slides") {
        if (!createdPage.current) throw new Error("The original Page identity is unavailable.")
        if (!slideInitialize.current) {
          const slideID = `s_${crypto.randomUUID().replaceAll("-", "")}`
          slideInitialize.current = { page_id: createdPage.current, schema_version: 1, base_rev: 0, mutation_id: initializeID, deck: { schema_version: 1, deck_id: createdPage.current, width: 1280, height: 720, theme: { background: "#FFFFFF", color: "#112233", font_family: "Arial", font_size: 24 }, slide_order: [slideID], slides: [{ id: slideID, notes: "", elements: [] }] } }
        }
        const initialized = await apiFetch(`/api/v1/pages/${encodeURIComponent(intent.slug)}/slides/initialize?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(slideInitialize.current) })
        if (!initialized.ok) { const value = await initialized.json().catch(() => null); throw new Error(value?.error ?? "Page created, but Slides initialization failed. Retry preserves the original Page and request.") }
      }
      return intent.slug
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
        <DialogDescription>Create a page owned by you. Choose a Doc, native Slides or an HTML artifact.</DialogDescription>
      </DialogHeader>
      <form className="space-y-4" onSubmit={event => { event.preventDefault(); create.mutate() }}>
        <div className="space-y-2"><label htmlFor="page-kind" className="text-sm font-medium">Content</label><select id="page-kind" className="w-full rounded-md border bg-background p-2 text-sm" value={kind} disabled={create.isPending || Boolean(createdSlug) || intentFrozen} onChange={event => setKind(event.target.value)}><option value="artifact">HTML artifact</option><option value="slides">Slides</option><option value="document" disabled={!documentAvailability.data}>Doc{documentAvailability.data ? "" : " (unavailable)"}</option></select></div>
        <div className="space-y-2"><label htmlFor="artifact-name" className="text-sm font-medium">Name</label><Input id="artifact-name" disabled={create.isPending || Boolean(createdSlug) || intentFrozen} autoFocus required value={name} onChange={event => {
          const next = event.target.value
          setName(next)
          if (!slug || slug === name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "")) setSlug(next.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, ""))
        }} /></div>
        <div className="space-y-2"><label htmlFor="artifact-slug" className="text-sm font-medium">Page address</label><Input id="artifact-slug" disabled={create.isPending || Boolean(createdSlug) || intentFrozen} required pattern="[a-z0-9]+(-[a-z0-9]+)*" value={slug} onChange={event => setSlug(event.target.value)} /><p className="text-xs text-muted-foreground">Lowercase letters, numbers and hyphens.</p></div>
        {create.error && <p role="alert" className="text-sm text-destructive">{create.error.message}</p>}
        <DialogFooter><Button type="button" variant="ghost" disabled={create.isPending} onClick={onClose}>Cancel</Button><Button type="submit" disabled={create.isPending || !name.trim() || !slug}>{create.isPending ? "Creating…" : kind === "document" ? "Create Doc page" : kind === "slides" ? "Create Slides page" : "Create artifact page"}</Button></DialogFooter>
      </form>
    </DialogContent>
  </Dialog>
}
