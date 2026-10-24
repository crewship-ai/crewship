/** Synthetic transport + native adapter boundary, not a GridCraft engine run. */
import { expect, it, vi } from "vitest"
import { createEditorSDK } from "./editor-sdk"
import type { CapabilityPort, WindowEnvelope } from "./channel"
const bundle = "a".repeat(64)
function native() {
  const parent = {}, hello = vi.fn(), delivered = vi.fn(), onStatus = vi.fn(), sent: string[] = []
  let listener: (event: WindowEnvelope & { ports?: CapabilityPort[] }) => void = () => {}
  const port: CapabilityPort = { onmessage: null, close: vi.fn(), start() {}, postMessage(raw) { sent.push(raw) } }
  const sdk = createEditorSDK({ parentOrigin: "https://studio.example.test", bundle,
    environment: { parent, hello, listen(fn: typeof listener) { listener = fn; return () => {} } },
    onStatus, profile: { kind: "native", namespace: "gridcraft", onDocumentOpen: delivered, onConfirmedChange: delivered, onAcknowledged: delivered, onRejected: delivered, onConnectivity: delivered },
  })
  const connect = (patch: Partial<WindowEnvelope> = {}) => listener({ source: parent, origin: "https://studio.example.test", data: { type: "gridcraft:connect", protocol: 1 }, ports: [port], ...patch })
  const receive = (value: object) => { const raw = JSON.stringify({ protocol: 1, ...value }); port.onmessage?.({ data: raw }); return raw }
  return { sdk, parent, hello, delivered, onStatus, sent, port, connect, receive }
}
it("executes the same editor client with the native wire namespace and exact parent authority", () => {
  const t = native(); t.sdk.hello()
  expect(t.hello).toHaveBeenCalledWith({ type: "gridcraft:hello", protocol: 1, bundle }, "https://studio.example.test")
  t.connect({ source: {} }); expect(t.port.onmessage).toBeNull()
  t.connect({ origin: "https://foreign.example.test" }); expect(t.port.onmessage).toBeNull()
  t.connect(); expect(t.port.onmessage).not.toBeNull()
})
it("delivers native opening, confirmed changes, acknowledgements and refusals unchanged", () => {
  const t = native(); t.connect()
  for (const value of [{ type: "snapshot", rev: 1, generation: 1, snapshot: { sheets: [] }, discarded: [] }, { type: "committed", rev: 2, actor_label: "Agent", ops: [] }, { type: "ack", seq: 1, rev: 2 }, { type: "rejected", seq: 2, code: "conflict", current_rev: 2, cells: [] }, { type: "offline" }]) {
    const raw = t.receive(value)
    expect(t.delivered).toHaveBeenLastCalledWith(JSON.parse(raw), raw)
  }
})
it("sends native mutation, resume and draft bytes without translating cell semantics", () => {
  const t = native(); t.connect()
  for (const value of [{ type: "mutation", protocol: 1, seq: 1, base_rev: 1, ops: [{ op: "cell.set", sheet: "Sheet1", cell: "B2", input: "draft" }] }, { type: "resume", protocol: 1, last_rev: 1, pending: [] }, { type: "draft", protocol: 1, cells: [{ sheet: "Sheet1", cell: "B2" }] }]) {
    const raw = JSON.stringify(value); expect(t.sdk.send(raw)).toBe(true); expect(t.sent.at(-1)).toBe(raw)
  }
})
it("keeps native save state with the adapter and reports truthful lifecycle errors", () => {
  const t = native(); t.sdk.reportStatus("saved", 1); expect(t.sdk.state.status).toBe("connecting")
  t.connect(); t.receive({ type: "snapshot", rev: 0, generation: 1, snapshot: {}, discarded: [] })
  t.sdk.reportStatus("saved", 0); expect(t.sdk.state).toMatchObject({ status: "saved", rev: 0 })
  t.sdk.reportStatus("draft", 1); expect(t.onStatus).toHaveBeenLastCalledWith("draft", undefined)
  t.sdk.reportStatus("saved", 2); expect(t.sdk.state).toMatchObject({ status: "saved", rev: 2 })
  t.sdk.reportStatus("saved", 1); expect(t.onStatus).toHaveBeenLastCalledWith("error", "Native revision moved backwards")
  t.sdk.close(); expect(t.port.close).toHaveBeenCalledOnce(); expect(t.sdk.send('{"type":"draft","protocol":1,"cells":[]}')).toBe(false)
})
it("rejects a foreign protocol rather than installing its port", () => {
  const t = native(); t.connect({ data: { type: "gridcraft:connect", protocol: 2 } }); expect(t.port.onmessage).toBeNull(); expect(t.onStatus).toHaveBeenLastCalledWith("error", "Unsupported protocol or duplicate connection")
})
it("preserves native snapshots above the small-message limit, with the exact one-MiB body cap", () => {
  const t = native(); t.connect()
  const snapshot = { text: "x".repeat((1 << 20) - 11) }
  expect(new TextEncoder().encode(JSON.stringify(snapshot)).length).toBe(1 << 20)
  t.receive({ type: "snapshot", rev: 1, generation: 1, snapshot, discarded: [] })
  expect(t.delivered).toHaveBeenCalledOnce()
  t.receive({ type: "snapshot", rev: 2, generation: 1, snapshot: { text: snapshot.text + "x" }, discarded: [] })
  expect(t.delivered).toHaveBeenCalledOnce()
  expect(t.onStatus).toHaveBeenLastCalledWith("error", "Invalid document state")
})
it("native outbound messages keep the original 64-KiB transport limit", () => {
  const t = native(); t.connect()
  expect(t.sdk.send(JSON.stringify({ protocol: 1, type: "mutation", input: "x".repeat(64 << 10) }))).toBe(false)
  expect(t.sent).toHaveLength(0)
})
it("does not accept the JSON profile's namespace on a native editor connection", () => {
  const t = native(); t.connect({ data: { type: "mooring:connect", protocol: 1 } })
  expect(t.port.onmessage).toBeNull(); expect(t.sdk.send('{"type":"draft","protocol":1,"cells":[]}')).toBe(false)
})
