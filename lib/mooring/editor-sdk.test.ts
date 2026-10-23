/** Synthetic capability-channel tests; these do not run GridCraft. */
import { describe, expect, it, vi } from "vitest"
import { createEditorSDK, type DocumentState, type SaveStatus } from "./editor-sdk"
import { verifyEditorHello, type CapabilityPort, type WindowEnvelope } from "./channel"
const bundle = "a".repeat(64)
const receipt = (rev: number) => ({ rev, outcome: "committed", issued_by: "crewship", durability: "node" })
function setup() {
  const parent = {}, states: DocumentState[] = [], statuses: SaveStatus[] = [], sent: Record<string, unknown>[] = []
  let listener: (event: WindowEnvelope & { ports?: CapabilityPort[] }) => void = () => {}
  const port: CapabilityPort = { onmessage: null, close: vi.fn(), postMessage(raw) { sent.push(JSON.parse(raw)) } }
  const hello = vi.fn()
  const sdk = createEditorSDK({ bundle, parentOrigin: "https://studio.example.test", environment: { parent, hello, listen(fn) { listener = fn; return vi.fn() } }, onState: value => states.push(value), onStatus: value => statuses.push(value) })
  const connect = (patch: Partial<WindowEnvelope & { ports: CapabilityPort[] }> = {}) => listener({ source: parent, origin: "https://studio.example.test", data: { type: "mooring:connect", protocol: 1 }, ports: [port], ...patch })
  const receive = (data: object) => port.onmessage?.({ data: JSON.stringify({ protocol: 1, ...data }) })
  return { sdk, states, statuses, sent, port, connect, receive, hello }
}
describe("Mooring shared handshake", () => {
  const frame = {}
  const event = { source: frame, origin: "null", data: { type: "mooring:hello", protocol: 1, bundle } }
  const options = { namespace: "mooring", bundle, waiting: true, loads: 1 }
  it("ignores unrelated windows and rejects nonopaque windows, protocol and pin mismatch", () => {
    expect(verifyEditorHello({ ...event, source: {} }, frame, options)).toBeNull()
    expect(verifyEditorHello({ ...event, origin: "https://evil.example" }, frame, options)).toBe("handshake")
    expect(verifyEditorHello({ ...event, data: { ...event.data, protocol: 2 } }, frame, options)).toBe("handshake")
    expect(verifyEditorHello({ ...event, data: { ...event.data, bundle: "b".repeat(64) } }, frame, options)).toBe("bundle")
    expect(verifyEditorHello(event, frame, options)).toBe("accept")
  })
  it("refuses hello before load, duplicate connection, and additional envelope keys", () => {
    expect(verifyEditorHello(event, frame, { ...options, loads: 0 })).toBe("handshake")
    expect(verifyEditorHello(event, frame, { ...options, waiting: false })).toBe("handshake")
    expect(verifyEditorHello({ ...event, data: { ...event.data, extra: true } }, frame, options)).toBe("handshake")
  })
})
describe("Mooring TypeScript editor SDK", () => {
  it("sends only pinned hello and ignores a foreign window or origin", () => {
    const t = setup(); t.sdk.hello()
    expect(t.hello).toHaveBeenCalledWith({ type: "mooring:hello", protocol: 1, bundle }, "https://studio.example.test")
    t.connect({ source: {} }); expect(t.port.onmessage).toBeNull()
    t.connect({ origin: "https://evil.example" }); expect(t.port.onmessage).toBeNull()
    t.connect(); expect(t.port.onmessage).not.toBeNull()
  })
  it("reports unsupported protocol and keeps mutation unavailable", () => {
    const t = setup(); t.connect({ data: { type: "mooring:connect", protocol: 2 } })
    expect(t.statuses.at(-1)).toBe("error")
    expect(t.sdk.command("set", { text: "no" })).toBe(false)
  })
  it("does not call an edit saved before its durable receipt, including a newer draft after send", () => {
    const t = setup(); t.connect(); t.receive({ type: "snapshot", rev: 1, generation: 1, snapshot: { text: "initial" } })
    t.sdk.draft(true); expect(t.statuses.at(-1)).toBe("draft")
    expect(t.sdk.command("set", { text: "first" })).toBe(true); expect(t.statuses.at(-1)).toBe("saving")
    t.receive({ type: "snapshot", rev: 2, generation: 1, snapshot: { text: "first" } }); expect(t.statuses.at(-1)).toBe("saving")
    t.sdk.draft(true)
    t.receive({ type: "ack", seq: 1, rev: 2, receipt: receipt(2) }); expect(t.statuses.at(-1)).toBe("draft")
    t.sdk.draft(false); expect(t.statuses.at(-1)).toBe("saved")
  })
  it("accepts a different host's explicit durable issuer without a Crewship dependency", () => {
    const t = setup(); t.connect(); t.receive({ type: "snapshot", rev: 1, generation: 1, snapshot: {} })
    t.sdk.command("set", {})
    t.receive({ type: "snapshot", rev: 2, generation: 1, snapshot: {} })
    t.receive({ type: "ack", seq: 1, rev: 2, receipt: { ...receipt(2), issued_by: "another-host" } })
    expect(t.statuses.at(-1)).toBe("saved")
  })
  it("holds a receipt until the corresponding confirmed revision is delivered", () => {
    const t = setup(); t.connect(); t.receive({ type: "snapshot", rev: 1, generation: 1, snapshot: {} })
    t.sdk.command("set", { text: "mine" })
    t.receive({ type: "ack", seq: 1, rev: 2, receipt: receipt(2) })
    expect(t.statuses.at(-1)).toBe("saving"); expect(t.sdk.pending).toBe(1)
    t.receive({ type: "snapshot", rev: 2, generation: 1, snapshot: { text: "mine" } })
    expect(t.statuses.at(-1)).toBe("saved"); expect(t.sdk.pending).toBe(0)
  })
  it("rejects an HTTP-success-shaped ACK lacking a durable receipt", () => {
    const t = setup(); t.connect(); t.receive({ type: "snapshot", rev: 1, generation: 1, snapshot: {} })
    t.sdk.command("set", {})
    t.receive({ type: "ack", seq: 1, rev: 2, receipt: { ...receipt(2), durability: "memory" } })
    expect(t.statuses.at(-1)).toBe("error"); expect(t.sdk.pending).toBe(1)
  })
  it("a stale snapshot and older receipt cannot roll back a later confirmed push", () => {
    const t = setup(); t.connect(); t.receive({ type: "snapshot", rev: 1, generation: 1, snapshot: { text: "initial" } })
    t.sdk.command("set", { text: "mine" })
    t.receive({ type: "snapshot", rev: 3, generation: 1, snapshot: { text: "agent" } })
    t.receive({ type: "ack", seq: 1, rev: 2, receipt: receipt(2) })
    t.receive({ type: "snapshot", rev: 2, generation: 1, snapshot: { text: "mine" } })
    expect(t.states.at(-1)?.snapshot).toEqual({ text: "agent" }); expect(t.sdk.state?.rev).toBe(3)
  })
  it("refusal keeps draft unsaved and immediate typing survives a later push", () => {
    const t = setup(); t.connect(); t.receive({ type: "snapshot", rev: 1, generation: 1, snapshot: {} })
    t.sdk.command("set", { text: "mine" }); t.receive({ type: "rejected", seq: 1, message: "Conflict" })
    expect(t.statuses.at(-1)).toBe("error")
    t.sdk.draft(true); t.receive({ type: "snapshot", rev: 2, generation: 1, snapshot: { text: "agent" } })
    expect(t.statuses.at(-1)).toBe("draft"); expect(t.sdk.command("set", { text: "fast typed" })).toBe(true)
    expect(t.sent.at(-1)).toMatchObject({ command: "set", arguments: { text: "fast typed" }, base_rev: 2 })
  })
  it("does not permit offline edits and closes the capability on teardown", () => {
    const t = setup(); t.connect(); t.receive({ type: "snapshot", rev: 1, generation: 1, snapshot: {} })
    t.receive({ type: "offline" }); expect(t.sdk.command("set", {})).toBe(false)
    expect(t.statuses.at(-1)).toBe("offline"); t.sdk.close(); expect(t.port.close).toHaveBeenCalledOnce()
  })
  it("rejects a generation change without replacing the prior confirmed document", () => {
    const t = setup(); t.connect(); t.receive({ type: "snapshot", rev: 3, generation: 1, snapshot: { text: "prior" } })
    t.receive({ type: "snapshot", rev: 4, generation: 2, snapshot: { text: "other" } })
    expect(t.statuses.at(-1)).toBe("error"); expect(t.sdk.state?.snapshot).toEqual({ text: "prior" })
  })
})
