/** Browser editor SDK. Contains no authentication or Crewship dependencies.
 * The host owns durable state; an editor owns its unsubmitted drafts. */
import { MOORING_PROTOCOL, MOORING_MAX_MESSAGE_BYTES, record, revision } from "./channel"

import { createEditorConnection, type EditorEnvironment } from "./editor-connection"
import { createNativeProfile, type NativeEditorSDKOptions } from "./native-profile"
export type { EditorEnvironment } from "./editor-connection"
export type { NativeEditorSDKOptions } from "./native-profile"

export interface DocumentState { rev: number; generation: number; snapshot: unknown }
export type SaveStatus = "connecting" | "saved" | "draft" | "saving" | "offline" | "error"
export interface DurableReceipt { rev: number; outcome: string; issued_by: string; durability: string; [key: string]: unknown }
export interface EditorSDKOptions {
  parentOrigin: string
  bundle: string
  environment: EditorEnvironment
  onState(state: DocumentState): void
  onStatus(status: SaveStatus, message?: string): void
  onReceipt?(receipt: DurableReceipt): void
}
export function durableReceipt(value: unknown, rev: number, issuer?: string): value is DurableReceipt {
  return record(value) && value.rev === rev && value.outcome === "committed" && typeof value.issued_by === "string" && value.issued_by.length > 0 && (issuer === undefined || value.issued_by === issuer) && value.durability === "node"
}
export function createEditorSDK(options: NativeEditorSDKOptions): ReturnType<typeof createNativeProfile>
export function createEditorSDK(options: EditorSDKOptions): ReturnType<typeof createJSONProfile>
export function createEditorSDK(options: EditorSDKOptions | NativeEditorSDKOptions) {
  return "profile" in options ? createNativeProfile(options) : createJSONProfile(options)
}
function createJSONProfile(options: EditorSDKOptions) {
  let state: DocumentState | undefined
  let dirty = false, online = true, failed = false, closed = false, sequence = 0
  const pending = new Map<number, Record<string, unknown>>()
  const receipts = new Map<number, DurableReceipt>()
  function settleReceipts() {
    for (const [seq, receipt] of receipts) {
      if (!state || receipt.rev > state.rev) continue
      receipts.delete(seq); pending.delete(seq); failed = false; options.onReceipt?.(receipt)
    }
  }
  function status(message?: string) {
    options.onStatus(failed ? "error" : !online ? "offline" : !connection.connected || !state ? "connecting" : pending.size ? "saving" : dirty ? "draft" : "saved", message)
  }
  function error(message: string) { failed = true; status(message) }
  function send(message: Record<string, unknown>) { connection.send(JSON.stringify({ ...message, protocol: MOORING_PROTOCOL })) }
  function receive(message: Record<string, unknown>) {
    if (closed) return
    if (message.type === "snapshot" || message.type === "committed") {
      if (!revision(message.rev) || !revision(message.generation) || message.generation < 1 || !("snapshot" in message)) { error("Invalid document state"); return }
      if (state && message.generation !== state.generation) { error("Document generation changed; reopen the editor"); return }
      // An old snapshot or delayed ACK must never roll back a newer push.
      if (!state || message.rev >= state.rev) {
        state = { rev: message.rev, generation: message.generation, snapshot: message.snapshot }
        options.onState(state)
      }
      settleReceipts()
      failed = false; status(); return
    }
    if (message.type === "ack") {
      if (!revision(message.seq) || !revision(message.rev) || !pending.has(message.seq) || !durableReceipt(message.receipt, message.rev)) { error("The server did not confirm this change"); return }
      receipts.set(message.seq, message.receipt)
      settleReceipts(); status(); return
    }
    if (message.type === "rejected") {
      if (!revision(message.seq) || !pending.has(message.seq)) { error("Invalid refusal"); return }
      pending.delete(message.seq); dirty = true; error(typeof message.message === "string" ? message.message : "Change refused; your draft is not saved"); return
    }
    if (message.type === "online" || message.type === "offline") { online = message.type === "online"; status(); return }
    error("Unsupported host message")
  }
  const connection = createEditorConnection({ ...options, namespace: "mooring", onConnect: () => status(), onMessage: message => receive(message), onError: error })
  status()
  return {
    hello() { connection.hello() },
    get state() { return state },
    get pending() { return pending.size },
    draft(value: boolean) { dirty = value; if (!value && !pending.size) failed = false; send({ type: "draft", dirty }); status() },
    command(command: string, args: unknown): boolean {
      if (!connection.connected || !state || !online || pending.size || closed) return false
      const mutation = { type: "command", seq: sequence + 1, base_rev: state.rev, generation: state.generation, command, arguments: args }
      let encoded: string
      try {
        encoded = JSON.stringify({ ...mutation, protocol: MOORING_PROTOCOL })
        if (typeof command !== "string" || !command || !("arguments" in JSON.parse(encoded))) throw new Error("Invalid command")
      } catch { dirty = true; error("This change cannot be encoded; your draft is not saved"); return false }
      if (new TextEncoder().encode(encoded).length > MOORING_MAX_MESSAGE_BYTES) {
        dirty = true; error("This change exceeds the message limit; your draft is not saved"); return false
      }
      sequence++
      pending.set(sequence, mutation); dirty = false; failed = false; if (!connection.send(encoded)) { pending.delete(sequence); dirty = true; status(); return false }
      status(); return true
    },
    close() { closed = true; connection.close() },
  }
}
