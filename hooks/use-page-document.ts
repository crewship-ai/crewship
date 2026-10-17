"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import * as Y from "yjs"
import { documentSchema } from "@/lib/documents/schema"
import { apiFetch } from "@/lib/api-fetch"

import { useQueryClient } from "@tanstack/react-query"
import { verifyMutationReceipt, type DocumentState } from "@/lib/documents/contracts"
export type { DocumentState } from "@/lib/documents/contracts"

export const documentKeys = { all: (workspace: string) => ["page-document",workspace] as const, detail: (workspace: string, pageId: string) => ["page-document",workspace,pageId] as const }
class UnconfirmedReceipt extends Error {}
export function decodeUpdate(value: string): Uint8Array { return Uint8Array.from(atob(value), c => c.charCodeAt(0)) }
export function encodeUpdate(value: Uint8Array): string { let text = ""; for (const byte of value) text += String.fromCharCode(byte); return btoa(text) }
export class DocumentAPIError extends Error { constructor(message: string, public status: number, public code?: string) { super(message) } }
export async function documentRequest<T>(workspace: string, slug: string, path = "", body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await apiFetch(`/api/v1/pages/${encodeURIComponent(slug)}/document${path}?workspace_id=${encodeURIComponent(workspace)}`, body === undefined ? {signal} : { signal, method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
  const value = await response.json().catch(() => null)
  if (!response.ok) throw new DocumentAPIError(value?.error ?? `Document request failed (${response.status}).`, response.status, value?.code)
  return value as T
}

/** Acknowledged updates are removed only after the API confirms them. Remote
 * updates merge into the same CRDT, never replace a locally edited JSON body. */
export function usePageDocument(workspace: string, slug: string, pageId: string, allowWrites = true) {
  const client = useQueryClient()
  const writesAllowed = useRef(allowWrites); writesAllowed.current = allowWrites
  const identity = pageId
  const endpoint = useRef(slug); endpoint.current = slug
  const doc = useMemo(() => new Y.Doc({ gc: false, guid: `${workspace}:${identity}` }), [workspace, identity])
  const [confirmed, setConfirmed] = useState<{identity:string;value:DocumentState}>()
  const state = confirmed?.identity === `${workspace}:${identity}` ? confirmed.value : undefined
  const setState = (value: DocumentState) => setConfirmed({identity:`${workspace}:${identity}`,value})
  const [status, setStatus] = useState("Loading document…")
  const [error, setError] = useState<string>()
  const [pending, setPending] = useState(0)
  const [recovery, setRecovery] = useState(false)
  const [denied, setDenied] = useState(false)
  const authorizationLost=useRef(false)
  const deny=useCallback(()=>{authorizationLost.current=true;setDenied(true);setRecovery(pending>0);setStatus("Document access is unavailable.")},[pending])
  useEffect(() => {
    authorizationLost.current=false
    setDenied(false);setRecovery(false);setPending(0);setStatus("Loading document…");setError(undefined)
    let requiredRev = 0
    let active = true, epoch: string | undefined, polling = false, blocked = false
    const queue: { id: string; update: string; attempted: boolean; baselineRev?: number; request?: {id:string;update:string;schema:number;epoch:string;kind:string;page_id?:string} }[] = []
    const local = (update: Uint8Array, origin: unknown) => {
      if (origin === "remote") return
      const last = queue.at(-1)
      if (last && !last.attempted) last.update = encodeUpdate(Y.mergeUpdates([decodeUpdate(last.update), update]))
      else queue.push({ id: crypto.randomUUID(), update: encodeUpdate(update), attempted: false })
      setPending(queue.length); setStatus("Saving…")
    }
    doc.on("update", local)
    function accept(next: DocumentState): boolean {
        if (!active || authorizationLost.current) return false
        if (next.rev < requiredRev) throw new Error("The acknowledged revision is not yet available. Your update is confirmed; refreshing saved content.")
        setPending(queue.length)
        if (!Number.isSafeInteger(next.rev) || next.rev<0 || typeof next.epoch!=="string" || !next.epoch || next.epoch.length>256 || typeof next.snapshot!=="string" || typeof next.can_write!=="boolean" || (next.updates!==undefined && (!Array.isArray(next.updates) || next.updates.some(update=>typeof update!=="string")))) throw new DocumentAPIError("Invalid confirmed document metadata; content preserved.",400)
        if (![1,2,3].includes(next.schema)) { blocked = true; setRecovery(queue.length > 0); setDenied(true); setStatus("This document needs a newer editor."); return false }
        if (next.page_id !== pageId) throw new DocumentAPIError("The original Page identity changed. Recover your draft before reopening.",409)
        let remote: Y.Doc | undefined
        try { const loaded = documentSchema.load(next.snapshot); if (!(loaded instanceof Y.Doc)) throw new Error("Invalid document binary type"); remote=loaded; for (const update of next.updates ?? []) Y.applyUpdate(loaded,decodeUpdate(update)); documentSchema.validate(loaded,next.schema) } catch(cause) { throw new DocumentAPIError(cause instanceof Error ? `Confirmed document content rejected: ${cause.message}` : "Confirmed document content rejected.",400) } finally { remote?.destroy() }
        if (epoch !== undefined && next.epoch !== epoch) {
          // An opaque epoch change invalidates earlier attempts. Never replay a draft automatically.
          blocked = true; setRecovery(true); setStatus("Document needs to be reopened. Recover your draft first."); return false
        }
        epoch = next.epoch
        if (next.snapshot) Y.applyUpdate(doc, decodeUpdate(next.snapshot), "remote")
        for (const update of next.updates ?? []) Y.applyUpdate(doc, decodeUpdate(update), "remote")
        setState(next)
        if (!next.can_write && queue.length) { blocked = true; setRecovery(true); setStatus("Write access changed. Recover your unsaved draft."); return false }
        return true
    }
    const read = () => client.fetchQuery({queryKey:documentKeys.detail(workspace,pageId),staleTime:0,gcTime:0,retry:false,queryFn:({signal})=>documentRequest<DocumentState>(workspace,endpoint.current,"",undefined,signal)})
    async function poll() {
      if (!workspace || !pageId || !active || authorizationLost.current || polling || blocked) return
      polling = true
      try {
        let next = await read()
        if (!accept(next)) return
        if (queue.length && active && next.can_write && writesAllowed.current) {
          const batch = queue[0]; batch.attempted = true
          try { documentSchema.validate(doc,next.schema) } catch (cause) { throw new DocumentAPIError(cause instanceof Error ? `Local document content rejected: ${cause.message}` : "Local document content rejected.",400) }
          if (!epoch) throw new DocumentAPIError("Confirmed document epoch required.",409)
          batch.baselineRev ??= next.rev
          batch.request ??= { id:batch.id,update:batch.update,schema:next.schema===1?2:next.schema,epoch,kind:"human",...((pageId ?? next.page_id)?{page_id:pageId ?? next.page_id}:{}) }
          const result = await documentRequest(workspace,endpoint.current,"/patch",batch.request)
          try {
            const receipt = verifyMutationReceipt(result,{...batch.request,baselineRev:batch.baselineRev})
            requiredRev = Math.max(requiredRev,receipt.rev)
          } catch (cause) { throw new UnconfirmedReceipt(cause instanceof Error ? cause.message : "Document update could not be confirmed.") }
          if (!active) return
          queue.shift();setPending(queue.length)
          // The receipt confirms durability, but the pre-write read is stale.
          // Refresh authorized head metadata before enabling checkpoints.
          next = await read()
          if (!accept(next)) return
          setPending(queue.length)
        }
        setError(undefined); setStatus(queue.length ? "Saving…" : next.can_write ? "Saved" : "Read only")
      } catch (cause) {
        if (!active) return
        setPending(queue.length)
        if (cause instanceof UnconfirmedReceipt) {
          blocked=true;setRecovery(true);setStatus("Save confirmation unavailable · changes are unsaved")
        } else if (cause instanceof DocumentAPIError && [401, 403, 404].includes(cause.status)) {
          authorizationLost.current=true;client.removeQueries({queryKey:documentKeys.detail(workspace,pageId)})
          blocked = true; setDenied(true); setRecovery(queue.length > 0); setStatus("Document access is unavailable.")
        } else if (cause instanceof DocumentAPIError && ([400, 413, 422].includes(cause.status) || (cause.status === 429 && cause.code === "document_quota"))) {
          blocked = true; setRecovery(true); setStatus("Document update was rejected. Recover your draft before reopening.")
        } else if (cause instanceof DocumentAPIError && cause.status === 429 && cause.code === "document_rate_limit") {
          setStatus("Temporarily rate limited · changes are unsaved")
        } else if (cause instanceof DocumentAPIError && cause.status === 409) {
          blocked = true; setRecovery(true); setStatus("Document changed. Recover your draft before reopening.")
        } else setStatus(queue.length ? "Disconnected · changes are unsaved" : "Disconnected")
        setError(cause instanceof Error ? cause.message : "Could not sync document.")
      } finally { polling = false }
    }
    if (workspace && pageId) void poll()
    const timer = setInterval(() => void poll(), 500)
    return () => { active = false; clearInterval(timer); doc.off("update", local); doc.destroy() }
  // The queue is scoped to immutable Page identity; the ref follows slug renames.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspace, pageId, doc, client])
  useEffect(() => {
    if (!pending) return
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = "" }
    window.addEventListener("beforeunload", warn)
    return () => window.removeEventListener("beforeunload", warn)
  }, [pending])
  return { doc, state, status, error, pending, recovery, denied, deny, editable: Boolean(state?.can_write && allowWrites && !recovery && !denied) }
}
