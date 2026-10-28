"use client"

import { useState } from "react"
import { useMutation } from "@tanstack/react-query"
import { Frame, Plus } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader } from "@/components/layout/create-surface"

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
  const submit = () => { if (name.trim() && !create.isPending) create.mutate() }
  return <>
    <Button type="button" className="coarse:min-h-[44px]" onClick={() => setOpen(true)}><Plus aria-hidden />New artifact</Button>
    <CreateSurface open={open} onOpenChange={setOpen} size="md" restoreOpenerFocus onSubmit={submit}>
      <form onSubmit={event => { event.preventDefault(); submit() }} className="flex min-h-0 flex-1 flex-col">
        <CreateSurfaceHeader title="New artifact" description="Create a Design to edit together with people and agents." onClose={() => setOpen(false)} />
        <CreateSurfaceBody className="grid gap-4">
          <div className="flex items-center gap-3 rounded-lg border border-primary p-3"><Frame aria-hidden /><div><b>Design</b><p className="text-sm text-muted-foreground">A shared canvas with screens, text, shapes and images</p></div></div>
          <div className="grid gap-2"><Label htmlFor="design-name">Name</Label><Input id="design-name" autoFocus value={name} maxLength={120} onChange={event => setName(event.target.value)} placeholder="Checkout flow" required /></div>
          <p className="text-sm text-muted-foreground">Starts as an empty canvas. Only you and workspace administrators can open it until you grant Page access.</p>
          {create.error && <p role="alert" className="text-sm text-destructive">{create.error.message}</p>}
        </CreateSurfaceBody>
        <CreateSurfaceFooter primaryLabel={create.isPending ? "Creating…" : "Create Design"} primaryDisabled={!name.trim()} busy={create.isPending} onPrimary={submit} onCancel={() => setOpen(false)} />
      </form>
    </CreateSurface>
  </>
}
