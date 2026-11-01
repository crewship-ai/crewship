/** Parent-only WordCraft connection. No credential or socket is transferred to
 * the opaque editor. Receipt checks use the release's Rust/WASM boundary. */
import { apiFetch } from "@/lib/api-fetch"
import { getAuthMode, resolveWsBase } from "@/lib/server-base"
import { record } from "./channel"

export const WORDCRAFT_FRAME_BYTES = 262144
export const WORDCRAFT_BOOTSTRAP_BYTES = 2097152
const encoder = new TextEncoder()
const identifier = (value: unknown): value is string => typeof value === "string" && value.length > 0 && [...value].length <= 200 && !value.includes("\0")
const counter = (value: unknown): value is string => typeof value === "string" && /^(0|[1-9][0-9]{0,19})$/.test(value) && BigInt(value) <= 18446744073709551615n
const origin = (value: unknown): value is string => counter(value) && BigInt(value) > 0n && BigInt(value) < 9007199254740992n
const tuple = (value: unknown) => record(value) && value.protocol_version === "0.4.0" && value.model_version === "0.2.0" && value.engine_version === "0.10.0"
const parse = (raw: unknown, limit = WORDCRAFT_FRAME_BYTES): Record<string, unknown> => {
  if (typeof raw !== "string" || raw.length > limit || encoder.encode(raw).length > limit) throw new Error("invalid_frame")
  const value: unknown = JSON.parse(raw)
  if (!record(value)) throw new Error("invalid_frame")
  return value
}
export interface WordCraftPendingReceipt {
  original_request_json(): string
  check_host_receipt(raw: string): string
  free(): void
}
export interface WordCraftReceiptSDK {
  WordCraftPendingCommit: new (joined: string, checkpoint: string, original: string) => WordCraftPendingReceipt
}
export interface WordCraftOpening {
  session_id: string
  participant_ref: string
  connection_ref: string
  ticket: string
  origin: string
  checkpoint_revision: string
  sdk_prefix: string
  versions: Record<string, unknown>
  editor: { url: string; bundle_digest: string }
}
/** The path is chosen from original host scope, never a server/iframe URL. */
export async function openWordCraftSession(workspaceId: string, pageId: string, write: boolean, signal: AbortSignal, checkpointBase64?: string): Promise<WordCraftOpening> {
  if (!identifier(workspaceId) || !identifier(pageId) || getAuthMode() !== "cookie") throw new Error("session_unavailable")
  const query = new URLSearchParams({ workspace_id: workspaceId })
  const response = await apiFetch(`/api/v1/pages/${encodeURIComponent("@" + pageId)}/mooring/session?${query}`, {
    method: "POST", cache: "no-store", skipRefresh: true, signal,
    headers: { "content-type": "application/json" }, body: JSON.stringify({ write, ...(checkpointBase64 ? { checkpoint_base64: checkpointBase64 } : {}) }),
  })
  if (!response.ok) throw new Error([401, 403, 404].includes(response.status) ? "access_unavailable" : "session_unavailable")
  const body = parse(await response.text())
  if (![body.session_id, body.participant_ref, body.connection_ref, body.ticket].every(identifier)
    || !origin(body.origin) || !counter(body.checkpoint_revision) || !tuple(body.versions)
    || typeof body.sdk_prefix !== "string" || !/^\/mooring-sdk\/generated\/[0-9a-f]{64}$/.test(body.sdk_prefix)
    || !record(body.editor) || typeof body.editor.url !== "string" || !/^[0-9a-f]{64}$/.test(String(body.editor.bundle_digest))) throw new Error("editor_unavailable")
  return body as unknown as WordCraftOpening
}
export async function loadWordCraftReceiptSDK(prefix: string): Promise<WordCraftReceiptSDK> {
  if (!/^\/mooring-sdk\/generated\/[0-9a-f]{64}$/.test(prefix)) throw new Error("invalid_sdk")
  // Only the authenticated release-selected content-addressed path reaches this
  // import. The child cannot choose a module or invoke this loader.
  const url = new URL(`${prefix}/mooring_wordcraft_client.js`, window.location.origin).href
  const loaded: unknown = await import(/* webpackIgnore: true */ url)
  if (!record(loaded) || typeof loaded.default !== "function" || typeof loaded.WordCraftPendingCommit !== "function") throw new Error("invalid_sdk")
  await loaded.default({ module_or_path: new URL(`${prefix}/mooring_wordcraft_client_bg.wasm`, window.location.origin).href })
  return loaded as unknown as WordCraftReceiptSDK
}
export type WordCraftHostState = "connecting" | "ready" | "draft" | "pending" | "saved" | "unknown" | "unsaved-draft" | "stopped"
export interface WordCraftBootstrap { joined_json: string; checkpoint_json: string; assigned_origin: string; access: "writer" | "reader" }
export interface WordCraftHostCallbacks {
  bootstrap(value: WordCraftBootstrap): void
  hostFrame(raw: string): void
  state(value: WordCraftHostState): void
  stopped(reason: string): void
}
/** Exactly one original browser connection, bounded original packets, no
 * automatic reconnect that could relabel old mutations with a new authority. */
