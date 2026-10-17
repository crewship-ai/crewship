"use client"

import { useId, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { z } from "zod"
import { apiFetch } from "@/lib/api-fetch"
import { requireApiPathSegment } from "@/lib/api-path-segment"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger } from "@/components/ui/alert-dialog"

const schema = z.object({ id: z.string(), project_id: z.string(), name: z.string(), description: z.string(), target_date: z.string(), status: z.string(), position: z.number().int(), version: z.string().regex(/^[a-f0-9]{64}$/), can_manage: z.boolean(), can_delete: z.boolean() })
type Milestone = z.infer<typeof schema>
class MilestoneReadError extends Error { constructor(readonly status: number) { super("Milestones could not be refreshed. Check your project access and retry.") } }

export function SharedProjectMilestones({ workspaceId, projectId, userId }: { workspaceId: string; projectId: string; userId: string }) {
  const client = useQueryClient()
  const [offset, setOffset] = useState(0)
  const key = ["restricted-project-milestones", userId, workspaceId, projectId]
  const base = `/api/v1/workspaces/${requireApiPathSegment(workspaceId)}/restricted-projects/${requireApiPathSegment(projectId)}/milestones`
  const query = useQuery({ queryKey: [...key, offset], retry: false, refetchInterval: 15_000,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`${base}?limit=100&offset=${offset}`, { signal })
      if (!response.ok) throw new MilestoneReadError(response.status)
      const items = z.array(schema).parse(await response.json())
      if (items.some(item => item.project_id !== projectId)) throw new Error("Milestone access could not be confirmed.")
      const totalHeader = response.headers.get("X-Total-Count")
      const total = totalHeader !== null && /^\d+$/.test(totalHeader) ? Number(totalHeader) : NaN
      if (!Number.isSafeInteger(total) || total < 0) throw new Error("Milestone pagination could not be confirmed.")
      return { items, total, canManage: response.headers.get("X-Can-Manage") === "true" }
    },
  })
  const lost = query.error instanceof MilestoneReadError && [401, 403, 404].includes(query.error.status)
  const refreshed = () => { void client.invalidateQueries({ queryKey: key }) }
  return <section className="space-y-3 border-t pt-3" aria-label="Shared project milestones">
    <h3 className="font-medium">Milestones</h3>
    <Button variant="outline" onClick={() => void query.refetch()}>Refresh milestones</Button>
    {query.isPending && <p role="status">Loading milestones…</p>}
    {query.isError && <p role="alert">{query.error.message}</p>}
    {!lost && query.data && <>
      {query.data.canManage && <MilestoneForm key={`${userId}:${workspaceId}:${projectId}:new`} base={base} disabled={query.isError} onSaved={refreshed} />}
      {query.data.items.length === 0 && <p className="text-sm text-muted-foreground">No milestones on this page.</p>}
      {query.data.items.map(item => <MilestoneCard key={item.id} item={item} base={base} disabled={query.isError} onSaved={refreshed} />)}
      <div className="flex gap-2"><Button variant="outline" disabled={offset === 0 || query.isError} onClick={() => setOffset(Math.max(0, offset - 100))}>Previous milestones</Button><Button variant="outline" disabled={offset + 100 >= query.data.total || query.isError} onClick={() => setOffset(offset + 100)}>Next milestones</Button></div>
    </>}
  </section>
}

function MilestoneCard({ item, base, disabled, onSaved }: { item: Milestone; base: string; disabled: boolean; onSaved: () => void }) {
  const [open, setOpen] = useState(false)
  const [deletion, setDeletion] = useState<{ version: string; name: string } | null>(null)
  const path = `${base}/${requireApiPathSegment(item.id)}`
  const remove = useMutation({ mutationFn: async () => {
    const response = await apiFetch(path, { method: "DELETE", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ version: deletion?.version }) })
    if (!response.ok) throw new Error(response.status === 409 ? "Milestone changed or has linked work. Refresh before removing it." : "Milestone was not removed. Refresh to check your access.")
  }, onSuccess: () => { setOpen(false); onSaved() }, onError: onSaved })
  const status = ({ active: "Active", completed: "Completed", cancelled: "Cancelled" } as Record<string, string>)[item.status] || item.status
  return <article className="space-y-3 rounded border p-3" aria-label={`Milestone ${item.name}`}>
    <h4 className="font-medium">{item.name}</h4><p className="text-xs text-muted-foreground">{status}{item.target_date && ` · ${item.target_date}`}</p><p className="whitespace-pre-wrap text-sm">{item.description}</p>
    {item.can_manage && <details><summary className="cursor-pointer text-sm">Edit milestone</summary><MilestoneForm item={item} base={base} disabled={disabled || remove.isPending} onSaved={onSaved} /></details>}
    {item.can_delete && <AlertDialog open={open} onOpenChange={next => { if (next) setDeletion({ version: item.version, name: item.name }); setOpen(next) }}><AlertDialogTrigger asChild><Button variant="outline" disabled={disabled || remove.isPending}>Remove milestone</Button></AlertDialogTrigger><AlertDialogContent><AlertDialogHeader><AlertDialogTitle>Remove {deletion?.name || item.name}?</AlertDialogTitle><AlertDialogDescription>This removes the empty milestone. Linked issue work prevents removal.</AlertDialogDescription></AlertDialogHeader><AlertDialogFooter><AlertDialogCancel>Keep milestone</AlertDialogCancel><AlertDialogAction disabled={disabled || remove.isPending || !item.can_delete || deletion?.version !== item.version} onClick={event => { event.preventDefault(); if (!disabled && !remove.isPending && item.can_delete && deletion?.version === item.version) remove.mutate() }}>Remove empty milestone</AlertDialogAction></AlertDialogFooter>{remove.isError && <p role="alert">{remove.error.message}</p>}</AlertDialogContent></AlertDialog>}
  </article>
}

