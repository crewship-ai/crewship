"use client"

import { useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
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
  const [name, setName] = useState("")
  const [slug, setSlug] = useState("")
  const client = useQueryClient()
  const create = useMutation({
    mutationFn: async () => {
      const response = await apiFetch(`/api/v1/pages?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        // Omitting owner uses the authenticated human; no crew delegation.
        body: JSON.stringify({ name: name.trim(), slug, panels: [], allow_empty: true }),
      })
      const body = await response.json().catch(() => null)
      if (!response.ok) throw new Error(body?.error ?? "Could not create the artifact page.")
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
        <DialogTitle>New artifact</DialogTitle>
        <DialogDescription>Create a page owned by you, then upload or paste an HTML report. Static preview is available; scripts stay disabled.</DialogDescription>
      </DialogHeader>
      <form className="space-y-4" onSubmit={event => { event.preventDefault(); create.mutate() }}>
        <div className="space-y-2"><label htmlFor="artifact-name" className="text-sm font-medium">Name</label><Input id="artifact-name" autoFocus required value={name} onChange={event => {
          const next = event.target.value
          setName(next)
          if (!slug || slug === name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "")) setSlug(next.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, ""))
        }} /></div>
        <div className="space-y-2"><label htmlFor="artifact-slug" className="text-sm font-medium">Page address</label><Input id="artifact-slug" required pattern="[a-z0-9]+(-[a-z0-9]+)*" value={slug} onChange={event => setSlug(event.target.value)} /><p className="text-xs text-muted-foreground">Lowercase letters, numbers and hyphens.</p></div>
        {create.error && <p role="alert" className="text-sm text-destructive">{create.error.message}</p>}
        <DialogFooter><Button type="button" variant="ghost" disabled={create.isPending} onClick={onClose}>Cancel</Button><Button type="submit" disabled={create.isPending || !name.trim() || !slug}>{create.isPending ? "Creating…" : "Create artifact page"}</Button></DialogFooter>
      </form>
    </DialogContent>
  </Dialog>
}