export function connectWordCraftHost(workspaceId: string, pageId: string, opening: WordCraftOpening, write: boolean, sdk: WordCraftReceiptSDK, callbacks: WordCraftHostCallbacks) {
  const scope = { workspace_id: workspaceId, artifact_id: pageId, application_id: "wordcraft" }
  const sameScope = (value: unknown) => record(value) && value.workspace_id === workspaceId && value.artifact_id === pageId && value.application_id === "wordcraft"
  const query = new URLSearchParams({ workspace_id: workspaceId, connection_ref: opening.connection_ref })
  const socket = new WebSocket(`${resolveWsBase()}/api/v1/pages/${encodeURIComponent("@" + pageId)}/mooring/ws?${query}`)
  const joinRequest = crypto.randomUUID(), checkpointRequest = crypto.randomUUID()
  let joined = "", checkpoint = "", generation = "", stopped = false, bootstrapped = false, childReady = false, draftEpoch = 0, confirmedEpoch = 0, pendingBytes = 0
  let renewalTimer: ReturnType<typeof setTimeout> | undefined
  let renewalAbort: AbortController | undefined
  const pending = new Map<string, { receipt: WordCraftPendingReceipt; raw: string; epoch: number; changeIds: readonly string[] }>()
  const settled = new Map<string, string>()
  let settledBytes = 0
  // Semantic identities live for this ORIGINAL source, including after Saved.
  // A new transport request cannot assign an old operation a later draft epoch.
  const operationIds = new Set<string>(), changeIds = new Set<string>()
  const state = (value: WordCraftHostState) => { if (!stopped) callbacks.state(value) }
  const stop = (reason: string) => {
    if (stopped) return
    stopped = true
    clearTimeout(renewalTimer); renewalAbort?.abort()
    socket.close(); callbacks.state(pending.size || draftEpoch > confirmedEpoch ? "unsaved-draft" : "stopped"); callbacks.stopped(reason)
  }
  // Revalidate this same original admission before its five-minute horizon.
  // No new WebSocket, bootstrap, producer, command or Saved acknowledgement.
  const renew = async () => {
    if (stopped) return
    const abort = new AbortController()
    renewalAbort = abort
    const timeout = setTimeout(() => { abort.abort(); stop("access_unavailable") }, 12_000)
    try {
      const response = await apiFetch(`/api/v1/pages/${encodeURIComponent("@" + pageId)}/mooring/session/reauthorize?${new URLSearchParams({ workspace_id: workspaceId })}`, {
        method: "POST", cache: "no-store", skipRefresh: true, redirect: "error", signal: abort.signal,
        headers: { "content-type": "application/json" }, body: JSON.stringify({ connection_ref: opening.connection_ref }),
      })
      if (stopped) return
      if (response.status !== 204) { stop("access_unavailable"); return }
      renewalTimer = setTimeout(() => { void renew() }, 240_000)
    } catch { if (!stopped) stop("access_unavailable") }
    finally { clearTimeout(timeout); if (renewalAbort === abort) renewalAbort = undefined }
  }
  renewalTimer = setTimeout(() => { void renew() }, 240_000)
  const send = (raw: string) => {
    if (stopped || socket.readyState !== WebSocket.OPEN || encoder.encode(raw).length > WORDCRAFT_FRAME_BYTES) throw new Error("connection_unavailable")
    socket.send(raw)
  }
  socket.onopen = () => { try { send(JSON.stringify({ type: "session_join", request_id: joinRequest, ticket: opening.ticket, versions: opening.versions })) } catch { stop("connection_unavailable") } }
  // 4009: aggregate artifact storage is full (the Design editor's code). Nothing
  // was saved; the unsaved local draft is retained like any other stop.
  socket.onclose = (event?: CloseEvent) => stop(event?.code === 4009 ? "storage_full" : "connection_unavailable")
  socket.onerror = () => stop("connection_unavailable")
  socket.onmessage = event => {
    if (stopped) return
    try {
      const frame = parse(event.data)
      if (!joined) {
        if (frame.type !== "session_joined" || frame.request_id !== joinRequest || !sameScope(frame.scope) || frame.session_id !== opening.session_id || frame.participant_ref !== opening.participant_ref || !tuple(frame.versions) || !counter(frame.generation)) throw new Error("invalid_join")
        joined = event.data; generation = frame.generation
        send(JSON.stringify({ type: "checkpoint_request", request_id: checkpointRequest, scope, revision: opening.checkpoint_revision }))
        return
      }
      if (!bootstrapped) {
        if (frame.type !== "checkpoint_result" || frame.request_id !== checkpointRequest || !record(frame.checkpoint) || !sameScope(frame.checkpoint.scope) || !tuple(frame.checkpoint.versions) || !counter(frame.checkpoint.revision) || frame.checkpoint.revision !== opening.checkpoint_revision) throw new Error("invalid_checkpoint")
        checkpoint = JSON.stringify(frame.checkpoint)
        const value: WordCraftBootstrap = { joined_json: joined, checkpoint_json: checkpoint, assigned_origin: opening.origin, access: write ? "writer" : "reader" }
        if (encoder.encode(JSON.stringify({ protocol: 1, type: "wordcraft:bootstrap", ...value })).length > WORDCRAFT_BOOTSTRAP_BYTES) throw new Error("quota_exceeded")
        bootstrapped = true; callbacks.bootstrap(value); return
      }
      if (frame.type === "changes_received") {
        const keys = ["type", "request_id", "scope", "session_id", "change_ids", "state"]
        if (Object.keys(frame).length !== keys.length || Object.keys(frame).some(key => !keys.includes(key))
          || !identifier(frame.request_id) || !sameScope(frame.scope) || Object.keys(frame.scope as object).length !== 3
          || frame.session_id !== opening.session_id || (frame.state !== "pending" && frame.state !== "synchronized")) throw new Error("invalid_pending")
        const original = pending.get(frame.request_id)
        if (!original || !Array.isArray(frame.change_ids) || frame.change_ids.length !== original.changeIds.length
          || frame.change_ids.some((id, index) => id !== original.changeIds[index])) throw new Error("invalid_pending")
        // This is receipt of the original group, never a durable acknowledgement.
        // Preserve a newer unsent Draft and keep the original packet retryable.
        return
      }
      if (frame.type === "commit_result") {
        if (!identifier(frame.request_id)) throw new Error("invalid_receipt")
        const original = pending.get(frame.request_id)
        if (!original) {
          if (settled.get(frame.request_id) === event.data) return
          throw new Error("invalid_receipt")
        }
        // Network provenance is THIS socket callback, never an iframe flag.
        original.receipt.check_host_receipt(event.data)
        const changes = frame.changes
        if (!Array.isArray(changes) || changes.some(change => !record(change) || change.status !== "saved")) {
          callbacks.hostFrame(event.data); stop("change_rejected"); return
        }
        callbacks.hostFrame(event.data)
        pending.delete(frame.request_id); pendingBytes -= encoder.encode(original.raw).length
        original.receipt.free(); settled.set(frame.request_id, event.data)
        settledBytes += encoder.encode(event.data).length
        while (settled.size > 256 || settledBytes > WORDCRAFT_FRAME_BYTES) {
          const oldest = settled.keys().next().value!
          settledBytes -= encoder.encode(settled.get(oldest)!).length; settled.delete(oldest)
        }
        confirmedEpoch = Math.max(confirmedEpoch, original.epoch)
        state(pending.size ? "pending" : draftEpoch > confirmedEpoch ? "draft" : "saved"); return
      }
      if (frame.type === "document_update" && sameScope(frame.scope) && frame.session_id === opening.session_id && frame.generation === generation && counter(frame.from_revision) && counter(frame.to_revision) && BigInt(frame.from_revision) <= BigInt(frame.to_revision)) {
        callbacks.hostFrame(event.data); return // peer receipt never acknowledges OUR pending packet
      }
      throw new Error("invalid_host_frame")
    } catch { stop("unqualified_host_frame") }
  }
  return {
    ready() { if (!bootstrapped || childReady || stopped) { stop("invalid_editor_sequence"); return }; childReady = true; state("ready") },
    draft() { if (!childReady || stopped) { stop("invalid_editor_sequence"); return }; draftEpoch++; state("draft") },
    submit(raw: string) {
      if (!childReady || !write || stopped) { stop("invalid_editor_sequence"); return }
      try {
        const request = parse(raw)
        if (request.type !== "submit_changes" || !identifier(request.request_id) || !sameScope(request.scope) || request.session_id !== opening.session_id || !tuple(request.versions)) throw new Error("invalid_original")
        const old = pending.get(request.request_id)
        if (old) { if (old.raw !== raw) throw new Error("original_changed"); send(old.raw); return }
        if (settled.has(request.request_id) || pending.size >= 256 || pendingBytes + encoder.encode(raw).length > WORDCRAFT_FRAME_BYTES) throw new Error("quota_exceeded")
        const receipt = new sdk.WordCraftPendingCommit(joined, checkpoint, raw)
        if (receipt.original_request_json() !== raw) { receipt.free(); throw new Error("original_changed") }
        // The Rust constructor above checks schema/fingerprints/membership. This
        // additional source-lifetime namespace prevents relabeling a known
        // Saved atomic/op under a new public request ID to clear a newer Draft.
        const nextChanges: string[] = [], nextOperations: string[] = []
        try {
          if (!Array.isArray(request.changes)) throw new Error("invalid_original")
          for (const change of request.changes) {
            if (!record(change) || !identifier(change.change_id) || !Array.isArray(change.operations)) throw new Error("invalid_original")
            nextChanges.push(change.change_id)
            for (const operation of change.operations) {
              if (!record(operation) || !identifier(operation.op_id)) throw new Error("invalid_original")
              nextOperations.push(operation.op_id)
            }
          }
          if (!nextChanges.length || !nextOperations.length || nextChanges.some(id => changeIds.has(id)) || nextOperations.some(id => operationIds.has(id))
            || changeIds.size + nextChanges.length > 256 || operationIds.size + nextOperations.length > 256) throw new Error("original_changed")
        } catch (error) { receipt.free(); throw error }
        for (const id of nextChanges) changeIds.add(id)
        for (const id of nextOperations) operationIds.add(id)
        pending.set(request.request_id, { receipt, raw, epoch: draftEpoch, changeIds: Object.freeze(nextChanges) }); pendingBytes += encoder.encode(raw).length
        state("pending"); send(raw)
      } catch { stop("unqualified_original") }
    },
    /** Exact bytes of every submitted original not yet confirmed Saved. */
    pendingOriginals(): string[] { return [...pending.values()].map(item => item.raw) },
    stop,
    close() { stop("closed"); socket.onopen = null; socket.onmessage = null; socket.onclose = null; socket.onerror = null; for (const item of pending.values()) item.receipt.free() },
  }
}
