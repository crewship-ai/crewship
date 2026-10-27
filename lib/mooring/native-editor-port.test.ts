import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { createNativeEditorPort, type NativeEditorPortOptions, type NativeStructuredPort } from "./native-editor-port"

beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })
function rig(applicationId: "wordcraft" | "gridcraft" = "wordcraft", accessRequired = false) {
  const port = { postMessage: vi.fn(), onmessage: null, start: vi.fn(), close: vi.fn() } satisfies NativeStructuredPort
  const frame = { postMessage: vi.fn() }, peer = {}
  const options: NativeEditorPortOptions = { applicationId, enginePin: "pinned-engine", bundle: "a".repeat(64), frame, scope: { workspaceId: "w", pageId: "p", baseVersion: "1", generation: "9007199254740993", sessionId: "public-tab" }, createChannel: () => ({ port1: port, port2: peer }), onChanged: vi.fn(), onSaveRequest: vi.fn(), onError: vi.fn() }
  if (accessRequired) options.requireAccessProtocol = true
  const adapter = createNativeEditorPort(options)
  const hello = { source: frame, origin: "null", data: { type: `${applicationId}:hello`, protocol: 1, bundle: options.bundle } }
  const receive = (data: unknown) => (port as NativeStructuredPort).onmessage?.({ data })
  return { port, frame, peer, options, adapter, hello, receive }
}
it("transfers a private port only for the exact isolated editor and bundle", () => {
  const r = rig(); r.adapter.loaded()
  expect(r.adapter.handleWindowMessage({ ...r.hello, source: {} })).toBe(false)
  expect(r.frame.postMessage).not.toHaveBeenCalled()
  expect(r.adapter.handleWindowMessage(r.hello)).toBe(true)
  expect(r.frame.postMessage).toHaveBeenCalledWith({ type: "wordcraft:connect", protocol: 1, bundle: "a".repeat(64) }, "*", [r.peer])
  expect(JSON.stringify(r.frame.postMessage.mock.calls)).not.toContain("public-tab")
  r.adapter.close()
})
const signal = () => new AbortController().signal
const flush = async () => { await Promise.resolve(); await Promise.resolve() }
const bytes = new Uint8Array([1, 2, 255])
function reply(r: ReturnType<typeof rig>, request: Record<string, unknown>, document = "bound", revision?: string, payload = bytes) {
  r.receive({ type: r.options.applicationId === "wordcraft" ? "wordcraft:snapshot-result" : "gridcraft:native-exported", requestId: request.requestId, document, revision: revision ?? (r.options.applicationId === "wordcraft" ? "opaque:9007199254740993:7" : "18446744073709551615"), bytes: payload })
}
function latest(r: ReturnType<typeof rig>) { return r.port.postMessage.mock.calls.at(-1)![0] as Record<string, unknown> }
async function bootstrap(r: ReturnType<typeof rig>) {
  r.adapter.loaded(); r.adapter.handleWindowMessage(r.hello)
  const boot = r.adapter.bootstrap(bytes, signal()); void boot.catch(() => {})
  reply(r, latest(r), "seed"); await vi.waitFor(() => expect(latest(r).type).toBe(`${r.options.applicationId}:open`))
  const open = latest(r); expect(open.type).toBe(`${r.options.applicationId}:open`)
  const count = r.port.postMessage.mock.calls.length; await flush()
  expect(r.port.postMessage).toHaveBeenCalledTimes(count)
  r.receive({ type: `${r.options.applicationId}:opened`, requestId: open.requestId, document: "bound", revision: r.options.applicationId === "wordcraft" ? "opaque-open" : "1" })
  await vi.waitFor(() => expect(latest(r).type).toBe(r.options.applicationId === "wordcraft" ? "wordcraft:snapshot" : "gridcraft:export-native")); reply(r, latest(r))
  const snapshot = await boot; r.port.postMessage.mockClear(); return snapshot
}
it.each(["wordcraft", "gridcraft"] as const)("binds %s only after correlated async import and preserves opaque revision", async applicationId => {
  const r = rig(applicationId); await bootstrap(r)
  const snapshot = r.adapter.snapshot(signal()); void snapshot.catch(() => {})
  const request = latest(r)
  expect(request.type).toBe(applicationId === "wordcraft" ? "wordcraft:snapshot" : "gridcraft:export-native")
  expect(request.requestId).toMatch(/^[0-9a-f-]{36}$/)
  reply(r, request)
  expect(await snapshot).toEqual({ bytes, documentId: "bound", editorRevision: expect.stringContaining(`snapshot:${request.requestId}:`) })
  expect(r.frame.postMessage).toHaveBeenCalledTimes(1); r.adapter.close()
})
it("rejects snapshots before bootstrap and does not treat changed as import completion", async () => {
  const r = rig(); r.adapter.loaded(); r.adapter.handleWindowMessage(r.hello)
  await expect(r.adapter.snapshot(signal())).rejects.toThrow("native_bootstrap_required")
  const boot = r.adapter.bootstrap(bytes, signal()); void boot.catch(() => {})
  reply(r, latest(r), "seed"); await vi.waitFor(() => expect(latest(r).type).toBe(`${r.options.applicationId}:open`))
  const open = latest(r)
  r.receive({ type: "wordcraft:changed", document: "other", revision: "opaque" })
  await flush(); expect(latest(r)).toBe(open); expect(r.options.onChanged).not.toHaveBeenCalled()
  r.adapter.close(); await expect(boot).rejects.toThrow()
})
it.each(["wordcraft", "gridcraft"] as const)("stops %s File New without forwarding a foreign document change", async app => {
  const r = rig(app); await bootstrap(r)
  r.receive(app === "wordcraft" ? { type: "wordcraft:changed", document: "foreign", revision: "opaque" } : { type: "gridcraft:changed", document: "foreign", revision: "2", dirty: true })
  expect(r.options.onChanged).not.toHaveBeenCalled(); expect(r.options.onError).toHaveBeenCalledWith("native_document_changed")
  await expect(r.adapter.snapshot(signal())).rejects.toThrow(); r.adapter.close()
})
it("rejects a foreign snapshot even without a changed event", async () => {
  const r = rig(); await bootstrap(r)
  const snapshot = r.adapter.snapshot(signal()); void snapshot.catch(() => {})
  reply(r, latest(r), "foreign")
  await expect(snapshot).rejects.toThrow("native_document_changed"); expect(r.options.onChanged).not.toHaveBeenCalled()
})
it("requires a new document identity after open", async () => {
  const r = rig(); r.adapter.loaded(); r.adapter.handleWindowMessage(r.hello)
  const boot = r.adapter.bootstrap(bytes, signal()); void boot.catch(() => {})
  reply(r, latest(r), "seed"); await vi.waitFor(() => expect(latest(r).type).toBe(`${r.options.applicationId}:open`))
  r.receive({ type: "wordcraft:opened", requestId: latest(r).requestId, document: "seed", revision: "opaque" })
  await expect(boot).rejects.toThrow("native_bootstrap_identity"); expect(r.port.close).toHaveBeenCalled()
})
it("does not let an uncorrelated opened event authorize bootstrap", async () => {
  const r = rig(); r.adapter.loaded(); r.adapter.handleWindowMessage(r.hello)
  const boot = r.adapter.bootstrap(bytes, signal()); void boot.catch(() => {})
  reply(r, latest(r), "seed"); await vi.waitFor(() => expect(latest(r).type).toBe(`${r.options.applicationId}:open`))
  r.receive({ type: "wordcraft:opened", requestId: "wrong-id", document: "bound", revision: "opaque" })
  await expect(boot).rejects.toThrow("native_port_protocol")
})
it("aborts a pending snapshot and ignores its late response", async () => {
  const r = rig(); await bootstrap(r); const controller = new AbortController()
  const snapshot = r.adapter.snapshot(controller.signal); void snapshot.catch(() => {})
  const request = latest(r); controller.abort(); await expect(snapshot).rejects.toThrow("native_request_aborted")
  reply(r, request); expect(r.options.onError).not.toHaveBeenCalled(); r.adapter.close()
})
it("rejects native payloads above 32MiB", async () => {
  const r = rig(); await bootstrap(r); const snapshot = r.adapter.snapshot(signal()); void snapshot.catch(() => {})
  reply(r, latest(r), "bound", undefined, new Uint8Array(32 * 1024 * 1024 + 1))
  await expect(snapshot).rejects.toThrow("native_port_protocol")
})
it("closes on repeated hello and iframe reload", () => {
  const r = rig(); r.adapter.loaded(); r.adapter.handleWindowMessage(r.hello)
  expect(r.adapter.handleWindowMessage(r.hello)).toBe(false); expect(r.port.close).toHaveBeenCalled()
  const other = rig(); other.adapter.loaded(); other.adapter.handleWindowMessage(other.hello); other.adapter.loaded()
  expect(other.options.onError).toHaveBeenCalledWith("native_editor_reloaded")
})
async function receiptFor(r: ReturnType<typeof rig>, snapshot: Awaited<ReturnType<typeof r.adapter.snapshot>>) {
  const digest = await webcrypto.subtle.digest("SHA-256", new Uint8Array(snapshot.bytes))
  const contentHash = `sha256:${Array.from(new Uint8Array(digest), b => b.toString(16).padStart(2, "0")).join("")}`
  return { ...r.options.scope, applicationId: r.options.applicationId, enginePin: r.options.enginePin, editorRevision: snapshot.editorRevision, contentHash, candidateHash: contentHash, version: "2", issuedBy: "crewship" as const, durability: "node" as const, phase: "committed" as const }
}
it.each(["wordcraft", "gridcraft"] as const)("ACKs %s only for its own committed snapshot request and bound hash", async app => {
  const r = rig(app); await bootstrap(r)
  const pending = r.adapter.snapshot(signal()); const request = latest(r); reply(r, request)
  const snapshot = await pending, receipt = await receiptFor(r, snapshot)
  expect(await r.adapter.ack({ ...receipt, generation: "9007199254740994" })).toBe(false)
  expect(await r.adapter.ack({ ...receipt, contentHash: "sha256:wrong" })).toBe(false)
  expect(await r.adapter.ack({ ...receipt, version: "3" })).toBe(false)
  expect(await r.adapter.ack(receipt)).toBe(true)
  expect(latest(r)).toEqual({ type: app === "wordcraft" ? "wordcraft:saved" : "gridcraft:save-ack", requestId: request.requestId, document: "bound", revision: app === "wordcraft" ? "opaque:9007199254740993:7" : "18446744073709551615" })
  expect(await r.adapter.ack(receipt)).toBe(false); r.adapter.close()
})
it("preserves Word's manual save ticket and fails only that ticket", async () => {
  const r = rig(); await bootstrap(r)
  r.receive({ type: "wordcraft:save-request", requestId: "ticket-a", document: "bound", revision: "opaque", saveAs: false })
  expect(r.options.onSaveRequest).toHaveBeenCalledWith({ requestId: "ticket-a", documentId: "bound", editorRevision: "opaque", saveAs: false })
  const snapshot = r.adapter.snapshot(signal(), "ticket-a"); reply(r, latest(r), "bound", "opaque"); await snapshot
  expect(latest(r).requestId).toBe("ticket-a")
  expect(r.adapter.saveFailed("other", "conflict")).toBe(false)
  expect(r.adapter.saveFailed("ticket-a", "conflict")).toBe(true)
  expect(latest(r)).toEqual({ type: "wordcraft:save-failed", requestId: "ticket-a", error: "conflict" }); r.adapter.close()
})
it("uses Word's self-validated bytes without inventing a validate RPC", async () => {
  const r = rig(), snapshot = await bootstrap(r)
  const draft = { ...r.options.scope, applicationId: r.options.applicationId, enginePin: r.options.enginePin, ...snapshot }
  await expect(r.adapter.validate(draft, signal())).resolves.toBeUndefined()
  expect(r.port.postMessage).not.toHaveBeenCalled()
  await expect(r.adapter.validate({ ...draft, bytes: new Uint8Array([9]) }, signal())).rejects.toThrow("unvalidated_word_snapshot")
  r.adapter.close()
})
it("validates Grid through its existing private native RPC", async () => {
  const r = rig("gridcraft"), snapshot = await bootstrap(r)
  const validation = r.adapter.validate({ ...r.options.scope, applicationId: "gridcraft", enginePin: r.options.enginePin, ...snapshot }, signal())
  const request = latest(r); expect(request.type).toBe("gridcraft:validate-native")
  r.receive({ type: "gridcraft:native-validated", requestId: request.requestId, valid: true, error: "" })
  await expect(validation).resolves.toBeUndefined(); r.adapter.close()
})
it("does not ACK after lifecycle close while hashing", async () => {
  const r = rig(), snapshot = await bootstrap(r), receipt = await receiptFor(r, snapshot)
  r.adapter.close(); expect(await r.adapter.ack(receipt)).toBe(false)
  expect(r.port.postMessage).not.toHaveBeenCalled()
})
it("pins its lifecycle scope even when a host options object is reused", async () => {
  const r = rig(), snapshot = await bootstrap(r), receipt = await receiptFor(r, snapshot)
  r.options.scope.generation = "9007199254740994"
  expect(await r.adapter.ack(receipt)).toBe(true)
  r.adapter.close()
})
it("closes Word's pending managed Save only with the matching committed snapshot ticket", async () => {
  const r = rig(); await bootstrap(r)
  r.receive({ type: "wordcraft:save-request", requestId: "managed-ticket", document: "bound", revision: "opaque:7" })
  const pending = r.adapter.snapshot(signal(), "managed-ticket"); reply(r, latest(r), "bound", "opaque:7")
  const snapshot = await pending
  expect(await r.adapter.ack(await receiptFor(r, snapshot))).toBe(true)
  expect(latest(r)).toEqual({ type: "wordcraft:saved", requestId: "managed-ticket", document: "bound", revision: "opaque:7" })
  expect(r.adapter.saveFailed("managed-ticket", "late failure")).toBe(false); r.adapter.close()
})

