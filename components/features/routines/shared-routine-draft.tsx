"use client"

import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { z } from "zod"

import { apiFetch } from "@/lib/api-fetch"
import { readApiError } from "@/lib/api-error"
import { Button } from "@/components/ui/button"

const draftSchema = z.object({ draft_id: z.string(), revision: z.number().int().nonnegative(), base_revision: z.number().int().nonnegative(), name: z.string(), description: z.string(), definition: z.record(z.string(), z.unknown()), can_edit: z.boolean(), can_publish: z.boolean().default(false) })
const validationSchema = z.object({ valid: z.literal(true), scope: z.literal("structure_only") })
const publicationSchema = z.object({ id: z.string().min(1), version: z.number().int().positive(), publication_revision: z.number().int().positive() })
type Draft = z.infer<typeof draftSchema>
type PublicationOutcome = { blocked: true; receipt?: z.infer<typeof publicationSchema> }
const publicationKey = (userId: string, url: string) => ["shared-routine-publication", userId, url]
/** The server definitively refused (422); its reason is safe to show and nothing changed. */
class PublicationRefused extends Error {}

export function SharedRoutineDraft({ url, userId }: { url: string; userId: string }) {
  const client = useQueryClient()
  const [open, setOpen] = useState(false)
  const [reloadGeneration, setReloadGeneration] = useState(0)
  const [busy, setBusy] = useState(false)
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
    <Button variant="outline" disabled={busy} onClick={() => setOpen(!open)} aria-expanded={open}>{open ? "Close draft editor" : "Edit shared draft"}</Button>
    {open && <>
      <p className="text-sm text-muted-foreground">This draft is shared with other editors. Saving does not change the published routine. Publish is a separate permission. The structure check inspects the saved definition only; it does not execute a test run.</p>
      {query.isPending && <p role="status">Loading shared draft…</p>}
      {query.isError && <p role="alert">{query.error.message}</p>}
      {query.data === null && <p role="alert">Draft access is no longer available.</p>}
      {query.data && !query.data.can_edit && <p role="alert">This credential cannot edit the draft.</p>}
      {query.data?.can_edit && <DraftForm key={`${url}:${userId}:${query.data.draft_id}:${query.data.revision}:${reloadGeneration}`} draft={query.data} url={url} queryKey={key} onBusy={setBusy} />}
      <Button variant="outline" disabled={query.isFetching || busy} onClick={async () => {
        const result = await query.refetch()
        if (result.isSuccess) {
          client.setQueryData(publicationKey(userId, url), null)
          setReloadGeneration(generation => generation + 1)
        }
      }}>Reload draft and discard local changes</Button>
    </>}
  </section>
}

