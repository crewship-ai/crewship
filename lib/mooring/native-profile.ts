/** Native application adapter: application-specific reconciliation stays in the
 * native model. Transport, versioning, lifecycle delivery and status callbacks
 * use the same editor client as the JSON reference editor. */
import { createEditorConnection, type EditorEnvironment } from "./editor-connection"
import { revision } from "./channel"
import type { SaveStatus } from "./editor-sdk"
export const NATIVE_SNAPSHOT_LIMIT = 1 << 20
/** Native snapshot body limit plus bounded protocol metadata. */
export const NATIVE_HOST_MESSAGE_LIMIT = NATIVE_SNAPSHOT_LIMIT + (16 << 10)
export interface NativeEditorSDKOptions {
  parentOrigin: string
  bundle: string
  environment: EditorEnvironment
  profile: {
    kind: "native"
    namespace: string
    /** Apply/queue the original wire bytes without converting the document. */
    onDocumentOpen(message: Record<string, unknown>, raw: string): void
    onConfirmedChange(message: Record<string, unknown>, raw: string): void
    onAcknowledged(message: Record<string, unknown>, raw: string): void
    onRejected(message: Record<string, unknown>, raw: string): void
    onConnectivity(message: Record<string, unknown>, raw: string): void
  }
  onStatus(status: SaveStatus, message?: string): void
}
export function createNativeProfile(options: NativeEditorSDKOptions) {
  let status: SaveStatus = "connecting", failure: string | undefined
  let opened = false, appliedRev = 0
  const error = (message: string) => { failure = message; status = "error"; options.onStatus(status, message) }
  const connection = createEditorConnection({ ...options, namespace: options.profile.namespace, maxHostMessageBytes: NATIVE_HOST_MESSAGE_LIMIT,
    onConnect() { options.onStatus("connecting") }, onError: error,
    onMessage(message, raw) {
      switch (message.type) {
        case "snapshot":
          if (!revision(message.rev) || !("snapshot" in message) || new TextEncoder().encode(JSON.stringify(message.snapshot)).length > NATIVE_SNAPSHOT_LIMIT) { error("Invalid document state"); return }
          options.profile.onDocumentOpen(message, raw); break
        case "committed":
          if (!revision(message.rev)) { error("Invalid confirmed revision"); return }
          options.profile.onConfirmedChange(message, raw); break
        case "ack":
          if (!revision(message.seq) || !revision(message.rev)) { error("Invalid acknowledgement"); return }
          options.profile.onAcknowledged(message, raw); break
        case "rejected": options.profile.onRejected(message, raw); break
        case "online": case "offline": options.profile.onConnectivity(message, raw); break
        default: error("Unsupported host message")
      }
    },
  })
  options.onStatus("connecting")
  return {
    hello: () => connection.hello(), close: () => connection.close(),
    /** Native mutation/resume/draft envelopes retain their exact wire bytes. */
    send: (raw: string) => connection.send(raw),
    /** Called only after the native model applied the queued lifecycle events.
     * The adapter remains responsible for its native pending/draft semantics. */
    reportStatus(next: SaveStatus, rev: number, message?: string) {
      if (!revision(rev) || rev < appliedRev) { error("Native revision moved backwards"); return }
      if (!["connecting", "saved", "draft", "saving", "offline", "error"].includes(next)) { error("Invalid native save state"); return }
      if (!connection.connected) { status = "connecting"; options.onStatus(status); return }
      if (next !== "connecting") opened = true
      appliedRev = rev
      // A malformed transport must not be presented as saved by the model.
      status = failure ? "error" : next
      options.onStatus(status, failure ?? message)
    },
    get state() { return { opened, rev: appliedRev, status, error: failure } },
  }
}
