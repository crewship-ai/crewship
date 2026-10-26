/** C1 parent transport. Each request uses normal current auth; no socket lease,
 * new authority, optimistic native mutation, or automatic mutation retry. */
import { apiFetch } from "@/lib/api-fetch"
import { getAuthMode } from "@/lib/server-base"
import { record } from "./channel"

import { COMMAND_INPUT_BYTES, COMMAND_MESSAGE_BYTES, commandCanonicalJSON, commandID, commandOwnSelection, exactCommandKeys, parseCommandResponse, type CommandProjection, type CommandResponse } from "./wordcraft-command-contract"
const bytes = (raw: string) => new TextEncoder().encode(raw).length

async function boundedResponse(response: Response): Promise<unknown> {
  if (!response.body) {
    const raw = await response.text()
    if (raw.length > COMMAND_MESSAGE_BYTES || bytes(raw) > COMMAND_MESSAGE_BYTES) throw new Error("response_too_large")
    return JSON.parse(raw)
  }
  const reader = response.body.getReader(), chunks: Uint8Array[] = []
  let size = 0
  try {
    for (;;) {
      const item = await reader.read()
      if (item.done) break
      size += item.value.length
      if (size > COMMAND_MESSAGE_BYTES) throw new Error("response_too_large")
      chunks.push(item.value)
    }
  } catch (error) { await reader.cancel().catch(() => {}); throw error }
  finally { reader.releaseLock() }
  const all = new Uint8Array(size)
  let offset = 0
  for (const chunk of chunks) { all.set(chunk, offset); offset += chunk.length }
  return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(all))
}
export async function commandRequest(workspace: string, page: string, input: { action: "open"; create?: boolean; write?: boolean } | { action: "read" | "submit" | "outcome"; view: string; raw?: string }, signal: AbortSignal): Promise<CommandResponse> {
  if (!commandID(workspace) || !commandID(page) || getAuthMode() !== "cookie") throw new Error("command_unavailable")
  const bounded = new AbortController(), cancel = () => bounded.abort()
  if (signal.aborted) cancel()
  signal.addEventListener("abort", cancel, { once: true })
  const timeout = setTimeout(cancel, 6_000)
  try {
    const response = await apiFetch(`/api/v1/pages/${encodeURIComponent("@" + page)}/mooring/command-session?${new URLSearchParams({ workspace_id: workspace })}`, {
      method: "POST", cache: "no-store", redirect: "error", signal: bounded.signal,
      // ONLY canonical RequireAuth returns401, before entering the C1 handler.
      // Standard refresh may repeat that NOT-ADMITTED request with identical
      // original bytes. No network/5xx/409 mutation retry exists here.
      skipRefresh: false,
      headers: { "content-type": "application/json" }, body: JSON.stringify(input),
    })
    if (!response.ok) throw new Error([401, 403, 404].includes(response.status) ? "access_unavailable" : "command_unconfirmed")
    return parseCommandResponse(await boundedResponse(response))
  } finally { clearTimeout(timeout); signal.removeEventListener("abort", cancel) }
}

export type CommandState = "connecting" | "ready" | "reader" | "pending" | "saved" | "unknown" | "stopped"
export interface CommandSessionCallbacks {
  project(projection: CommandProjection, ownSelection: string | null): void
  state(state: CommandState): void
  stopped(): void
}
/** One serialized original. Unknown retains EXACT raw for explicit SQL lookup;
 * there is no mutation retry, polling outcome, or pending keystroke buffer. */