function DraftForm({ draft, url, queryKey, onBusy }: { draft: Draft; url: string; queryKey: string[]; onBusy: (busy: boolean) => void }) {
  const client = useQueryClient()
  const [name, setName] = useState(draft.name)
  const [description, setDescription] = useState(draft.description)
  const [definition, setDefinition] = useState(JSON.stringify(draft.definition, null, 2))
  const [error, setError] = useState("")
  const [validated, setValidated] = useState(false)
  // Keep the outcome outside the form lifetime. Only an explicit successful
  // reload clears it; closing, remounting or a background GET cannot retry it.
  const outcomeKey = publicationKey(queryKey[1], url)
  const { data: outcome } = useQuery<PublicationOutcome | null>({ queryKey: outcomeKey, queryFn: async () => null, enabled: false, initialData: null, gcTime: Infinity })
  const publicationBlocked = !!outcome?.blocked
  const receipt = outcome?.receipt
  const blockPublication = () => client.setQueryData<PublicationOutcome>(outcomeKey, { blocked: true })
  const dirty = name !== draft.name || description !== draft.description || definition !== JSON.stringify(draft.definition, null, 2)
  const saved = !!draft.draft_id && draft.revision > 0 && draft.base_revision > 0
  function changed() { setValidated(false); if (!publicationBlocked) setError("") }
  const mutation = useMutation({
    retry: false,
    onMutate: () => { onBusy(true); setValidated(false) },
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
    onSettled: () => onBusy(false),
  })
  const publication = useMutation({
    retry: false,
    onMutate: () => { onBusy(true); setError("") },
    mutationFn: async (operation: "validate" | "publish") => {
      if (!saved || dirty || publicationBlocked || receipt || (operation === "publish" && (!draft.can_publish || !validated))) throw new Error("Save and check the exact draft structure before publication.")
      const response = await apiFetch(`${url}/${operation}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ draft_id: draft.draft_id, revision: draft.revision, base_revision: draft.base_revision }) })
      if ([401, 403, 404].includes(response.status)) {
        client.setQueryData(queryKey, null)
        throw new Error("Draft access is no longer available.")
      }
      if (response.status === 409) {
        blockPublication()
        throw new Error("Publication changed. Reload and review the saved draft before trying again. Your changes are retained.")
      }
      // 422 is a definite refusal with nothing written: show the server's reason
      // and keep publication available once the cause is fixed.
      if (response.status === 422) throw new PublicationRefused(await readApiError(response, operation === "validate" ? "The saved draft failed the structure check." : "Publication was refused. Nothing was published."))
      if (!response.ok) throw new Error(operation === "validate" ? "Draft structure could not be checked. Review the saved definition." : "Publication could not be confirmed. Reload and review the published version before trying again.")
      try {
        if (operation === "validate") return validationSchema.parse(await response.json())
        const acknowledged = publicationSchema.parse(await response.json())
        if (acknowledged.publication_revision !== draft.base_revision + 1 || encodeURIComponent(acknowledged.id) !== url.split("/").at(-2)) throw new Error("Mismatched publication receipt")
        return acknowledged
      } catch {
        throw new Error(operation === "validate" ? "Structure check could not be confirmed. Review the saved definition before trying again." : "Publication could not be confirmed. Reload and review the published version before trying again.")
      }
    },
    onSuccess: (result, operation) => {
      if (operation === "validate") { setValidated(true); return }
      setValidated(false)
      client.setQueryData<PublicationOutcome>(outcomeKey, { blocked: true, receipt: publicationSchema.parse(result) })
      void client.invalidateQueries({ queryKey: ["restricted-routine-library", queryKey[1]] })
      void client.invalidateQueries({ queryKey: ["restricted-routine-definition", queryKey[1]] })
    },
    onError: (failure, operation) => { setValidated(false); if (operation === "publish" && !(failure instanceof PublicationRefused)) blockPublication(); setError(failure.message) },
    onSettled: () => onBusy(false),
  })
  const pending = mutation.isPending || publication.isPending
  const blocked = pending || publicationBlocked || !!receipt
  return <form className="space-y-3" onSubmit={event => { event.preventDefault(); if (!blocked) { setError(""); mutation.mutate() } }}>
    <p className="text-xs text-muted-foreground">Draft revision {draft.revision} · Based on publication {draft.base_revision}</p>
    <label className="block space-y-1"><span>Draft name</span><input aria-label="Draft name" className="min-h-10 w-full rounded border bg-background p-2 coarse:min-h-[44px]" maxLength={4096} value={name} disabled={pending || !!receipt} onChange={event => { changed(); setName(event.target.value) }} /></label>
    <label className="block space-y-1"><span>Draft description</span><textarea aria-label="Draft description" className="min-h-24 w-full rounded border bg-background p-2" maxLength={65536} value={description} disabled={pending || !!receipt} onChange={event => { changed(); setDescription(event.target.value) }} /></label>
    <label className="block space-y-1"><span>Draft definition JSON</span><textarea aria-label="Draft definition JSON" className="min-h-64 w-full rounded border bg-background p-2 font-mono text-xs" value={definition} disabled={pending || !!receipt} onChange={event => { changed(); setDefinition(event.target.value) }} /></label>
    {(error || (publicationBlocked && !receipt)) && <p role="alert">{error || "Publication must be reviewed. Reload the saved draft before trying again."}</p>}
    <div className="flex flex-wrap gap-2">
      <Button type="submit" className="coarse:h-[44px]" disabled={blocked}>{mutation.isPending ? "Saving draft…" : "Save shared draft"}</Button>
      <Button type="button" variant="outline" className="coarse:h-[44px]" disabled={blocked || dirty || !saved} onClick={() => publication.mutate("validate")}>Check saved draft structure</Button>
      {draft.can_publish && <Button type="button" className="coarse:h-[44px]" disabled={blocked || dirty || !saved || !validated} onClick={() => publication.mutate("publish")}>Publish reviewed draft</Button>}
    </div>
    {(!saved || dirty) && <p>Save your changes before checking this exact revision.</p>}
    {!draft.can_publish && <p>Editing is permitted. Publication requires a separate current Publish share.</p>}
    {validated && <p role="status">Structural check passed. This is not a test run: nothing was executed, and runtime availability is not guaranteed.</p>}
    {receipt && <p role="status">Published version {receipt.version}. No run was started. Reload the draft before making further changes.</p>}
  </form>
}
