"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { canonicalSlideDeck, type SlideDeck } from "@/lib/slides/model"
import { planSlidesMutation, validSlidesState, type SlidesAttempt, type SlidesReceipt, type SlidesState } from "@/lib/slides/mutations"

export class SlidesAPIError extends Error { constructor(message: string, public status: number) { super(message) } }
export async function slidesRequest<T>(workspace: string, slug: string, path = "", body?: unknown): Promise<T> {
  const response = await apiFetch(`/api/v1/pages/${encodeURIComponent(slug)}/slides${path}?workspace_id=${encodeURIComponent(workspace)}`, body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
  const value = await response.json().catch(() => null)
  if (!response.ok) throw new SlidesAPIError(value?.error ?? `Slides request failed (${response.status}).`, response.status)
  return value as T
}
export function isOrderOnly(base: SlideDeck, draft: SlideDeck) { return JSON.stringify(base.slide_order) !== JSON.stringify(draft.slide_order) && canonicalSlideDeck({ ...draft, slide_order: base.slide_order }) === canonicalSlideDeck(base) }
export function usePageSlides(workspace: string, slug: string, pageID: string) {
  const [base, setBase] = useState<SlidesState>(), [draft, setDraft] = useState<SlideDeck>(), [canWrite, setCanWrite] = useState(false)
  const [busy, setBusy] = useState(false), [blocked, setBlocked] = useState(false), [error, setError] = useState<string>(), [status, setStatus] = useState("Loading Slides…")
  const attempt = useRef<SlidesAttempt | undefined>(undefined), live = useRef(true), generation = useRef(0)
  const dirty = Boolean(base && draft && canonicalSlideDeck(base.deck) !== canonicalSlideDeck(draft))
  const orderOnly = Boolean(base && draft && isOrderOnly(base.deck, draft))
  const read = useCallback(async (path = "") => {
    const value = await slidesRequest<unknown>(workspace, slug, path)
    if (!validSlidesState(value) || value.deck.deck_id !== pageID) throw new Error("Page identity or Slides format changed. Your draft is retained.")
    return value
  }, [workspace, slug, pageID])
  const reopen = useCallback(async () => {
    const requestGeneration = ++generation.current
    const current = () => live.current && generation.current === requestGeneration
    setBusy(true); setError(undefined)
    try {
      // Separate reads are advisory; every write still checks current authority
      // and the original immutable Page/revision under the durable writer fence.
      const [state, response] = await Promise.all([read(), apiFetch(`/api/v1/pages/${encodeURIComponent(slug)}?workspace_id=${encodeURIComponent(workspace)}`)])
      const page = await response.json()
      if (!response.ok || page.id !== pageID) throw new Error("Page access or identity changed. Your draft is retained.")
      if (!current()) return
      setCanWrite(page.can_edit_slides === true); setBase(state); setDraft(structuredClone(state.deck)); attempt.current = undefined; setBlocked(false); setStatus(`Saved revision ${state.revision}`)
    } catch (cause) { if (current()) { setCanWrite(false); setError(cause instanceof Error ? cause.message : "Could not read Slides."); setStatus("Slides unavailable; any draft is retained") } }
    finally { if (current()) setBusy(false) }
  }, [read, workspace, slug, pageID])
  // A slug is a mutable lookup alias. Same-identity route changes only update
  // the endpoint closures; they must never reopen over a retained candidate.
  const reopenLatest = useRef(reopen)
  reopenLatest.current = reopen
  const invalidate = useCallback(() => { live.current = false; ++generation.current }, [])
  useEffect(() => { live.current = true; void reopenLatest.current(); return invalidate }, [workspace, pageID, invalidate])
  const change = (next: SlideDeck) => {
    if (!base || !draft || !canWrite || busy || blocked || attempt.current) return
    // Removing an unsaved first image also undoes its uncommitted upgrade.
    if (base.schema_version === 1 && next.schema_version === 2 && !next.slides.some(slide => slide.elements.some(element => element.type === "image"))) next = { ...next, schema_version: 1 }
    if (orderOnly) { setError("Save or discard the pending reorder before editing content."); return }
    if (dirty && isOrderOnly(draft, next)) { setError("Save or discard content changes before reordering slides."); return }
    setDraft(next); setError(undefined); setStatus("Unsaved changes")
  }
  async function send(proposed?: SlidesAttempt) {
    if (!base || !draft || !canWrite || busy || blocked) return
    const requestGeneration = ++generation.current
    const current = () => live.current && generation.current === requestGeneration
    setBusy(true); setError(undefined)
    let acknowledged: SlidesReceipt | undefined
    try {
      const request = attempt.current ?? proposed ?? await planSlidesMutation(base, draft, crypto.randomUUID())
      if (!current()) return
      attempt.current = structuredClone(request); setStatus("Saving…")
      const receipt = await slidesRequest<SlidesReceipt>(workspace, slug, request.path, request.body)
      if (receipt.mutation_id !== request.body.mutation_id || receipt.revision !== request.body.base_rev + 1 || !Number.isSafeInteger(receipt.revision) || !Number.isSafeInteger(receipt.order_version) || receipt.order_version < 0 || typeof receipt.content_hash !== "string" || !/^[a-f0-9]{64}$/.test(receipt.content_hash) || typeof receipt.duplicate !== "boolean") throw new Error("Invalid save acknowledgement. Retry the original request.")
      if (!current()) return
      acknowledged = receipt
      setStatus(`Saved revision ${receipt.revision}; verifying saved content…`)
      const confirmed = await read(`/revisions/${receipt.revision}`)
      if (confirmed.content_hash !== receipt.content_hash || confirmed.order_version !== receipt.order_version) throw new Error("Saved revision verification failed. Retry the original request.")
      if (!current()) return
      // An exact retry may acknowledge an older revision. Never relabel it as
      // the latest head or replace it with a silently rebased draft.
      setBase(confirmed); setDraft(structuredClone(confirmed.deck)); attempt.current = undefined
      setStatus(`Saved revision ${receipt.revision}${receipt.duplicate ? " (original request confirmed)" : ""}. Reopen saved Slides to check for newer changes.`)
    } catch (cause) {
      if (!current()) return
      if (cause instanceof SlidesAPIError && [401, 403, 404, 409].includes(cause.status)) { setBlocked(true); if (cause.status !== 409) setCanWrite(false); setStatus(cause.status === 409 ? "Conflict. Your unsaved draft and original preconditions are retained." : "Access or Page identity changed. Your unsaved draft is retained.") }
      else if (cause instanceof SlidesAPIError && cause.status >= 400 && cause.status < 500 && ![408, 429].includes(cause.status)) { setBlocked(true); setStatus("Change rejected. Your draft is retained.") }
      else setStatus(attempt.current ? "Save outcome uncertain. Retry uses the exact original request." : "Unsaved changes")
      if (acknowledged) setStatus(`Saved revision ${acknowledged.revision}; saved content could not be loaded. Your copy is retained; retry confirms the original request.`)
      setError(cause instanceof Error ? cause.message : "Could not save Slides.")
    } finally { if (current()) setBusy(false) }
  }
  const restore = (revision: number) => base && !dirty && !attempt.current ? send({ path: "/restore", body: { page_id: base.deck.deck_id, schema_version: base.schema_version, base_rev: base.revision, if_order_version: base.order_version, mutation_id: crypto.randomUUID(), revision } }) : Promise.resolve()
  return { base, draft, canWrite, busy, blocked, dirty, orderOnly, status, error, change, save: () => send(), restore, reopen, retry: Boolean(attempt.current), read }
}
