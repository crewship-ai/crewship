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

  const list: HistoryRead<DocumentCheckpointList> = !scope.available ? { status: "idle" } : state.scope !== scope ? { status: "loading" } : state.list
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
