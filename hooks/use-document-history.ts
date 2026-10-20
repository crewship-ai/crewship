"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { documentSchema } from "@/lib/documents/schema"
import { useRealtimeEventSafe, type RealtimeEvent } from "@/hooks/use-realtime"
import type { DocumentCheckpoint, DocumentCheckpointList, DocumentCheckpointSnapshot, HistoryRead } from "@/components/features/pages/document-history/types"

export interface DocumentHistoryScope {
  enabled?: boolean
  workspaceId: string
  humanId: string
  pageId: string
  /** Nonsecret auth/open epoch; change whenever the authenticated open changes. */
  activationKey: string
}
const identifier = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(value)
const object = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === "object" && !Array.isArray(value)
const closed = (value: unknown, keys: string[]): value is Record<string, unknown> => object(value) && Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key))
const nonempty = (value: string) => typeof value === "string" && value.trim().length > 0
const revision = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) >= 0
const schemaNumber = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) >= 1
const supported = (value: number): value is 2 | 3 => value === 2 || value === 3
class HistoryFailure extends Error {
  constructor(readonly status: "denied" | "unavailable" | "error") { super("Document history unavailable") }
}
function listResponse(value: unknown): DocumentCheckpointList {
  if (!closed(value, ["checkpoints"]) || !Array.isArray(value.checkpoints) || value.checkpoints.length > 1000) throw new HistoryFailure("error")
  const seen = new Set<string>()
  for (const pin of value.checkpoints) {
    if (!closed(pin, ["id", "rev", "schema", "actor", "created_at"]) || !identifier(pin.id) || seen.has(pin.id) || !revision(pin.rev) || !schemaNumber(pin.schema) || typeof pin.actor !== "string" || pin.actor.length > 1024 || typeof pin.created_at !== "string" || pin.created_at.length > 128) throw new HistoryFailure("error")
    seen.add(pin.id)
  }
  return value as unknown as DocumentCheckpointList
}
function snapshotResponse(value: unknown, pageId: string, pin: DocumentCheckpoint): DocumentCheckpointSnapshot {
  if (!closed(value, ["page_id", "id", "rev", "schema", "content"]) || value.page_id !== pageId || value.id !== pin.id || value.rev !== pin.rev || value.schema !== pin.schema || !supported(pin.schema)) throw new HistoryFailure("error")
  // Root closure is explicit for both supported versions. The compiled schema
  // checks all nested nodes/marks/attributes; no Y.Doc or editor is constructed.
  if (!closed(value.content, ["type", "content"]) || value.content.type !== "doc" || !Array.isArray(value.content.content)) throw new HistoryFailure("error")
  documentSchema.validateJSON(value.content, pin.schema)
  const schema = documentSchema.schemaFor(pin.schema), node = schema.nodeFromJSON(value.content)
  node.check()
  if (node.type !== schema.topNodeType) throw new HistoryFailure("error")
  return value as unknown as DocumentCheckpointSnapshot
}
async function read(url: string, signal: AbortSignal): Promise<unknown> {
  const response = await apiFetch(url, { signal, cache: "no-store" })
  if (!response.ok) throw new HistoryFailure([401, 403, 404].includes(response.status) ? "denied" : response.status === 503 ? "unavailable" : "error")
  return response.json()
}
type Scope = { scopeKey: string; available: boolean }
type Selection = { scope: Scope; id: string }
type State = { scope: Scope; list: HistoryRead<DocumentCheckpointList>; snapshot: HistoryRead<DocumentCheckpointSnapshot>; selection?: Selection }

/** Private local reads only. No shared query cache, editor binding or write path.
 * Callers must change activationKey on auth/open changes, including same-human
 * credential changes. A confirmed snapshot never replaces the live draft. */
