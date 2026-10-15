"use client"

import { useRef, useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { pagesKeys } from "@/hooks/use-pages"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@/components/ui/dialog"
export function NewBoardDialog({ workspaceId, onClose, onCreated }: { workspaceId: string; onClose: () => void; onCreated: (slug: string) => void }) {
  const [name, setName] = useState("")
  const [slug, setSlug] = useState("")
  const [createdSlug, setCreatedSlug] = useState<string | null>(null)
  const created = useRef<string | null>(null)
  const mutation = useRef(crypto.randomUUID())
  const client = useQueryClient()
  const create = useMutation({
    mutationFn: async () => {
      const params = new URLSearchParams({ workspace_id: workspaceId })
      if (!created.current) {
        const response = await apiFetch(`/api/v1/pages?${params}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: name.trim(), slug, panels: [], allow_empty: true }) })
        const body = await response.json().catch(() => null)
        if (!response.ok) throw new Error(body?.error ?? "Could not create the page.")
        created.current = body?.slug ?? slug; setCreatedSlug(created.current)
      }
      const response = await apiFetch(`/api/v1/pages/${encodeURIComponent(created.current!)}/board?${params}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mutation_id: mutation.current }), signal: AbortSignal.timeout(20000) })
      const body = await response.json().catch(() => null)
      if (!response.ok) throw new Error(body?.error ?? "The page exists, but board initialization could not be confirmed. Retry to finish the same board.")
      return created.current!
    },
    onSuccess: async address => { await client.invalidateQueries({ queryKey: pagesKeys.all(workspaceId) }); onCreated(address); onClose() },
  })
  return <Dialog open onOpenChange={open => { if (!open && !create.isPending) onClose() }}><DialogContent><DialogHeader><DialogTitle>New board</DialogTitle><DialogDescription>Create a board owned by you. Draw shapes and notes, then save changes explicitly. Pinned HTML artifacts remain static.</DialogDescription></DialogHeader>
    <form className="space-y-4" onSubmit={event => { event.preventDefault(); create.mutate() }}>
      <div className="space-y-2"><label htmlFor="board-name" className="text-sm font-medium">Name</label><Input id="board-name" autoFocus required disabled={Boolean(createdSlug)} value={name} onChange={event => { const next = event.target.value; setName(next); if (!slug || slug === name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "")) setSlug(next.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "")) }} /></div>
      <div className="space-y-2"><label htmlFor="board-slug" className="text-sm font-medium">Page address</label><Input id="board-slug" required pattern="[a-z0-9]+(-[a-z0-9]+)*" disabled={Boolean(createdSlug)} value={slug} onChange={event => setSlug(event.target.value)} /></div>
      {createdSlug && <p className="text-sm">Page created at <code>/pages/{createdSlug}</code>. Retry finishes initialization on this same page.</p>}
      {create.error && <p role="alert" className="text-sm text-destructive">{create.error.message}</p>}
      <DialogFooter><Button type="button" variant="ghost" disabled={create.isPending} onClick={onClose}>Cancel</Button>{createdSlug && <Button type="button" variant="outline" disabled={create.isPending} onClick={() => { onCreated(createdSlug); onClose() }}>Open created page</Button>}<Button type="submit" disabled={create.isPending || !name.trim() || !slug}>{create.isPending ? "Creating…" : createdSlug ? "Retry initialization" : "Create board"}</Button></DialogFooter>
    </form></DialogContent></Dialog>
}
