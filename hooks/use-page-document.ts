"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import * as Y from "yjs"
import { documentSchema } from "@/lib/documents/schema"
import { apiFetch } from "@/lib/api-fetch"
import { useSessionSafe } from "@/hooks/use-auth"
import { openDocumentDrafts, DocumentStorageError, DocumentStorageBusy, type DocumentDraft, type DocumentDraftStore } from "@/lib/document-queue"

import { useQueryClient } from "@tanstack/react-query"
import { verifyMutationReceipt, type DocumentState } from "@/lib/documents/contracts"
export type { DocumentState } from "@/lib/documents/contracts"

export const documentKeys = { all: (workspace: string) => ["page-document",workspace] as const, detail: (workspace: string, pageId: string, human: string) => ["page-document",workspace,pageId,human] as const }
/** Journal writes of edits that arrive in separate tasks are merged within this window; the barrier before a send never waits for it. */
const JOURNAL_COALESCE_MS = 200
class UnconfirmedReceipt extends Error {}
export function decodeUpdate(value: string): Uint8Array { return Uint8Array.from(atob(value), c => c.charCodeAt(0)) }
export function encodeUpdate(value: Uint8Array): string { let text = ""; for (const byte of value) text += String.fromCharCode(byte); return btoa(text) }
/** Where unsent work stands. Only "confirmed" means the server holds everything. */
export type DocumentDurability =
  | "confirmed" // nothing unsent
  | "writing"   // unsent; the browser journal write has not resolved yet
  | "local"     // unsent; the journal write resolved, so it survives a reload
  | "unsaved"   // unsent and not retained by the browser either
  | "held"      // an earlier unsent draft is held in this browser, not restored
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
  const session = useSessionSafe()
  const human = session.status === "authenticated" ? session.data?.user.id ?? "" : ""
  const writesAllowed = useRef(allowWrites); writesAllowed.current = allowWrites
  const identity = JSON.stringify([human, workspace, pageId])
  const [activeIdentity, setActiveIdentity] = useState(identity)
  const current = Boolean(human) && activeIdentity === identity
  const endpoint = useRef(slug); endpoint.current = slug
  const doc = useMemo(() => new Y.Doc({ gc: false, guid: `${workspace}:${identity}` }), [workspace, identity])
  const [confirmed, setConfirmed] = useState<{identity:string;value:DocumentState}>()
  const state = current && confirmed?.identity === identity ? confirmed.value : undefined
  const setState = (value: DocumentState) => setConfirmed({identity,value})
  const [status, setStatus] = useState("Loading document…")
  const [error, setError] = useState<string>()
  const [pending, setPending] = useState(0)
  const [recovery, setRecovery] = useState(false)
  const [denied, setDenied] = useState(false)
  const [storageReady, setStorageReady] = useState(false)
  // Browser storage is unusable (private mode, blocked, quota, hung): the server
  // read still works, edits are held in memory only and reported as unsaved.
  const [storageOff, setStorageOff] = useState(false)
  const [storageBusy, setStorageBusy] = useState(false)
  const [storageLockless, setStorageLockless] = useState(false)
  // Rows found in a journal this tab could not lock: owner unknown, so download-only until explicitly discarded.
  const [unverified, setUnverified] = useState<DocumentDraft[]>([])
  const [journalWrites, setJournalWrites] = useState(0)
  const [journalFailed, setJournalFailed] = useState(false)
  const [recoveredDrafts, setRecoveredDrafts] = useState<DocumentDraft[]>([])
  const discardRecovery = useRef<() => Promise<void>>(async () => {})
  const discardRecoveredDrafts = useCallback(() => discardRecovery.current(), [])
  const authorizationLost=useRef(false)
  const deny=useCallback(()=>{authorizationLost.current=true;setConfirmed(undefined);setRecoveredDrafts([]);setUnverified([]);setDenied(true);setRecovery(pending>0);setStatus("Document access is unavailable.")},[pending])
  useEffect(() => {
    setActiveIdentity(identity)
    setConfirmed(undefined)
    setStorageReady(false); setStorageOff(false); setStorageBusy(false); setStorageLockless(false); setUnverified([]); setRecoveredDrafts([]); setJournalWrites(0); setJournalFailed(false)
    discardRecovery.current = async () => {};
    authorizationLost.current=false
    setDenied(false);setRecovery(false);setPending(0);setStatus("Loading document…");setError(undefined)
    if (!human) return () => { doc.destroy() }
    const queryKey = documentKeys.detail(workspace,pageId,human)
    const controller = new AbortController()
    let requiredRev = 0
    let active = true, epoch: string | undefined, polling = false, blocked = false
    let storage: DocumentDraftStore | undefined, schema = 0, durableOff = false
    let unverifiedStaged: DocumentDraft[] | undefined, blockedOther = false, opening: Promise<void> | undefined, retryAt = 0, retryDelay = 5000, tried = false
    const stored = new Set<string>()
    let restored: DocumentDraft[] = []
    // Bounded journal: one writer loop, one pending row per draft. Edits made
    // while a write is in flight coalesce into the next write of the same draft.
    const dirty = new Map<string, DocumentDraft>()
    let flushing: Promise<void> | undefined, inflight = 0
    const queue: DocumentDraft[] = []
    const countWrites = () => { if (active) setJournalWrites(dirty.size + inflight) }
    // One batch per run: a snapshot is encoded once per batch, and edits arriving
    // in separate tasks wait for the next timer, so cost does not scale with keystrokes.
    let flushTimer: ReturnType<typeof setTimeout> | undefined
    const run = () => flushing = Promise.resolve().then(async () => {
      try {
        const rows = [...dirty.values()]; dirty.clear(); inflight = rows.length; countWrites()
        let snapshot: string | undefined
        for (const draft of rows) {
          if (!storage) throw new DocumentStorageError("Private document storage is unavailable.")
          // The packet (update, request) of an attempted draft is never rewritten; only the recovery baseline is taken here.
          if (!draft.attempted || !draft.snapshot) draft.snapshot = snapshot ??= encodeUpdate(Y.encodeStateAsUpdate(doc))
          await storage.put(structuredClone(draft))
          inflight--
        }
      } catch (cause) {
        dirty.clear()
        if (!active) return
        blocked = blockedOther = true;setStorageReady(false);setJournalFailed(true);setRecovery(true);setStatus("Private document storage failed · changes are unsaved")
        setError(cause instanceof Error ? cause.message : "Could not retain draft.")
        throw cause
      } finally { inflight = 0; flushing = undefined; countWrites(); if (dirty.size) schedule() }
    })
    function schedule() { flushTimer ??= setTimeout(() => { flushTimer = undefined; if (!flushing && dirty.size) void run().catch(() => {}) }, JOURNAL_COALESCE_MS) }
    /** The durable barrier: resolves only when every queued row has been written. */
    const flushAll = async () => {
      clearTimeout(flushTimer); flushTimer = undefined
      while (flushing || dirty.size) await (flushing ?? run())
    }
    const persist = (batch: DocumentDraft) => {
      if (durableOff) return
      stored.add(batch.id)
      dirty.set(batch.id, batch)
      countWrites()
      schedule()
    }
    const local = (update: Uint8Array, origin: unknown) => {
      if (origin === "remote") return
      const last = queue.at(-1)
      if (last && !last.attempted) last.update = encodeUpdate(Y.mergeUpdates([decodeUpdate(last.update), update]))
      else queue.push({ id: crypto.randomUUID(), update: encodeUpdate(update), attempted: false, schema, epoch: epoch ?? "", snapshot: "" })
      const draft = queue.at(-1)!
      persist(draft)
      setPending(total()); setStatus("Saving…")
    }
    doc.on("update", local)
    const total = () => queue.length + restored.length
    function accept(next: DocumentState): boolean {
        if (!active || authorizationLost.current) return false
        if (next.rev < requiredRev) throw new Error("The acknowledged revision is not yet available. Your update is confirmed; refreshing saved content.")
        setPending(total())
        if (!Number.isSafeInteger(next.rev) || next.rev<0 || typeof next.epoch!=="string" || !next.epoch || next.epoch.length>256 || typeof next.snapshot!=="string" || typeof next.can_write!=="boolean" || (next.updates!==undefined && (!Array.isArray(next.updates) || next.updates.some(update=>typeof update!=="string")))) throw new DocumentAPIError("Invalid confirmed document metadata; content preserved.",400)
        if (![1,2,3].includes(next.schema)) { blocked = blockedOther = true; setRecovery(queue.length > 0); setDenied(true); setStatus("This document needs a newer editor."); return false }
        if (next.page_id !== pageId) throw new DocumentAPIError("The original Page identity changed. Recover your draft before reopening.",409)
        let remote: Y.Doc | undefined
        try { const loaded = documentSchema.load(next.snapshot); if (!(loaded instanceof Y.Doc)) throw new Error("Invalid document binary type"); remote=loaded; for (const update of next.updates ?? []) Y.applyUpdate(loaded,decodeUpdate(update)); documentSchema.validate(loaded,next.schema) } catch(cause) { throw new DocumentAPIError(cause instanceof Error ? `Confirmed document content rejected: ${cause.message}` : "Confirmed document content rejected.",400) } finally { remote?.destroy() }
        if (epoch !== undefined && next.epoch !== epoch) {
          // An opaque epoch change invalidates earlier attempts. Never replay a draft automatically.
          blocked = blockedOther = true; setRecovery(true); setStatus("Document needs to be reopened. Recover your draft first."); return false
        }
        epoch = next.epoch
        schema = next.schema
        if (next.snapshot) Y.applyUpdate(doc, decodeUpdate(next.snapshot), "remote")
        for (const update of next.updates ?? []) Y.applyUpdate(doc, decodeUpdate(update), "remote")
        setState(next)
        if (!next.can_write && queue.length) { blocked = blockedOther = true; setRecovery(true); setStatus("Write access changed. Recover your unsaved draft."); return false }
        return true
    }
    const read = () => client.fetchQuery({queryKey,staleTime:0,gcTime:0,retry:false,queryFn:({signal})=>documentRequest<DocumentState>(workspace,endpoint.current,"",undefined,signal)})
    async function attemptOpen() {
      try {
        const opened = await openDocumentDrafts(human,workspace,pageId)
        let rows: DocumentDraft[]
        try { rows = await opened.load() } catch (cause) { opened.close(); throw cause }
        // Access may have been revoked while the store was opening.
        if (!active || authorizationLost.current) { opened.close(); return }
        storage = opened
        if (!opened.exclusive) {
          // No lock, no journal: this tab never writes rows it could not later prove it owns.
          // Rows already there may belong to a live tab: download-only, editing continues in memory.
          if (rows.length) unverifiedStaged = rows
          restored = []; durableOff = true; setStorageOff(true); setStorageBusy(false); setStorageLockless(true)
          if (!rows.length) opened.close()
          return
        }
        restored = rows
        if (durableOff) {
          durableOff = false; setStorageOff(false); setStorageBusy(false)
          // Edits made while storage was away join the journal next to any held draft.
          for (const draft of queue) persist(draft)
          setPending(total())
        }
      } catch (cause) {
        if (!active) return
        durableOff = true; restored = []; setStorageOff(true); setStorageBusy(cause instanceof DocumentStorageBusy)
        retryAt = Date.now() + retryDelay; retryDelay = Math.min(retryDelay * 2, 30000)
      }
    }
    // Private rows of an unlocked journal become visible only after a fresh authorized read.
    function publishUnverified() { if (unverifiedStaged && active && !authorizationLost.current) { setUnverified(unverifiedStaged); unverifiedStaged = undefined } }
    async function poll() {
      if (!workspace || !pageId || !active || authorizationLost.current || polling || (blocked && !restored.length)) return
      polling = true
      try {
        let next = await read()
        if (!accept(next)) return
        publishUnverified()
        if (!storage) {
          // The first open gates only the editor becoming writable. Later
          // retries run in the background with backoff and never hold a send.
          if (!tried) {
            tried = true; await attemptOpen(); if (!active || authorizationLost.current) return
            // Authorization may have changed while the store opened: read again before exposing anything.
            next = await read(); if (!accept(next)) return
            publishUnverified()
          }
          else if (!opening && Date.now() >= retryAt) { opening = attemptOpen().finally(() => { opening = undefined }) }
        }
        if (restored.length) {
          // Read authorization must succeed before exposing any private
          // baseline from this browser. Regrant still never enables replay.
          const drafts = restored
          setRecoveredDrafts(drafts);setPending(total())
          discardRecovery.current = async () => {
            // Without the Page's lock these rows may belong to a live tab: never delete them.
            if (!active || authorizationLost.current || !storage) return
            await flushAll().catch(() => {})
            for (const draft of drafts) { dirty.delete(draft.id); await storage.remove(draft.id) }
            if (!active) return
            restored=[];setRecoveredDrafts([]);setPending(queue.length)
            // Blocks with another cause (rejection, epoch change, failed journal) stay.
            blocked = blockedOther
            if (!blockedOther) { setRecovery(false);setStorageReady(true) }
            void poll()
          }
          blocked = true;setRecovery(true);setStatus("Unsaved document draft found · recover before reopening")
          return
        }
        if (!durableOff) setStorageReady(true)
        if (queue.length && active && next.can_write && writesAllowed.current) {
          const batch = queue[0]; batch.attempted = true
          try { documentSchema.validate(doc,next.schema) } catch (cause) { throw new DocumentAPIError(cause instanceof Error ? `Local document content rejected: ${cause.message}` : "Local document content rejected.",400) }
          if (!epoch) throw new DocumentAPIError("Confirmed document epoch required.",409)
          batch.baselineRev ??= next.rev
          const fresh = !batch.request
          batch.request ??= { id:batch.id,update:batch.update,schema:next.schema===1?2:next.schema,epoch,kind:"human",...((pageId ?? next.page_id)?{page_id:pageId ?? next.page_id}:{}) }
          if (fresh) persist(batch) // a written attempted row is never rewritten
          await flushAll()
          if (!active || authorizationLost.current || blocked) return
          const result = await documentRequest(workspace,endpoint.current,"/patch",batch.request,controller.signal)
          try {
            const receipt = verifyMutationReceipt(result,{...batch.request,baselineRev:batch.baselineRev})
            requiredRev = Math.max(requiredRev,receipt.rev)
          } catch (cause) { throw new UnconfirmedReceipt(cause instanceof Error ? cause.message : "Document update could not be confirmed.") }
          if (!active) return
          // Retain the receipt fence even if journal cleanup fails: this mount
          // must never recommit acknowledged content.
          queue.shift();setPending(total())
          if (storage && stored.has(batch.id)) await storage.remove(batch.id)
          // The receipt confirms durability, but the pre-write read is stale.
          // Refresh authorized head metadata before enabling checkpoints.
          next = await read()
          if (!accept(next)) return
          setPending(total())
        }
        setError(undefined); setStatus(queue.length ? "Saving…" : next.can_write ? "Saved" : "Read only")
      } catch (cause) {
        if (!active) return
        setPending(total())
        if (cause instanceof DocumentStorageError) {
          blocked = blockedOther = true;setStorageReady(false);setJournalFailed(true);setRecovery(queue.length>0);setStatus(requiredRev && !queue.length ? "Update confirmed · private storage cleanup failed" : "Private document storage is unavailable · changes are unsaved")
        } else if (cause instanceof UnconfirmedReceipt) {
          blocked = blockedOther = true;setRecovery(true);setStatus("Save confirmation unavailable · changes are unsaved")
        } else if (cause instanceof DocumentAPIError && [401, 403, 404].includes(cause.status)) {
          authorizationLost.current=true;setConfirmed(undefined);setRecoveredDrafts([]);client.removeQueries({queryKey,exact:true})
          blocked = blockedOther = true; setDenied(true); setRecovery(queue.length > 0); setStatus("Document access is unavailable.")
        } else if (cause instanceof DocumentAPIError && ([400, 413, 422].includes(cause.status) || (cause.status === 429 && cause.code === "document_quota"))) {
          blocked = blockedOther = true; setRecovery(true); setStatus("Document update was rejected. Recover your draft before reopening.")
        } else if (cause instanceof DocumentAPIError && cause.status === 429 && cause.code === "document_rate_limit") {
          setStatus("Temporarily rate limited · changes are unsaved")
        } else if (cause instanceof DocumentAPIError && cause.status === 409) {
          blocked = blockedOther = true; setRecovery(true); setStatus("Document changed. Recover your draft before reopening.")
        } else setStatus(queue.length ? "Disconnected · changes are unsaved" : "Disconnected")
        setError(cause instanceof Error ? cause.message : "Could not sync document.")
      } finally { polling = false }
    }
    if (workspace && pageId) void poll()
    const timer = setInterval(() => void poll(), 500)
    return () => {
      active = false; clearInterval(timer); controller.abort()
      void client.cancelQueries({queryKey,exact:true})
      client.removeQueries({queryKey,exact:true})
      doc.off("update", local); doc.destroy()
      void flushAll().catch(() => {}).finally(() => { storage?.close() })
    }
  // The queue is scoped to the human and immutable Page identity; the ref follows slug renames.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspace, pageId, human, identity, doc, client])
  useEffect(() => {
    if (!current || !pending) return
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = "" }
    window.addEventListener("beforeunload", warn)
    return () => window.removeEventListener("beforeunload", warn)
  }, [current, pending])
  let durability: DocumentDurability = "confirmed"
  if (recoveredDrafts.length) durability = "held"
  else if (pending > 0) durability = journalFailed || !storageReady ? (journalWrites > 0 && !journalFailed ? "writing" : "unsaved") : journalWrites > 0 ? "writing" : "local"
  return { durability: current ? durability : "confirmed", doc, state, status: !human ? "Document access is unavailable." : current ? status : "Loading document…", error: current ? error : undefined, pending: current ? pending : 0, recovery: current && recovery, recoveredDrafts: current ? recoveredDrafts : [], discardRecoveredDrafts, denied: !human || (current && denied), deny, storageUnavailable: current && storageOff && !denied, unverifiedDrafts: current ? unverified : [], storageLockless: current && storageOff && storageLockless && !denied, storageBusy: current && storageOff && storageBusy && !denied, editable: Boolean(current && state?.can_write && (storageReady || storageOff) && allowWrites && !recovery && !denied) }
}
