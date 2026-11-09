"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import * as Y from "yjs"
import type { ArtifactOrigin } from "@/hooks/use-artifact-agent-dispatch"
import type { ArtifactComment } from "@/hooks/use-artifact-project"
import { useSessionSafe } from "@/hooks/use-auth"
import { artifactDraftKey, loadArtifactDraft, mergeArtifactDraft, admittedArtifactDraftMerge, purgeLocalDraftPage, captureLocalDraftPage } from "@/lib/artifact-drafts"
import { createLocalDraftCipher, LocalDraftReadRevoked } from "@/lib/local-draft-seal"
import { registerDocumentDraftWriter } from "@/lib/document-queue"
import { apiFetch, tryRefresh } from "@/lib/api-fetch"
import { getAuthMode, resolveWsBase } from "@/lib/server-base"
import { DesignProvider, type DesignConnection, type DesignPeer } from "./design-provider"
import { combineDesignDrafts, createDesignUndo, designConflictIdentity, designDraftConflicts, designNodes, designStateCovered, mergeDesignConflictRecord, parseDesignConflictRecord, parseDesignDraft, serializeDesignDraft, type DesignConflict, type DesignNode } from "./design-document"

interface DesignState {
 page_id: string
 authority_revision: string
 origin?: ArtifactOrigin
 can_comment?: boolean
 comments?: ArtifactComment[]
 nodes: DesignNode[]
 order: string[]
 can_edit: boolean
 /** Only the Page owner shares a version; Edit alone never offers it. */
 can_share?: boolean
 version: number
 latest_version: number
 shared_version: number
 actor?: { id: string; name: string }
}
export interface DesignVersion { version: number; message?: string; actor?: string; created_at?: string }
interface DesignVersions { versions: DesignVersion[]; latest_version: number; shared_version: number }
const COLORS = ["#2563eb", "#c026d3", "#059669", "#ea580c", "#9333ea", "#be123c", "#0e7490", "#4d7c0f"]
/**
 * One colour per person on this canvas, the same for everyone looking at it:
 * the people present (the viewer included) in user ID order take the palette
 * in turn, so nobody shares a colour until more people than colours are here.
 */
export function designPresenceColors(userIds: Iterable<string>): Map<string, string> {
 const sorted = [...new Set(userIds)].sort()
 return new Map(sorted.map((id, index) => [id, COLORS[index % COLORS.length]]))
}
const DRAFT_LIMIT = 2 * 1024 * 1024
function encodeDraft(bytes: Uint8Array) {
 if (bytes.length > DRAFT_LIMIT) throw new Error("Canvas exceeds the local draft limit. Keep this tab open.")
 let value = ""
 for (let index = 0; index < bytes.length; index += 8192) value += String.fromCharCode(...bytes.subarray(index, index + 8192))
 return btoa(value)
}
const decodeDraft = (value: string) => Uint8Array.from(atob(value), char => char.charCodeAt(0))
/** What older builds stored beside a bare canvas: only read, for the comparison base of that canvas. */
interface PendingMark { baseline: string; work: string }
function readPendingRecord(text: string): Record<string, PendingMark> {
 try {
  const saved: unknown = JSON.parse(text || "{}")
  if (!saved || typeof saved !== "object" || Array.isArray(saved)) return {}
  return Object.fromEntries(Object.entries(saved as Record<string, PendingMark>).filter(([, mark]) => typeof mark?.baseline === "string" && typeof mark.work === "string"))
 } catch { return {} }
}
/** Conflicts on screen: the durable list, plus changes this tab could not store yet. */
function designConflictView(ledger: ConflictLedger | undefined): DesignConflict[] {
 if (!ledger) return []
 const seen = new Set(ledger.remove)
 return [...ledger.durable, ...ledger.add].filter(conflict => {
  const identity = designConflictIdentity(conflict)
  if (seen.has(identity)) return false
  seen.add(identity)
  return true
 })
}
interface ConflictLedger { key: string; durable: DesignConflict[]; add: DesignConflict[]; remove: string[]; merged?: boolean }

