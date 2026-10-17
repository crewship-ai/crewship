"use client"

import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { z } from "zod"

import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"

const draftSchema = z.object({ draft_id: z.string(), revision: z.number().int().nonnegative(), base_revision: z.number().int().nonnegative(), name: z.string(), description: z.string(), definition: z.record(z.string(), z.unknown()), can_edit: z.boolean() })
type Draft = z.infer<typeof draftSchema>

export function SharedRoutineDraft({ url, userId }: { url: string; userId: string }) {
  const [open, setOpen] = useState(false)
  const [reloadGeneration, setReloadGeneration] = useState(0)
  const key = ["shared-routine-draft", userId, url]
  const query = useQuery({
    queryKey: key, enabled: open, retry: false, staleTime: Infinity, refetchOnWindowFocus: false,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(url, { signal })
      if ([401, 403, 404].includes(response.status)) return null
      if (!response.ok) throw new Error(response.status === 409 ? "The published version changed. Ask your administrator to review the existing draft." : "Could not refresh the shared draft. Your local changes are retained.")
      return draftSchema.parse(await response.json())
    },
  })
  return <section className="space-y-3 border-t pt-3" aria-label="Shared routine draft">
    <Button variant="outline" onClick={() => setOpen(!open)} aria-expanded={open}>{open ? "Close draft editor" : "Edit shared draft"}</Button>
    {open && <>
      <p className="text-sm text-muted-foreground">This draft is shared with other editors. Saving does not change the published routine. An administrator must review and test it before publication.</p>
      {query.isPending && <p role="status">Loading shared draft…</p>}
      {query.isError && <p role="alert">{query.error.message}</p>}
      {query.data === null && <p role="alert">Draft access is no longer available.</p>}
      {query.data && !query.data.can_edit && <p role="alert">This credential cannot edit the draft.</p>}
      {query.data?.can_edit && <DraftForm key={`${url}:${userId}:${query.data.draft_id}:${query.data.revision}:${reloadGeneration}`} draft={query.data} url={url} queryKey={key} />}
      <Button variant="outline" disabled={query.isFetching} onClick={async () => {
        const result = await query.refetch()
        if (result.isSuccess) setReloadGeneration(generation => generation + 1)
      }}>Reload draft and discard local changes</Button>
    </>}
  </section>
}

function DraftForm({ draft, url, queryKey }: { draft: Draft; url: string; queryKey: string[] }) {
  const client = useQueryClient()
  const [name, setName] = useState(draft.name)
  const [description, setDescription] = useState(draft.description)
  const [definition, setDefinition] = useState(JSON.stringify(draft.definition, null, 2))
  const [error, setError] = useState("")
  const mutation = useMutation({
    retry: false,
    mutationFn: async () => {
      let parsed: unknown
      try { parsed = JSON.parse(definition) } catch { throw new Error("Definition must be valid JSON.") }
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("Definition must be a JSON object.")
      if (!name.trim()) throw new Error("Enter a routine name.")
      const response = await apiFetch(url, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ draft_id: draft.draft_id, revision: draft.revision, base_revision: draft.base_revision, name: name.trim(), description, definition: parsed }) })
      if ([401, 403, 404].includes(response.status)) {
        client.setQueryData(queryKey, null)
        throw new Error("Draft access is no longer available.")
      }
      if (!response.ok) throw new Error(response.status === 409 ? "Another editor or publication changed this draft. Your changes are retained; copy them before reloading." : "Could not save the draft. Your changes are retained.")
      return draftSchema.parse(await response.json())
    },
    onSuccess: saved => { client.setQueryData(queryKey, saved) },
    onError: failure => setError(failure.message),
  })
  return <form className="space-y-3" onSubmit={event => { event.preventDefault(); setError(""); mutation.mutate() }}>
    <p className="text-xs text-muted-foreground">Draft revision {draft.revision} · Based on publication {draft.base_revision}</p>
    <label className="block space-y-1"><span>Draft name</span><input aria-label="Draft name" className="min-h-10 w-full rounded border bg-background p-2 coarse:min-h-[44px]" maxLength={4096} value={name} disabled={mutation.isPending} onChange={event => setName(event.target.value)} /></label>
    <label className="block space-y-1"><span>Draft description</span><textarea aria-label="Draft description" className="min-h-24 w-full rounded border bg-background p-2" maxLength={65536} value={description} disabled={mutation.isPending} onChange={event => setDescription(event.target.value)} /></label>
    <label className="block space-y-1"><span>Draft definition JSON</span><textarea aria-label="Draft definition JSON" className="min-h-64 w-full rounded border bg-background p-2 font-mono text-xs" value={definition} disabled={mutation.isPending} onChange={event => setDefinition(event.target.value)} /></label>
    {error && <p role="alert">{error}</p>}
    <Button type="submit" disabled={mutation.isPending}>{mutation.isPending ? "Saving draft…" : "Save shared draft"}</Button>
  </form>
}