function MilestoneForm({ item, base, disabled, onSaved }: { item?: Milestone; base: string; disabled: boolean; onSaved: () => void }) {
  const id = useId()
  const [name, setName] = useState(item?.name || "")
  const [description, setDescription] = useState(item?.description || "")
  const [target, setTarget] = useState(item?.target_date || "")
  const [status, setStatus] = useState(item?.status || "active")
  const [position, setPosition] = useState(item?.position || 0)
  const [version, setVersion] = useState(item?.version || "")
  const [conflict, setConflict] = useState(false)
  const changed = !!item && (conflict || version !== item.version)
  const save = useMutation({ mutationFn: async () => {
    const path = item ? `${base}/${requireApiPathSegment(item.id)}` : base
    const body = { name, description, target_date: target, status, ...(item ? { position, version } : {}) }
    const response = await apiFetch(path, { method: item ? "PUT" : "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
    if (!response.ok) { if (response.status === 409) { setConflict(true); onSaved() }; throw new Error(response.status === 409 ? "Milestone changed. Your draft is kept; review the current milestone." : "Milestone was not saved. Your draft is kept; refresh the list before retrying.") }
    const saved = schema.parse(await response.json())
    if ((item && (saved.project_id !== item.project_id || saved.id !== item.id)) || !base.endsWith(`/restricted-projects/${requireApiPathSegment(saved.project_id)}/milestones`)) throw new Error("Saved milestone could not be confirmed. Refresh before retrying.")
    return saved
  }, onSuccess: saved => { setName(item ? saved.name : ""); setDescription(item ? saved.description : ""); setTarget(item ? saved.target_date : ""); setStatus(item ? saved.status : "active"); setPosition(saved.position); setVersion(saved.version); setConflict(false); onSaved() } })
  const form = <form className="mt-3 space-y-2" onSubmit={event => { event.preventDefault(); if (!disabled && !changed && name.trim() && !save.isPending) save.mutate() }}>
    <label htmlFor={`${id}-name`} className="block text-sm">Milestone name</label><Input id={`${id}-name`} value={name} maxLength={4096} disabled={save.isPending} onChange={event => setName(event.target.value)} required />
    <label htmlFor={`${id}-description`} className="block text-sm">Milestone description</label><Textarea id={`${id}-description`} value={description} maxLength={65536} disabled={save.isPending} onChange={event => setDescription(event.target.value)} />
    <label htmlFor={`${id}-target`} className="block text-sm">Milestone target date</label><Input id={`${id}-target`} type="date" value={target} disabled={save.isPending} onChange={event => setTarget(event.target.value)} />
    <label htmlFor={`${id}-status`} className="block text-sm">Milestone status</label><select id={`${id}-status`} className="min-h-10 w-full rounded border bg-background p-2 coarse:min-h-[44px]" value={status} disabled={save.isPending} onChange={event => setStatus(event.target.value)}>{!["active", "completed", "cancelled"].includes(status) && <option value={status}>{status}</option>}<option value="active">Active</option><option value="completed">Completed</option><option value="cancelled">Cancelled</option></select>
    {item && <><label htmlFor={`${id}-position`} className="block text-sm">Milestone position</label><Input id={`${id}-position`} type="number" min={0} max={1000000} value={position} disabled={save.isPending} onChange={event => setPosition(Number(event.target.value))} /></>}
    {changed && item && <div role="status"><p className="text-sm">This milestone changed after your draft started.</p><Button type="button" variant="outline" disabled={disabled || save.isPending} onClick={() => { setName(item.name); setDescription(item.description); setTarget(item.target_date); setStatus(item.status); setPosition(item.position); setVersion(item.version); setConflict(false); save.reset() }}>Load current milestone</Button></div>}
    <Button type="submit" disabled={disabled || changed || !name.trim() || save.isPending}>{save.isPending ? "Saving…" : item ? "Save milestone" : "Create milestone"}</Button>
    {save.isError && <p role="alert">{save.error.message}</p>}
  </form>
  return item ? form : <details aria-label="New milestone" className="rounded border p-3"><summary className="cursor-pointer text-sm">New milestone</summary>{form}</details>
}
