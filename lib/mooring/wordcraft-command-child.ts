/** Opaque-origin C1 renderer. No fetch, credentials, module URLs or authority. */
import { bundleDigest, decodeMessage, record } from "./channel"
import { COMMAND_INPUT_BYTES, COMMAND_MESSAGE_BYTES, exactCommandKeys, parseCommandProjection, type CommandProjection } from "./wordcraft-command-contract"
import type { ChildWindow } from "./wordcraft-child"

export interface WordCraftCommandHandle {
  project_confirmed(native: string, ids: string, own: boolean, selection: string): void
  projection_pending(): boolean
  command_pending(): boolean
  stop(): void
}
export interface WordCraftCommandNative {
  start_c1_commands(native: string, ids: string, callbacks: { can_edit(): boolean; command(raw: string): boolean; failed(): void }): WordCraftCommandHandle
}
/** The same capability bootstrap as WordCraft, with a distinct namespace to
 * prevent either producer consuming the other protocol or host authority. */
export function startWordCraftCommandChild(native: WordCraftCommandNative, bundle: string, waitForReady: () => Promise<void>, hostWindow: ChildWindow = window) {
  if (!bundleDigest(bundle)) throw new Error("invalid_bundle")
  let port: MessagePort | undefined, handle: WordCraftCommandHandle | undefined
  let connected = false, stopped = false, booted = false, ready = false, write = false, pending = false, paused = false
  let projection: CommandProjection | undefined
  let applying: { own: boolean; projection: CommandProjection } | undefined
  let timer: ReturnType<typeof setTimeout> | undefined
  const send = (type: string, fields: Record<string, unknown> = {}) => {
    if (!port) throw new Error("missing_port")
    port.postMessage(JSON.stringify({ protocol: 1, type, ...fields }))
  }
  const stop = () => {
    if (stopped) return
    stopped = true; clearTimeout(timer); handle?.stop()
    try { send("wordcraft-command:stopped") } catch { /* keep original native P */ }
  }
  const inspect = () => {
    if (stopped || !handle || !applying) return
    try {
      // Queue consumption is an actual native getter, not an invented RAF or
      // GPU paint acknowledgement. Background tabs retain the one projection.
      if (handle.projection_pending()) { timer = setTimeout(inspect, 25); return }
      if (applying.own && handle.command_pending()) throw new Error("own_projection_not_applied")
      const own = applying.own
      projection = applying.projection; applying = undefined
      if (own) { pending = false; paused = false }
      send("wordcraft-command:applied", { own, revision: projection.revision })
    } catch { stop() }
  }
  const receive = (raw: unknown) => {
    if (stopped) return
    if (typeof raw !== "string" || raw.length > COMMAND_MESSAGE_BYTES) { stop(); return }
    const v = decodeMessage(raw, COMMAND_MESSAGE_BYTES)
    if (!v || JSON.stringify(v) !== raw) { stop(); return }
    try {
      if (v.type === "wordcraft-command:bootstrap") {
        if (booted || !exactCommandKeys(v, ["protocol", "type", "projection", "write"]) || typeof v.write !== "boolean") throw new Error("invalid_bootstrap")
        projection = parseCommandProjection(v.projection); write = v.write; booted = true
        handle = native.start_c1_commands(projection.native_json, JSON.stringify(projection.ids), {
          can_edit: () => ready && write && !stopped && !pending && !paused && !applying,
          command(raw) {
            if (!ready || stopped || !write || pending || paused || applying || !projection || raw.length > COMMAND_INPUT_BYTES || new TextEncoder().encode(raw).length > COMMAND_INPUT_BYTES) return false
            pending = true
            try { send("wordcraft-command:command", { raw, revision: projection.revision }); return true }
            catch { stop(); return false }
          },
          failed: stop,
        })
        if (typeof handle.projection_pending !== "function" || typeof handle.command_pending !== "function") throw new Error("unqualified_native_handle")
        void waitForReady().then(() => { if (!stopped) { ready = true; send("wordcraft-command:ready") } }).catch(stop)
      } else if (v.type === "wordcraft-command:projection") {
        if (!ready || !handle || applying || !projection || !exactCommandKeys(v, ["protocol", "type", "projection", "selection"]) || (v.selection !== null && typeof v.selection !== "string")) throw new Error("invalid_projection")
        const next = parseCommandProjection(v.projection), own = typeof v.selection === "string"
        if (next.generation !== projection.generation || next.revision < projection.revision || (own && !pending) || (own && new TextEncoder().encode(v.selection as string).length > 2048)) throw new Error("invalid_projection")
        // A peer projection cannot acknowledge our original. Normally polling
        // is paused while one exists; retain it even if peer DATA arrives.
        handle.project_confirmed(next.native_json, JSON.stringify(next.ids), own, own ? v.selection as string : "")
        applying = { own, projection: next }; inspect()
      } else if (v.type === "wordcraft-command:pause" && exactCommandKeys(v, ["protocol", "type"])) {
        paused = true
      } else if (v.type === "wordcraft-command:stop" && exactCommandKeys(v, ["protocol", "type"])) stop()
      else throw new Error("invalid_message")
    } catch { stop() }
  }
  const connect = (event: MessageEvent) => {
    if (event.source !== hostWindow.parent || connected || !record(event.data) || !exactCommandKeys(event.data, ["type", "protocol", "bundle"]) || event.data.type !== "wordcraft-command:connect" || event.data.protocol !== 1 || event.data.bundle !== bundle || event.ports.length !== 1) return
    connected = true; port = event.ports[0]; port.onmessage = event => receive(event.data); port.start()
    hostWindow.removeEventListener("message", connect)
  }
  hostWindow.addEventListener("message", connect)
  hostWindow.parent.postMessage({ protocol: 1, type: "wordcraft-command:hello", bundle }, "*")
  return { close() { stop(); hostWindow.removeEventListener("message", connect); if (port) { port.onmessage = null; port.close() } } }
}
