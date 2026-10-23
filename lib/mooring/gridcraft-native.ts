/** Small native adapter. Generated and vendored with this same SDK into the
 * GridCraft WASM package; the Rust model retains all cell/undo/draft semantics. */
import { createEditorSDK, type SaveStatus } from "./editor-sdk"
import { createBrowserEditorEnvironment, helloAfterLoad } from "./editor-connection"

export function connectGridCraft(parentOrigin: string, bundle: string, message: (raw: string) => void, status: (state: SaveStatus, reason: string) => void) {
  const deliver = (_value: Record<string, unknown>, raw: string) => message(raw)
  const sdk = createEditorSDK({ parentOrigin, bundle, environment: createBrowserEditorEnvironment(parentOrigin),
    profile: { kind: "native", namespace: "gridcraft", onDocumentOpen: deliver, onConfirmedChange: deliver, onAcknowledged: deliver, onRejected: deliver, onConnectivity: deliver },
    onStatus: (state, reason) => status(state, reason ?? ""),
  })
  const cancelHello = helloAfterLoad(() => sdk.hello())
  return {
    send: (raw: string) => sdk.send(raw),
    reportStatus: (state: SaveStatus, rev: number, reason: string) => sdk.reportStatus(state, rev, reason || undefined),
    close() { cancelHello(); sdk.close() },
  }
}