it("binds identical Word revisions and bytes to exactly one manual snapshot receipt", async () => {
  const r = rig(); await bootstrap(r)
  const snapshots = []
  for (const requestId of ["ticket-one", "ticket-two"]) {
    r.receive({ type: "wordcraft:save-request", requestId, document: "bound", revision: "same-wire-revision" })
    const pending = r.adapter.snapshot(signal(), requestId); reply(r, latest(r), "bound", "same-wire-revision")
    snapshots.push(await pending)
  }
  expect(snapshots[0].editorRevision).not.toBe(snapshots[1].editorRevision)
  r.port.postMessage.mockClear()
  expect(await r.adapter.ack(await receiptFor(r, snapshots[0]))).toBe(true)
  expect(r.port.postMessage).toHaveBeenCalledExactlyOnceWith({ type: "wordcraft:saved", requestId: "ticket-one", document: "bound", revision: "same-wire-revision" }, undefined)
  r.port.postMessage.mockClear()
  expect(await r.adapter.ack({ ...await receiptFor(r, snapshots[1]), baseVersion: "2", version: "3" })).toBe(true)
  expect(r.port.postMessage).toHaveBeenCalledExactlyOnceWith({ type: "wordcraft:saved", requestId: "ticket-two", document: "bound", revision: "same-wire-revision" }, undefined)
  r.adapter.close()
})
it("rejects a stable wire revision that changes bytes despite fresh RPC identities", async () => {
  const r = rig(); await bootstrap(r)
  const first = r.adapter.snapshot(signal()); reply(r, latest(r), "bound", "stable-wire", new Uint8Array([1])); await first
  const second = r.adapter.snapshot(signal()); void second.catch(() => {})
  reply(r, latest(r), "bound", "stable-wire", new Uint8Array([2]))
  await expect(second).rejects.toThrow("unstable_editor_revision")
})
it("preserves a pending Word ticket proof through many newer autosnapshots", async () => {
  const r = rig(); await bootstrap(r)
  r.receive({ type: "wordcraft:save-request", requestId: "delayed-ticket", document: "bound", revision: "manual-wire" })
  const manual = r.adapter.snapshot(signal(), "delayed-ticket"); reply(r, latest(r), "bound", "manual-wire")
  const receipt = await receiptFor(r, await manual)
  for (let i = 0; i < 260; i++) {
    const snapshot = r.adapter.snapshot(signal()); reply(r, latest(r), "bound", `auto-${i}`); await snapshot
  }
  r.port.postMessage.mockClear()
  expect(await r.adapter.ack(receipt)).toBe(true)
  expect(latest(r)).toEqual({ type: "wordcraft:saved", requestId: "delayed-ticket", document: "bound", revision: "manual-wire" })
  r.adapter.close()
})