export function useDesign(workspaceId: string, slug: string, selectedVersion?: number) {
 const session = useSessionSafe()
 const userId = session?.data?.user?.id ?? "anonymous"
 const client = useQueryClient()
 const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}/design`
 const key = ["artifact-design", workspaceId, slug, userId]
 const request = useCallback(async <T,>(path: string, body?: unknown, targetPageId?: string): Promise<T> => {
  const target = targetPageId ? `/api/v1/pages/@${encodeURIComponent(targetPageId)}/design` : endpoint
  const url = `${target}${path}${path.includes("?") ? "&" : "?"}workspace_id=${encodeURIComponent(workspaceId)}`
  const response = await apiFetch(url, body === undefined ? { cache: "no-store" } : { method: "POST", skipRefresh: true, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
  const data = await response.json().catch(() => null)
  if (!response.ok) throw Object.assign(new Error(data?.error ?? "Design could not be loaded."), { status: response.status })
  return data
 }, [endpoint, workspaceId])
 const query = useQuery({ queryKey: [...key, selectedVersion ?? "live"], enabled: userId !== "anonymous", queryFn: async () => {
  const current = await request<DesignState>(selectedVersion ? `?version=${selectedVersion}` : "")
  if (current.actor?.id !== userId) throw new Error("Design session changed. Reopen the canvas.")
  return current
 }, retry: false, gcTime: 0, refetchInterval: 10_000 })
 const pageId = query.data?.page_id
 // Opaque server policy generation: only successful metadata can admit a
 // replacement socket after the previous connection was retired.
 const authorityRevision = !query.isError && typeof query.data?.authority_revision === "string" && query.data.authority_revision !== "" ? query.data.authority_revision : null
 const pinnedRequest = useCallback(async <T,>(path: string, body?: unknown): Promise<T> => {
  if (!pageId) throw new Error("Design identity is unavailable.")
  return request<T>(path, body, pageId)
 }, [pageId, request])
 const versions = useQuery({ queryKey: [...key, pageId, authorityRevision, "versions"], queryFn: () => pinnedRequest<DesignVersions>("/versions"), enabled: Boolean(pageId && authorityRevision !== null && !query.isError), retry: false, gcTime: 0, refetchInterval: 15_000 })
 const comments = useQuery({ queryKey: [...key, pageId, authorityRevision, "comments"], queryFn: () => pinnedRequest<{ comments: ArtifactComment[] }>("/comments"), enabled: Boolean(pageId && authorityRevision !== null && !query.isError), retry: false, gcTime: 0, refetchInterval: 5000 })
 const [doc, setDoc] = useState<Y.Doc | null>(null)
 const [nodes, setNodes] = useState<DesignNode[]>([])
 const [peers, setPeers] = useState<DesignPeer[]>([])
 const [connection, setConnection] = useState<DesignConnection>("connecting")
 const [dirty, setDirty] = useState(false)
 // The Page with local work the server has not confirmed. Outlives a provider:
 // it ends with a confirmation, never with a replacement or a new connection.
 const [unsyncedPage, setUnsyncedPage] = useState<string>()
 const [readRevokedPage, setReadRevokedPage] = useState<string>()
 const [draftError, setDraftError] = useState(false)
 const provider = useRef<DesignProvider | null>(null)
 const selectionRef = useRef<string | null>(null)
 // This person's own edits only: a peer's change is never undone (Yjs never
 // overwrites a newer value it did not write).
 const undoRef = useRef<Y.UndoManager | null>(null)
 const [history, setHistory] = useState({ undo: false, redo: false })
 const [draftReady, setDraftReady] = useState(false)
 // A text that lost a merge exists only here: a sealed draft keeps it over a
 // reload until the person decides, and goes with the other drafts on read loss.
 const conflictKey = pageId ? artifactDraftKey("design-conflict", userId, workspaceId, pageId) : null
 const [ledger, setLedger] = useState<ConflictLedger>()
 const ledgerRef = useRef(ledger)
 const changeLedger = useCallback((target: string, change: (current: ConflictLedger) => ConflictLedger) => {
  const previous = ledgerRef.current
  ledgerRef.current = change(previous?.key === target ? previous : { key: target, durable: [], add: [], remove: [] })
  setLedger(ledgerRef.current)
 }, [])
 useEffect(() => {
  if (!conflictKey) return
  let active = true
  void loadArtifactDraft(conflictKey).then(text => {
   // A merge that landed meanwhile holds the newer list.
   if (active && text) changeLedger(conflictKey, current => current.merged ? current : { ...current, durable: parseDesignConflictRecord(text) })
  }).catch(() => { if (active) setDraftError(true) })
  return () => { active = false }
 }, [conflictKey, changeLedger])
 // Runs under the draft's cross-tab lock against what is stored at that moment.
 // Each change stays in the ledger until a merge carried it, so a failed one
 // stays on screen and goes out with the next.
 const storeConflicts = useCallback((change: { add?: DesignConflict[]; remove?: string[] }) => {
  if (!conflictKey) return Promise.resolve(true)
  changeLedger(conflictKey, current => ({ ...current, add: [...current.add, ...(change.add ?? [])], remove: [...current.remove, ...(change.remove ?? [])] }))
  const { add, remove } = ledgerRef.current!
  return mergeArtifactDraft(conflictKey, existing => mergeDesignConflictRecord(existing, add, remove)).then(text => {
   changeLedger(conflictKey, current => ({ key: conflictKey, durable: parseDesignConflictRecord(text), add: current.add.filter(item => !add.includes(item)), remove: current.remove.filter(item => !remove.includes(item)), merged: true }))
   return true
  }, () => { setDraftError(true); return false })
 }, [conflictKey, changeLedger])
 const storeConflictsRef = useRef(storeConflicts)
 useEffect(() => { storeConflictsRef.current = storeConflicts }, [storeConflicts])
 const tabId = useRef("")
 const conflicts = designConflictView(conflictKey && ledger?.key === conflictKey && readRevokedPage !== pageId ? ledger : undefined)
 const editable = Boolean(readRevokedPage !== pageId && draftReady && authorityRevision !== null && query.data?.can_edit && !query.isError && !selectedVersion && connection !== "denied" && connection !== "rejected")
 const editableRef = useRef(editable)
 useEffect(() => { editableRef.current = editable; provider.current?.setEditable(editable) }, [editable])
 const canConnect = Boolean(pageId && authorityRevision !== null && !query.isError && !selectedVersion)
 const actorName = query.data?.actor?.name ?? session?.data?.user?.name ?? "Collaborator"
 const initialCanEdit = query.data?.can_edit === true
 const admittedActor = query.data?.actor?.id
 // Only a fresh, same-person Page read refusal authorizes local deletion.
 const lastPage = useRef<{ page: string; human: string; address: string } | undefined>(undefined)
 if (pageId && !query.isError) lastPage.current = { page: pageId, human: userId, address: JSON.stringify([workspaceId, slug]) }
 const accessStatus = (query.error as { status?: number } | null)?.status
 useEffect(() => {
  const previous = lastPage.current
  if (!previous || previous.human !== userId || previous.address !== JSON.stringify([workspaceId, slug]) || (accessStatus !== 403 && accessStatus !== 404)) return
  const scope = { human: userId, workspace: workspaceId, page: previous.page }
  void captureLocalDraftPage(scope).then(captured => createLocalDraftCipher(scope).then(cipher => cipher.close(), async error => {
   if (error instanceof LocalDraftReadRevoked) { setReadRevokedPage(scope.page); await purgeLocalDraftPage(scope, captured) }
  })).catch(() => setDraftError(true))
 }, [accessStatus, userId, workspaceId, slug])
 useEffect(() => {
  if (!canConnect || !pageId) return
  let active = true
  const document = new Y.Doc()
  setDraftReady(false)
  const storageKey = artifactDraftKey("design-tabs", userId, workspaceId, pageId)!
  const oldCanvasKey = artifactDraftKey("design-canvas", userId, workspaceId, pageId)!
  const markerKey = artifactDraftKey("design-pending", userId, workspaceId, pageId)!
  let cleanup = () => { document.destroy() }
  const initialize = async () => {
  const canRecover = (error: unknown) => { if (initialCanEdit || error instanceof LocalDraftReadRevoked) throw error; setDraftError(true); return "" }
  // A reader only needs to know unsent work exists; failing to read must not cost them the live canvas.
  const tabs = await loadArtifactDraft(storageKey, combineDesignDrafts).then(parseDesignDraft).catch(error => parseDesignDraft(canRecover(error)))
  if (!active) return
  // What an older build stored: read as one more entry, never written.
  const oldCanvas = initialCanEdit ? await loadArtifactDraft(oldCanvasKey).catch(canRecover) : ""
  if (!active) return
  const marked = readPendingRecord(await loadArtifactDraft(markerKey).catch(canRecover))
  if (!active) return
  const hadMarks = Object.keys(marked).length > 0
  // Every entry joins the document in a fixed order, and what the merge hides
  // becomes a notice. Recomputed on each reopen until the server covers the
  // entries, so a reopen that dies here has lost nothing.
  const hidden: DesignConflict[] = []
  const entries = Object.entries(tabs).sort(([one], [two]) => one.localeCompare(two))
  if (initialCanEdit) for (const state of [...oldCanvas ? [decodeDraft(oldCanvas)] : [], ...entries.map(([, entry]) => decodeDraft(entry.state))]) {
   if (document.store.clients.size) hidden.push(...designDraftConflicts(document, state))
   Y.applyUpdate(document, state)
  }
  setDraftReady(true)
  const loaded = Y.snapshot(document)
  // An old canvas has its base in a mark. Anything the server held is a sound base, so several join.
  const oldBaseline = oldCanvas ? Object.values(marked).map(mark => decodeDraft(mark.baseline)).find(base => designStateCovered(loaded, base)) : undefined
  const bases = [...entries.flatMap(([, entry]) => entry.baseline ? [decodeDraft(entry.baseline)] : []), ...(oldBaseline ? [oldBaseline] : [])]
  const baseline = bases.length > 1 ? Y.mergeUpdates(bases) : bases[0]
  const unconfirmed = initialCanEdit && Boolean(entries.length || oldCanvas || hadMarks)
  // A stored draft is unconfirmed until this socket's barrier says otherwise.
  if (unconfirmed || hadMarks || entries.length) setUnsyncedPage(pageId)
  else if (initialCanEdit) setUnsyncedPage(current => current === pageId ? undefined : current)
  // The latest store carries every notice not yet stored, so its result speaks for all.
  let noticed = hidden.length ? storeConflictsRef.current({ add: hidden }) : Promise.resolve(true)
  // Causal coverage says nothing of text a merge dropped: only entries this tab
  // examined may go. Any other entry waits for the next reopen.
  const recovered = new Map(initialCanEdit ? entries.map(([tab, entry]) => [tab, entry.state]) : [])
  let stored = unconfirmed
  let localDirty = false
  let currentBaseline = baseline ? encodeDraft(baseline) : null
  // Edits made, and how many the last store carried. Retirement stores only what
  // no store carried: a repeat is a new revision, which a confirmed sign-out,
  // deleting exactly the revisions it saw, would leave behind.
  let edits = 0, carried = 0
  const storeEntry = (existing: string) => {
   canvasQueued = false
   carried = edits
   const record = parseDesignDraft(existing)
   tabId.current ||= crypto.randomUUID()
   record[tabId.current] = { state: encodeDraft(Y.encodeStateAsUpdate(document)), baseline: currentBaseline }
   return serializeDesignDraft(record)
  }
  const capture = initialCanEdit ? admittedArtifactDraftMerge(storageKey, storeEntry, combineDesignDrafts) : undefined
  // Set from admission to the merge reading the document, which then carries
  // every edit made since. A failed store leaves the last pair as it was.
  let canvasQueued = false
  const storeCanvas = () => {
   if (canvasQueued) return
   canvasQueued = true
   stored = true
   void mergeArtifactDraft(storageKey, storeEntry, combineDesignDrafts).then(() => { if (carried === edits) localDirty = false }, () => { canvasQueued = false; setDraftError(true) })
  }
  // Only this tab's entries, once the confirmed document holds them and the
  // notices that name their lost texts are stored.
  const clearEntries = async (confirmed: Y.Snapshot) => {
   if (!await noticed) throw new Error("A conflict could not be stored.")
   await mergeArtifactDraft(storageKey, existing => {
    const record = parseDesignDraft(existing)
    for (const [tab, entry] of Object.entries(record)) if ((tab === tabId.current || recovered.get(tab) === entry.state) && designStateCovered(confirmed, decodeDraft(entry.state))) delete record[tab]
    return serializeDesignDraft(record)
   }, combineDesignDrafts)
   if (oldCanvas && designStateCovered(confirmed, decodeDraft(oldCanvas))) await mergeArtifactDraft(oldCanvasKey, existing => existing === oldCanvas ? "" : existing)
   if (hadMarks) await mergeArtifactDraft(markerKey, () => "")
  }
  setDoc(document)
  setNodes(designNodes(document))
  const undo = createDesignUndo(document)
  const historyChanged = () => setHistory({ undo: undo.canUndo(), redo: undo.canRedo() })
  undo.on("stack-item-added", historyChanged); undo.on("stack-item-popped", historyChanged); undo.on("stack-cleared", historyChanged)
  undoRef.current = undo
  const update = (_update: Uint8Array, origin: unknown) => {
   setNodes(designNodes(document))
   if ((origin === "editor" || origin === undo) && initialCanEdit && userId !== "anonymous") {
    localDirty = true
    edits += 1
    storeCanvas()
   }
  }
  document.on("update", update)
  const color = COLORS[Array.from(userId).reduce((sum, char) => sum + char.charCodeAt(0), 0) % COLORS.length]
  const live = new DesignProvider(document, `${resolveWsBase()}/api/v1/pages/@${encodeURIComponent(pageId)}/design/ws?workspace_id=${encodeURIComponent(workspaceId)}&authority_revision=${encodeURIComponent(authorityRevision!)}`, initialCanEdit, { name: actorName, color }, status => {
   setConnection(status)
   if (status === "denied") void client.invalidateQueries({ queryKey: ["artifact-design", workspaceId, slug, userId] })
  }, () => setDirty(true), async () => {
   if (getAuthMode() !== "bearer") {
    const refreshed = await tryRefresh()
    if (!active) return "denied"
    if (refreshed !== "ok") return refreshed === "auth_failed" ? "denied" : "retry"
   }
   try {
    const current = await request<DesignState>("", undefined, pageId)
    if (!active) return "denied"
    // A new cookie may belong to another person, or permissions may have
    // changed while refreshing. Never carry this document across either gap.
    if (!admittedActor || current.actor?.id !== admittedActor || current.page_id !== pageId || current.authority_revision !== authorityRevision || current.can_edit !== initialCanEdit) {
     void client.invalidateQueries({ queryKey: ["artifact-design", workspaceId, slug, userId] })
     return "denied"
    }
    return "ready"
   } catch (error) {
    const status = (error as { status?: number }).status
    return status === 401 || status === 403 || status === 404 ? "denied" : "retry"
   }
  }, {
   unconfirmed,
   baseline: unconfirmed ? baseline : undefined,
   pending: confirmed => {
    if (confirmed) setUnsyncedPage(pageId)
    else setUnsyncedPage(current => current === pageId ? undefined : current)
    if (confirmed) {
     // Goes out with the next stored canvas, never beside it.
     try { currentBaseline = encodeDraft(confirmed) } catch { setDraftError(true) }
     return
    }
    currentBaseline = null
    localDirty = false
    if (!stored) return
    stored = false
    // Taken now: the document may move on before the merge runs.
    void clearEntries(Y.snapshot(document)).catch(() => setDraftError(true))
   },
   conflicts: found => { noticed = storeConflictsRef.current({ add: found }) },
  })
  provider.current = live
  // A replaced provider (new authority, reconnect) keeps announcing the selection.
  if (selectionRef.current) live.setSelection(selectionRef.current)
  const awareness = () => {
   const states = [...live.awareness.getStates()]
   const colors = designPresenceColors([userId, ...states.flatMap(([clientId, state]) => clientId !== document.clientID && typeof state.user?.id === "string" ? [state.user.id as string] : [])])
   setPeers(states.flatMap(([clientId, state]) => {
   if (clientId === document.clientID || !state.user) return []
   const selection = typeof state.selection === "string" && state.selection && state.selection.length <= 128 ? state.selection : undefined
   return [{ clientId, name: String(state.user.name ?? "Collaborator"), color: (typeof state.user.id === "string" && colors.get(state.user.id)) || String(state.user.color ?? "#2563eb"), cursor: state.cursor && Number.isFinite(state.cursor.x) && Number.isFinite(state.cursor.y) ? state.cursor : undefined, ...(selection ? { selection } : {}) }]
  }))
  }
  live.awareness.on("change", awareness)
  const stop = async () => { live.setEditable(false); live.destroy(); editableRef.current = false; setDraftReady(false) }
  const unregister = registerDocumentDraftWriter(userId, stop, { suspend: async () => {
   // Freeze transport first; a failed capture keeps this document mounted.
   await stop()
   if (localDirty && capture) await capture()
  } })
  cleanup = () => {
   unregister()
   if (provider.current === live) provider.current = null
   live.awareness.off("change", awareness)
   live.destroy()
   document.off("update", update)
   if (undoRef.current === undo) undoRef.current = null
   undo.destroy()
   document.destroy()
  }
  }
  void initialize().catch(error => { if (active && error instanceof LocalDraftReadRevoked) setReadRevokedPage(pageId); if (active) { setDraftError(true); setDraftReady(false) } })
  return () => {
   active = false
   cleanup()
   setDoc(null); setNodes([]); setPeers([]); setDraftReady(false); setHistory({ undo: false, redo: false })
  }
 }, [canConnect, endpoint, workspaceId, slug, userId, pageId, authorityRevision, actorName, admittedActor, initialCanEdit, client, request])
 const invalidate = () => { void client.invalidateQueries({ queryKey: key }) }
 const save = useMutation({ mutationFn: async (message: string) => {
  if(provider.current?.storageFull)throw Object.assign(new Error("Workspace storage is full. Your private draft is retained."),{status:413})
  if (!editableRef.current || !provider.current?.ready) throw new Error("Reconnect before saving a version. Your changes remain in this browser.")
  await provider.current.synchronize()
  return pinnedRequest<{ version: number }>("/snapshots", { message })
 }, onSuccess: () => { setDirty(false); invalidate() } })
 const saveRef = useRef(save.mutate)
 useEffect(() => { saveRef.current = save.mutate }, [save.mutate])
 useEffect(() => {
  if (!dirty || !editable || connection !== "connected") return
  const timer = setTimeout(() => saveRef.current("Automatic checkpoint"), 10_000)
  return () => clearTimeout(timer)
 }, [dirty, nodes, editable, connection])
 const restore = useMutation({ mutationFn: (version: number) => pinnedRequest("/restore", { version }), onSuccess: invalidate })
 const connectAgent = useMutation({ mutationFn: (chat_id: string) => pinnedRequest("/agent", { chat_id }), onSuccess: invalidate })
 const comment = useMutation({ mutationFn: (body: { version: number; path: string; selector: string; quote: string; text: string; send_to_agent: boolean }) => pinnedRequest<ArtifactComment>("/comments", body), onSuccess: invalidate })
 const share = useMutation({ mutationFn: (version: number) => pinnedRequest("/share", { version }), onSuccess: invalidate })
 return { readRevoked: readRevokedPage === pageId, query, versions, comments, doc, nodes: selectedVersion || query.data?.can_edit === false ? query.data?.nodes ?? [] : nodes, peers, connection, editable, dirty, unsynced: Boolean(pageId) && unsyncedPage === pageId && readRevokedPage !== pageId, draftError, save, restore, share, comment, connectAgent, conflicts, dismissConflict: (conflict: DesignConflict) => storeConflicts({ remove: [designConflictIdentity(conflict)] }), setCursor: (cursor: { x: number; y: number } | null) => provider.current?.setCursor(cursor), setSelection: (id: string | null) => { selectionRef.current = id; provider.current?.setSelection(id) },
  canUndo: editable && history.undo, canRedo: editable && history.redo,
  undo: () => { if (editableRef.current) undoRef.current?.undo() }, redo: () => { if (editableRef.current) undoRef.current?.redo() },
  /** Ends the current undo step, so the next edit is undone on its own. */
  endUndoStep: () => undoRef.current?.stopCapturing() }
}
