"use client"

import { useState } from "react"
import { useMutation } from "@tanstack/react-query"
import { Frame, Plus } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"

export function NewDesignDialog({ workspaceId, onCreated }: { workspaceId: string; onCreated: (slug: string) => void }) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState("")
  const create = useMutation({
    mutationFn: async () => {
      const slug = `design-${crypto.randomUUID().slice(0, 12)}`
      const response = await apiFetch(`/api/v1/pages/design?${new URLSearchParams({ workspace_id: workspaceId })}`, {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ slug, name: name.trim() }),
      })
      if (!response.ok) {
        const error = await response.json().catch(() => ({}))
        throw new Error(error.error || "Design could not be created")
      }
      return slug
    },
    onSuccess: slug => { setOpen(false); setName(""); onCreated(slug) },
  })
  return <Dialog open={open} onOpenChange={setOpen}>
    <DialogTrigger asChild><Button type="button" className="coarse:min-h-[44px]"><Plus aria-hidden />New artifact</Button></DialogTrigger>
    <DialogContent>
      <form onSubmit={event => { event.preventDefault(); if (name.trim()) create.mutate() }} className="grid gap-4">
        <DialogHeader><DialogTitle>New artifact</DialogTitle><DialogDescription>Create a Design to edit together with people and agents.</DialogDescription></DialogHeader>
        <div className="flex items-center gap-3 rounded-lg border border-primary p-3"><Frame aria-hidden /><div><b>Design</b><p className="text-sm text-muted-foreground">A shared canvas with screens, text, shapes and images</p></div></div>
        <div className="grid gap-2"><Label htmlFor="design-name">Name</Label><Input id="design-name" autoFocus value={name} maxLength={120} onChange={event => setName(event.target.value)} placeholder="Checkout flow" required /></div>
        <p className="text-sm text-muted-foreground">Starts as an empty canvas. Only you and workspace administrators can open it until you grant Page access.</p>
        {create.error && <p role="alert" className="text-sm text-destructive">{create.error.message}</p>}
        <DialogFooter><Button type="submit" disabled={create.isPending || !name.trim()}>{create.isPending ? "Creating…" : "Create Design"}</Button></DialogFooter>
      </form>
    </DialogContent>
  </Dialog>
}