export function useDocumentHistory({ enabled, workspaceId, humanId, pageId, activationKey }: DocumentHistoryScope) {
  const [refreshEpoch, setRefreshEpoch] = useState(0)
  const scope = useMemo(() => ({
    available: enabled === true && nonempty(workspaceId) && nonempty(humanId) && identifier(pageId) && nonempty(activationKey),
    scopeKey: JSON.stringify([workspaceId, humanId, pageId, activationKey, enabled === true, refreshEpoch]),
  }), [enabled, workspaceId, humanId, pageId, activationKey, refreshEpoch])
  const current = useRef(scope)
  current.current = scope
  const [state, setState] = useState<State>({ scope, list: { status: "idle" }, snapshot: { status: "idle" } })
  const [selection, setSelection] = useState<Selection>()
  const endpoint = `/api/v1/pages/${encodeURIComponent(`@${pageId}`)}/document/checkpoints`
  const params = new URLSearchParams({ workspace_id: workspaceId }).toString()
  const refresh = useCallback(() => { if (current.current === scope && scope.available) setRefreshEpoch(value => value + 1) }, [scope])
  const invalidate = useCallback((event: RealtimeEvent) => {
    if (current.current !== scope || !scope.available) return
    if (event.payload.workspace_id !== undefined && event.payload.workspace_id !== workspaceId) return
    if (event.channel && event.channel !== `workspace:${workspaceId}` && event.channel !== `page:${pageId}`) return
    if (event.type !== "realtime.reconnected") {
      if (event.payload.page_id !== undefined && event.payload.page_id !== pageId) return
      // Private workspace invalidations deliberately omit even the Page ID.
      if (event.payload.page_id === undefined && event.channel !== `workspace:${workspaceId}`) return
    }
    refresh()
  }, [scope, workspaceId, pageId, refresh])
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("page.deleted", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)

  useEffect(() => {
    if (!scope.available) { setState({ scope, list: { status: "idle" }, snapshot: { status: "idle" } }); return }
    let active = true
    const controller = new AbortController()
    const live = () => active && !controller.signal.aborted && current.current === scope
    setState({ scope, list: { status: "loading" }, snapshot: { status: "idle" } })
    void read(`${endpoint}?${params}`, controller.signal).then(value => {
      if (!live()) return
      const data = listResponse(value)
      if (live()) setState({ scope, list: { status: "ready", scopeKey: scope.scopeKey, data }, snapshot: { status: "idle" } })
    }).catch(error => {
      if (live()) { const status = error instanceof HistoryFailure ? error.status : "error"; setState({ scope, list: { status }, snapshot: { status } }) }
    })
    return () => { active = false; controller.abort() }
  }, [scope, endpoint, params])

  const list = useMemo<HistoryRead<DocumentCheckpointList>>(() => !scope.available ? { status: "idle" } : state.scope !== scope ? { status: "loading" } : state.list, [scope, state])
  const selectedId = selection?.scope === scope ? selection.id : undefined
  const pin = list.status === "ready" ? list.data.checkpoints.find(item => item.id === selectedId) : undefined
  useEffect(() => {
    if (!scope.available || !selection || selection.scope !== scope || !pin || !supported(pin.schema)) return
    let active = true
    const controller = new AbortController()
    const live = () => active && !controller.signal.aborted && current.current === scope
    void read(`${endpoint}/${encodeURIComponent(pin.id)}?${params}`, controller.signal).then(value => {
      if (!live()) return
      const data = snapshotResponse(value, pageId, pin)
      if (live()) setState(old => old.scope !== scope ? old : { ...old, selection, snapshot: { status: "ready", scopeKey: scope.scopeKey, data } })
    }).catch(error => {
      if (live()) { const status = error instanceof HistoryFailure ? error.status : "error"; setState({ scope, selection, list: { status }, snapshot: { status } }) }
    })
    return () => { active = false; controller.abort() }
  }, [scope, selection, pin, endpoint, params, pageId])
  const onSelect = useCallback((id: string) => {
    if (current.current !== scope || list.status !== "ready" || !list.data.checkpoints.some(pin => pin.id === id)) return
    setSelection({ scope, id })
  }, [scope, list])
  let snapshot: HistoryRead<DocumentCheckpointSnapshot> = { status: "idle" }
  if (scope.available && list.status !== "ready") snapshot = { status: list.status }
  else if (selectedId) snapshot = !pin || !supported(pin.schema) ? { status: "unavailable" }
    : state.selection !== selection ? { status: "loading" } : state.snapshot
  return { scopeKey: scope.scopeKey, list, snapshot, selectedId, onSelect, refresh }
}

type RestoreHead = { page_id: string; rev: number; schema: number; epoch: string; can_write?: boolean }
type RestorePacket = { page_id: string; id: string; schema: number; epoch: string; ifRev: number; checkpoint: string }
type RestoreStatus = "idle" | "pending" | "uncertain" | "error" | "denied"
interface RestoreScope extends DocumentHistoryScope {
  head?: RestoreHead
  snapshot: HistoryRead<DocumentCheckpointSnapshot>
  historyScopeKey: string
  blocked: boolean
  onStart: () => void
  onRestored: () => void
  onRefused: (denied: boolean) => void
}