export function createCommandSession(workspace: string, page: string, opened: CommandResponse, write: boolean, callbacks: CommandSessionCallbacks) {
  let current = opened.projection, original: string | undefined, busy = false, reading = false, stopped = false, awaitingApply = false
  let timer: ReturnType<typeof setTimeout> | undefined, active: AbortController | undefined
  let settled = 0
  const state = (s: CommandState) => { if (!stopped) callbacks.state(s) }
  const stop = () => { if (stopped) return; stopped = true; clearTimeout(timer); active?.abort(); callbacks.state(original ? "unknown" : "stopped"); callbacks.stopped() }
  const qualified = (value: CommandResponse) => {
    if (value.view !== opened.view || value.actor !== opened.actor || value.projection.generation !== current.generation || value.projection.revision < current.revision || value.editor.bundle_digest !== opened.editor.bundle_digest || value.editor.url !== opened.editor.url) throw new Error("view_changed")
  }
  const request = async (action: "read" | "submit" | "outcome", raw?: string) => {
    const abort = new AbortController(); active = abort
    const timeout = setTimeout(() => abort.abort(), action === "read" ? 2_500 : 6_000)
    try { const result = await commandRequest(workspace, page, { action, view: opened.view, ...(raw !== undefined ? { raw } : {}) }, abort.signal); qualified(result); return result }
    finally { clearTimeout(timeout); if (active === abort) active = undefined }
  }
  const poll = async () => {
    if (stopped || busy || original || awaitingApply) return
    busy = true; reading = true
    try {
      const result = await request("read")
      if (stopped || original) return
      if (result.outcome !== null) throw new Error("unexpected_receipt")
      if (result.projection.revision > current.revision) {
        current = result.projection; awaitingApply = true; callbacks.project(current, null)
      } else schedule()
    } catch { if (!stopped && !original) stop() }
    finally { reading = false; if (!original) busy = false }
  }
  const schedule = () => { clearTimeout(timer); if (!stopped && !original && !awaitingApply) timer = setTimeout(() => { void poll() }, 2_000) }
  const accept = async (result: CommandResponse) => {
    if (!original) throw new Error("missing_original")
    const retained = original
    const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(retained)))
    if (stopped) return
    if (original !== retained) throw new Error("original_changed")
    const selection = commandOwnSelection(result, retained, "sha256:" + Array.from(hash, b => b.toString(16).padStart(2, "0")).join(""))
    callbacks.project(result.projection, selection)
    current = result.projection; awaitingApply = true
    // Retain original until the native queued projection has actually applied.
  }
  return {
    start() { state(write ? "ready" : "reader"); schedule() },
    canEdit() { return !stopped && write && (!busy || reading) && !original && !awaitingApply && settled < 256 },
    submit(proposalRaw: string, shownRevision: number) {
      if (stopped || !write || (busy && !reading) || original || awaitingApply || settled >= 256 || shownRevision !== current.revision) return false
      try {
        // An idle read has no mutation to replay. Cancel/ignore it before
        // capturing this one original against the last actually shown cut.
        if (reading) active?.abort()
        if (proposalRaw.length > COMMAND_INPUT_BYTES || bytes(proposalRaw) > COMMAND_INPUT_BYTES) throw new Error("invalid_command")
        const proposal: unknown = JSON.parse(proposalRaw)
        // Registry/catalog/target/parameter semantics belong to the actual
        // native bridge and server allowlist. This transport copies bounded
        // DATA; it never maintains a second command catalog or accepts actor,
        // author, clock, source or permission from the child.
        if (!record(proposal) || !exactCommandKeys(proposal, ["registry_id", "command", "params", "target"]) || !commandID(proposal.registry_id) || !commandID(proposal.command)) throw new Error("invalid_command")
        const op = `${current.generation}:${crypto.randomUUID()}`
        if (!commandID(op)) throw new Error("invalid_identity")
        const raw = commandCanonicalJSON({ op_id: op, generation: current.generation, participant: opened.actor, base_revision: current.revision, target: proposal.target, command: proposal.command, params: proposal.params })
        if (bytes(raw) > COMMAND_INPUT_BYTES) throw new Error("command_too_large")
        original = raw; busy = true; clearTimeout(timer); state("pending")
        void request("submit", raw).then(async result => { if (!stopped) await accept(result) }).catch(error => { if (!stopped) { if (error instanceof Error && error.message === "access_unavailable") stop(); else state("unknown") } }).finally(() => { busy = false })
        return true
      } catch { stop(); return false }
    },
    applied(own: boolean, shownRevision: number) {
      if (stopped || !awaitingApply || shownRevision !== current.revision || (own !== Boolean(original))) { stop(); return }
      awaitingApply = false
      if (own) { original = undefined; settled++; state("saved") }
      schedule()
    },
    async reconcile() {
      if (stopped || busy || !original || awaitingApply) return
      busy = true
      try {
        const result = await request("outcome", original)
        if (stopped) return
        if (!result.outcome) { state("unknown"); return }
        await accept(result)
      } catch (error) { if (!stopped) { if (error instanceof Error && error.message === "access_unavailable") stop(); else state("unknown") } }
      finally { busy = false }
    },
    stop,
    close() { stop() },
  }
}
