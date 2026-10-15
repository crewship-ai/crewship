"use client"

import { useEffect, useMemo, useState } from "react"
import * as Y from "yjs"
import { apiFetch } from "@/lib/api-fetch"

export interface DocumentState {
  page_id?: string; schema: number; rev: number; epoch: string; snapshot: string; updates?: string[]
  can_write: boolean; content?: unknown; outline?: { id: string; text: string; hash?: string; type?: string }[]
}
export function decodeUpdate(value: string): Uint8Array { return Uint8Array.from(atob(value), c => c.charCodeAt(0)) }
export function encodeUpdate(value: Uint8Array): string { let text = ""; for (const byte of value) text += String.fromCharCode(byte); return btoa(text) }
export class DocumentAPIError extends Error { constructor(message: string, public status: number, public code?: string) { super(message) } }
export async function documentRequest<T>(workspace: string, slug: string, path = "", body?: unknown): Promise<T> {
  const response = await apiFetch(`/api/v1/pages/${encodeURIComponent(slug)}/document${path}?workspace_id=${encodeURIComponent(workspace)}`, body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
  const value = await response.json().catch(() => null)
  if (!response.ok) throw new DocumentAPIError(value?.error ?? `Document request failed (${response.status}).`, response.status, value?.code)
  return value as T
}

/** Acknowledged updates are removed only after the API confirms them. Remote
 * updates merge into the same CRDT, never replace a locally edited JSON body. */
export function usePageDocument(workspace: string, slug: string) {
  const doc = useMemo(() => new Y.Doc({ gc: false, guid: `${workspace}:${slug}` }), [workspace, slug])
  const [state, setState] = useState<DocumentState>()
  const [status, setStatus] = useState("Loading document…")
  const [error, setError] = useState<string>()
  const [pending, setPending] = useState(0)
  const [recovery, setRecovery] = useState(false)
  const [denied, setDenied] = useState(false)
  useEffect(() => {
    let active = true, epoch: string | undefined, polling = false, blocked = false
    const queue: { id: string; update: string; attempted: boolean }[] = []
    const local = (update: Uint8Array, origin: unknown) => {
      if (origin === "remote") return
      const last = queue.at(-1)
      if (last && !last.attempted) last.update = encodeUpdate(Y.mergeUpdates([decodeUpdate(last.update), update]))
      else queue.push({ id: crypto.randomUUID(), update: encodeUpdate(update), attempted: false })
      setPending(queue.length); setStatus("Saving…")
    }
    doc.on("update", local)
    function accept(next: DocumentState): boolean {
        if (!active) return false
        setPending(queue.length)
        if (next.schema > 2) { blocked = true; setRecovery(queue.length > 0); setDenied(true); setStatus("This document needs a newer editor."); return false }
        if (epoch !== undefined && next.epoch !== epoch) {
          // A restore replaces history. Never replay a stale draft into its new epoch.
          blocked = true; setRecovery(true); setStatus("Document history changed. Recover your draft before reopening."); return false
        }
        epoch = next.epoch
        if (next.snapshot) Y.applyUpdate(doc, decodeUpdate(next.snapshot), "remote")
        for (const update of next.updates ?? []) Y.applyUpdate(doc, decodeUpdate(update), "remote")
        setState(next)
        if (!next.can_write && queue.length) { blocked = true; setRecovery(true); setStatus("Write access changed. Recover your unsaved draft."); return false }
        return true
    }
    async function poll() {
      if (!active || polling || blocked) return
      polling = true
      try {
        let next = await documentRequest<DocumentState>(workspace, slug)
        if (!accept(next)) return
        if (queue.length && active && next.can_write) {
          const batch = queue[0]; batch.attempted = true
          await documentRequest(workspace, slug, "/patch", { id: batch.id, update: batch.update, schema: 2, epoch, kind: "human" })
          queue.shift()
          // The receipt confirms durability, but the pre-write read is stale.
          // Refresh authorized head metadata before enabling checkpoints.
          next = await documentRequest<DocumentState>(workspace, slug)
          if (!accept(next)) return
          setPending(queue.length)
        }
        setError(undefined); setStatus(queue.length ? "Saving…" : next.can_write ? "Saved" : "Read only")
      } catch (cause) {
        if (!active) return
        setPending(queue.length)
        if (cause instanceof DocumentAPIError && [401, 403, 404].includes(cause.status)) {
          blocked = true; setDenied(true); setRecovery(queue.length > 0); setStatus("Document access is unavailable.")
        } else if (cause instanceof DocumentAPIError && ([400, 413, 422].includes(cause.status) || (cause.status === 429 && cause.code === "document_quota"))) {
          blocked = true; setRecovery(true); setStatus("Document update was rejected. Recover your draft before reopening.")
        } else if (cause instanceof DocumentAPIError && cause.status === 409) {
          blocked = true; setRecovery(true); setStatus("Document changed. Recover your draft before reopening.")
        } else setStatus(queue.length ? "Disconnected · changes are unsaved" : "Disconnected")
        setError(cause instanceof Error ? cause.message : "Could not sync document.")
      } finally { polling = false }
    }
    void poll()
    const timer = setInterval(() => void poll(), 500)
    return () => { active = false; clearInterval(timer); doc.off("update", local); doc.destroy() }
  }, [workspace, slug, doc])
  useEffect(() => {
    if (!pending) return
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = "" }
    window.addEventListener("beforeunload", warn)
    return () => window.removeEventListener("beforeunload", warn)
  }, [pending])
  return { doc, state, status, error, pending, recovery, denied, editable: Boolean(state?.can_write && !recovery && !denied) }
}