/** Restore is a separate explicit human write. Never rebuild an uncertain
 * packet from a later head; the service/Store retain receipt and epoch fencing. */
export function useDocumentHistoryRestore(input: RestoreScope) {
  const { workspaceId, humanId, pageId, activationKey, enabled, head, snapshot, blocked } = input
  // Availability may disappear after a write commits. Keep its exact retry
  // packet across an outage; the caller changes activationKey on auth loss.
  const key = JSON.stringify([workspaceId, humanId, pageId, activationKey])
  const latest = useRef(input); latest.current = input
  const current = useRef(key); current.current = key
  const inFlight = useRef<AbortController | undefined>(undefined)
  useEffect(() => {
    current.current = key
    return () => { if (current.current === key) current.current = ""; inFlight.current?.abort() }
  }, [key])
  const request = useRef<{ key: string; packet: RestorePacket; busy: boolean } | undefined>(undefined)
  const [result, setResult] = useState<{ key: string; status: RestoreStatus }>({ key, status: "idle" })
  const status = result.key === key ? result.status : "idle"
  const allowed = enabled === true && nonempty(workspaceId) && nonempty(humanId) && identifier(pageId) && nonempty(activationKey)
    && !blocked && head?.page_id === pageId && head.can_write === true && revision(head.rev) && head.rev < Number.MAX_SAFE_INTEGER && supported(head.schema)
    && typeof head.epoch === "string" && head.epoch.length > 0 && head.epoch.length <= 256
    && snapshot.status === "ready" && snapshot.scopeKey === input.historyScopeKey && snapshot.data.page_id === pageId && identifier(snapshot.data.id) && supported(snapshot.data.schema)
  const run = useCallback(async (captured: { key: string; packet: RestorePacket; busy: boolean }) => {
    if (current.current !== captured.key || captured.busy) return
    captured.busy = true
    setResult({ key: captured.key, status: "pending" })
    const controller = new AbortController(); inFlight.current = controller
    const timer = setTimeout(() => controller.abort(), 30000)
    try {
      latest.current.onStart()
      const response = await apiFetch(`/api/v1/pages/${encodeURIComponent(`@${captured.packet.page_id}`)}/document/restore?${new URLSearchParams({ workspace_id: workspaceId })}`, {
        method: "POST", cache: "no-store", signal: controller.signal, headers: { "Content-Type": "application/json" }, body: JSON.stringify(captured.packet),
      })
      if (current.current !== captured.key) return
      if (!response.ok) {
        if ([401, 403, 404].includes(response.status)) throw new HistoryFailure("denied")
        if ([400, 409, 413, 422, 429].includes(response.status)) throw new HistoryFailure("error")
        throw new Error("unconfirmed restore")
      }
      const receipt: unknown = await response.json()
      if (current.current !== captured.key) return
      if (!object(receipt) || receipt.page_id !== captured.packet.page_id || receipt.id !== captured.packet.id || receipt.rev !== captured.packet.ifRev + 1
        || receipt.kind !== "restore" || typeof receipt.epoch !== "string" || !receipt.epoch || receipt.epoch === captured.packet.epoch || receipt.epoch.length > 256
        || (receipt.schema !== undefined && (typeof receipt.schema !== "number" || ![1, 2, 3].includes(receipt.schema)))) throw new Error("unconfirmed restore")
      request.current = undefined
      setResult({ key: captured.key, status: "idle" })
      latest.current.onRestored()
    } catch (error) {
      if (current.current !== captured.key) return
      const next = error instanceof HistoryFailure ? error.status === "denied" ? "denied" : "error" : "uncertain"
      if (next !== "uncertain") { request.current = undefined; latest.current.onRefused(next === "denied") }
      setResult({ key: captured.key, status: next })
    } finally { clearTimeout(timer); captured.busy = false }
  }, [workspaceId])
  const onConfirm = () => {
    if (!allowed || request.current?.key === key || status === "pending" || status === "uncertain" || status === "denied" || !head || snapshot.status !== "ready") return
    const captured = { key, packet: { page_id: pageId, id: crypto.randomUUID(), schema: head.schema, epoch: head.epoch, ifRev: head.rev, checkpoint: snapshot.data.id }, busy: false }
    request.current = captured
    void run(captured)
  }
  const onRetry = () => { const captured = request.current; if (enabled === true && captured?.key === key && status === "uncertain") void run(captured) }
  return { allowed: Boolean(allowed), baseKey: JSON.stringify([head?.page_id, head?.rev, head?.epoch, head?.schema]), status, onConfirm, onRetry }
}