it("requires native access capability and a read-only boot before an access-controlled host can open", async () => {
  const r = rig("gridcraft", true)
  r.adapter.loaded(); r.adapter.handleWindowMessage(r.hello)
  await expect(r.adapter.bootstrap(bytes, signal())).rejects.toThrow("native_access_not_ready")
  expect(r.port.postMessage).not.toHaveBeenCalled()
  r.receive({ type: "gridcraft:ready", version: "0.12.1" })
  expect(r.options.onError).toHaveBeenCalledWith("native_access_protocol_required")
  const writable = rig("gridcraft", true)
  writable.adapter.loaded(); writable.adapter.handleWindowMessage(writable.hello)
  writable.receive({ type: "gridcraft:ready", version: "0.12.1", accessProtocol: 1, readOnly: false })
  expect(writable.options.onError).toHaveBeenCalledWith("native_access_boot_writable")
})
it("waits for a correlated applied access reply and refuses an unbound unlock", async () => {
  const r = rig("gridcraft", true)
  r.adapter.loaded(); r.adapter.handleWindowMessage(r.hello)
  r.receive({ type: "gridcraft:ready", version: "0.12.1", accessProtocol: 1, readOnly: true })
  await expect(r.adapter.setReadOnly(false, signal())).rejects.toThrow("native_bootstrap_required")
  // bootstrap helper supplies the single initial load/handshake itself.
  const b = rig("gridcraft")
  await bootstrap(b)
  b.receive({ type: "gridcraft:ready", version: "0.12.1", accessProtocol: 1, readOnly: true })
  const applying = b.adapter.setReadOnly(false, signal())
  let settled = false; void applying.then(() => { settled = true })
  const request = latest(b)
  expect(request).toEqual({ type: "gridcraft:set-access", requestId: expect.any(String), readOnly: false })
  await flush(); expect(settled).toBe(false)
  b.receive({ type: "gridcraft:access", requestId: request.requestId, readOnly: false })
  await expect(applying).resolves.toBe(false)
  r.adapter.close(); b.adapter.close()
})
it("closes on mismatched native applied access instead of continuing with an uncertain editor", async () => {
  const r = rig("gridcraft"); await bootstrap(r)
  r.receive({ type: "gridcraft:ready", version: "0.12.1", accessProtocol: 1, readOnly: true })
  const request = r.adapter.setReadOnly(false, signal()); void request.catch(() => {})
  r.receive({ type: "gridcraft:access", requestId: latest(r).requestId, readOnly: true })
  await expect(request).rejects.toThrow("native_access_mismatch")
  expect(r.options.onError).toHaveBeenCalledWith("native_access_mismatch")
  expect(r.port.close).toHaveBeenCalled()
})
it("retires the private port on an aborted access transition and never reuses its late ACK", async () => {
  const r = rig("gridcraft"); await bootstrap(r)
  r.receive({ type: "gridcraft:ready", version: "0.12.1", accessProtocol: 1, readOnly: true })
  const abort = new AbortController()
  const changing = r.adapter.setReadOnly(false, abort.signal); void changing.catch(() => {})
  const request = latest(r); abort.abort()
  await expect(changing).rejects.toThrow("native_access_unconfirmed")
  r.receive({ type: "gridcraft:access", requestId: request.requestId, readOnly: false })
  expect(r.port.close).toHaveBeenCalled()
  await expect(r.adapter.snapshot(signal())).rejects.toThrow()
})
it("bounds access transitions and retires a timed-out unlock", async () => {
  const r = rig("gridcraft"); await bootstrap(r)
  r.receive({ type: "gridcraft:ready", version: "0.12.1", accessProtocol: 1, readOnly: true })
  vi.useFakeTimers()
  const changing = r.adapter.setReadOnly(false, signal()); void changing.catch(() => {})
  const request = latest(r)
  await expect(r.adapter.setReadOnly(true, signal())).rejects.toThrow("native_access_unavailable")
  expect(latest(r)).toBe(request)
  await vi.advanceTimersByTimeAsync(10_001)
  await expect(changing).rejects.toThrow("native_access_unconfirmed")
  expect(r.options.onError).toHaveBeenCalledWith("native_access_unconfirmed")
  expect(r.port.close).toHaveBeenCalled()
})
it("keeps a Word manual save ticket when a failure message exceeds the native UTF-8 limit", async () => {
  const r = rig(); await bootstrap(r)
  r.receive({ type: "wordcraft:save-request", requestId: "utf8-ticket", document: "bound", revision: "opaque:7" })
  expect(r.adapter.saveFailed("utf8-ticket", "漢".repeat(512))).toBe(false)
  expect(r.port.postMessage).not.toHaveBeenCalled()
  expect(r.adapter.saveFailed("utf8-ticket", "storage unavailable")).toBe(true)
  expect(latest(r)).toEqual({ type: "wordcraft:save-failed", requestId: "utf8-ticket", error: "storage unavailable" })
  r.adapter.close()
})
