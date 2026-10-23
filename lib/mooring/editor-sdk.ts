/** Browser editor SDK. Contains no authentication or Crewship dependencies.
 * The host owns durable state; an editor owns its unsubmitted drafts. */
import { MOORING_PROTOCOL, bundleDigest, decodeMessage, record, revision, type CapabilityPort, type WindowEnvelope } from "./channel"

export interface DocumentState { rev: number; generation: number; snapshot: unknown }
export type SaveStatus = "connecting" | "saved" | "draft" | "saving" | "offline" | "error"
export interface DurableReceipt { rev: number; outcome: string; issued_by: string; durability: string; [key: string]: unknown }
export interface EditorEnvironment {
  parent: unknown
  listen(handler: (event: WindowEnvelope & { ports?: CapabilityPort[] }) => void): () => void
  hello(message: unknown, targetOrigin: string): void
}
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
export function createEditorSDK(options: EditorSDKOptions) {
  const parentURL = new URL(options.parentOrigin)
  if (parentURL.origin !== options.parentOrigin || !["https:", "http:"].includes(parentURL.protocol) || !bundleDigest(options.bundle)) throw new Error("Invalid host configuration")
  let port: CapabilityPort | undefined, state: DocumentState | undefined
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
    options.onStatus(failed ? "error" : !online ? "offline" : !port || !state ? "connecting" : pending.size ? "saving" : dirty ? "draft" : "saved", message)
  }
  function error(message: string) { failed = true; status(message) }
  function send(message: Record<string, unknown>) { port?.postMessage(JSON.stringify({ ...message, protocol: MOORING_PROTOCOL })) }
  function receive(raw: unknown) {
    if (closed) return
    const message = decodeMessage(raw)
    if (!message) { error("Invalid host message"); return }
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
  const unlisten = options.environment.listen(event => {
    if (closed || event.source !== options.environment.parent || event.origin !== options.parentOrigin) return
    const data = event.data
    if (!record(data) || data.type !== "mooring:connect") return
    if (port || data.protocol !== MOORING_PROTOCOL || event.ports?.length !== 1) { error("Unsupported protocol or duplicate connection"); return }
    port = event.ports[0]; port.onmessage = event => receive(event.data); port.start?.(); status()
  })
  status()
  return {
    hello() { if (!closed) options.environment.hello({ type: "mooring:hello", protocol: MOORING_PROTOCOL, bundle: options.bundle }, options.parentOrigin) },
    get state() { return state },
    get pending() { return pending.size },
    draft(value: boolean) { dirty = value; if (!value && !pending.size) failed = false; send({ type: "draft", dirty }); status() },
    command(command: string, args: unknown): boolean {
      if (!port || !state || !online || pending.size || closed) return false
      const mutation = { type: "command", seq: ++sequence, base_rev: state.rev, generation: state.generation, command, arguments: args }
      pending.set(sequence, mutation); dirty = false; failed = false; send(mutation); status(); return true
    },
    close() { closed = true; unlisten(); if (port) { port.onmessage = null; port.close() } },
  }
}
