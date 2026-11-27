// The host's editor port interface on GridCraft protocol 2: the save coordinator, lease and
// journal keep working unchanged while the editor opens and saves the workbook file itself.
import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { createGridcraftNativePort } from "./gridcraft-native-port"
import type { NativeEditorPortOptions, NativeStructuredPort } from "./native-editor-port"
import type { NativeSaveReceipt } from "./single-editor-save"

beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => { vi.unstubAllGlobals() })
const bundle = "a".repeat(64)
const file = new Uint8Array([0x50, 0x4b, 3, 4, 7, 7])
function rig() {
  const port = { postMessage: vi.fn(), onmessage: null, start: vi.fn(), close: vi.fn() } satisfies NativeStructuredPort
  const frame = { postMessage: vi.fn() }
  const options: NativeEditorPortOptions = { applicationId: "gridcraft", enginePin: "pinned-engine", bundle, frame, scope: { workspaceId: "w", pageId: "p", baseVersion: "1", generation: "9", sessionId: "s" }, createChannel: () => ({ port1: port, port2: {} }), onChanged: vi.fn(), onSaveRequest: vi.fn(), onError: vi.fn(), requireAccessProtocol: true }
  const onOpened = vi.fn(), onCalculated = vi.fn()
  const adapter = createGridcraftNativePort({ ...options, fileName: "Plan.xlsm", onOpened, onCalculated })
  const receive = (data: unknown) => (port as NativeStructuredPort).onmessage?.({ data })
  adapter.loaded(); adapter.handleWindowMessage({ source: frame, origin: "null", data: { type: "gridcraft:hello", protocol: 2, bundle } })
  receive({ type: "gridcraft:ready", version: "v", accessProtocol: 1, readOnly: true })
  return { port, options, adapter, receive, onOpened, onCalculated }
}
type Rig = ReturnType<typeof rig>
// Contract: changed may carry calculated, true once the editor calculated every value itself.
it("tells the host once that the editor has calculated the whole workbook", async () => {
  const r = rig(); await bootstrap(r)
  r.receive({ type: "gridcraft:changed", document: "doc", revision: "2", dirty: true, calculated: false })
  r.receive({ type: "gridcraft:changed", document: "doc", revision: "3", dirty: true })
  expect(r.onCalculated).not.toHaveBeenCalled()
  r.receive({ type: "gridcraft:changed", document: "doc", revision: "3", dirty: true, calculated: true })
  r.receive({ type: "gridcraft:changed", document: "doc", revision: "4", dirty: true, calculated: true })
  expect(r.onCalculated).toHaveBeenCalledTimes(1)
  expect(r.options.onError).not.toHaveBeenCalled()
})
const signal = () => new AbortController().signal
const latest = (r: Rig) => r.port.postMessage.mock.calls.at(-1)![0] as Record<string, unknown>
const hex = async (bytes: Uint8Array) => `sha256:${Array.from(new Uint8Array(await webcrypto.subtle.digest("SHA-256", bytes)), b => b.toString(16).padStart(2, "0")).join("")}`
async function bootstrap(r: Rig) {
  const boot = r.adapter.bootstrap(file, signal(), 30_000)
  r.receive({ type: "gridcraft:opened", requestId: latest(r).requestId, document: "doc", revision: "2", format: "xlsm", calculated: false, warnings: [{ code: "vba_preserved", count: 1 }] })
  return await boot
}
const saved = (requestId: unknown, revision = "3", bytes = new Uint8Array([1, 2, 3])) => ({ type: "gridcraft:save", requestId, document: "doc", revision, name: "Plan.xlsm", format: "xlsm", bytes: bytes.slice().buffer })
async function receipt(r: Rig, editorRevision: string, bytes: Uint8Array, version = "2"): Promise<NativeSaveReceipt> {
  const contentHash = await hex(bytes)
  return { applicationId: "gridcraft", enginePin: "pinned-engine", workspaceId: "w", pageId: "p", sessionId: "s", generation: "9", baseVersion: String(BigInt(version) - 1n), editorRevision, contentHash, candidateHash: contentHash, version, durability: "node", issuedBy: "crewship", phase: "committed" } as NativeSaveReceipt
}

it("bootstraps by opening the stored file under its own name, without recalculating, and reports the warnings", async () => {
  const r = rig(); const snapshot = await bootstrap(r)
  expect(r.port.postMessage.mock.calls.find(([m]) => (m as { type: string }).type === "gridcraft:open")![0]).toMatchObject({ name: "Plan.xlsm", calculate: false, autoFit: false })
  expect(snapshot).toEqual({ bytes: file, editorRevision: "doc:2", documentId: "doc", revision: "doc:2", nativeState: "doc:2" })
  expect(r.onOpened).toHaveBeenCalledWith({ document: "doc", revision: "2", format: "xlsm", calculated: false, warnings: [{ code: "vba_preserved", count: 1 }] })
  expect(r.port.postMessage.mock.calls.map(([m]) => (m as { type: string }).type)).toEqual(["gridcraft:open"])
})
it("reports changes in the coordinator's revision form", async () => {
  const r = rig(); await bootstrap(r)
  r.receive({ type: "gridcraft:changed", document: "doc", revision: "3", dirty: true })
  expect(r.options.onChanged).toHaveBeenCalledWith("doc:3", "doc")
})
it("takes a host snapshot as the editor's saved file and acknowledges it with the stored version", async () => {
  const r = rig(); await bootstrap(r)
  const snapshot = r.adapter.snapshot(signal()); const request = latest(r)
  expect(request.type).toBe("gridcraft:save-request")
  r.receive(saved(request.requestId))
  const taken = await snapshot
  expect(taken).toEqual({ bytes: new Uint8Array([1, 2, 3]), editorRevision: "doc:3", documentId: "doc", nativeState: "doc:3", format: "xlsm" })
  await expect(r.adapter.validate({ applicationId: "gridcraft", enginePin: "pinned-engine", workspaceId: "w", pageId: "p", sessionId: "s", generation: "9", baseVersion: "1", editorRevision: "doc:3", bytes: taken.bytes }, signal())).resolves.toBeUndefined()
  expect(await r.adapter.ack(await receipt(r, "doc:3", taken.bytes))).toBe(true)
  expect(latest(r)).toEqual({ type: "gridcraft:save-ack", requestId: request.requestId, document: "doc", revision: "3", version: "2" })
})
it("never acknowledges a receipt whose hash is not the file the editor gave", async () => {
  const r = rig(); await bootstrap(r)
  const snapshot = r.adapter.snapshot(signal()); r.receive(saved(latest(r).requestId)); await snapshot
  expect(await r.adapter.ack(await receipt(r, "doc:3", new Uint8Array([9])))).toBe(false)
  const other = await receipt(r, "doc:3", new Uint8Array([1, 2, 3])); other.workspaceId = "elsewhere"
  expect(await r.adapter.ack(other)).toBe(false)
})
it("hands an editor-started save to the coordinator and gives it that same file", async () => {
  const r = rig(); await bootstrap(r)
  r.receive({ type: "gridcraft:save-request", requestId: "editor-7", document: "doc", revision: "4" })
  r.receive(saved("editor-7", "4"))
  expect(r.options.onSaveRequest).toHaveBeenCalledWith({ requestId: "editor-7", editorRevision: "doc:4", documentId: "doc" })
  const before = r.port.postMessage.mock.calls.length
  expect(await r.adapter.snapshot(signal(), "editor-7")).toMatchObject({ editorRevision: "doc:4" })
  expect(r.port.postMessage).toHaveBeenCalledTimes(before)
  await expect(r.adapter.snapshot(signal(), "unknown")).rejects.toThrow("unknown_native_save_ticket")
})
it("fails an editor-started save the host could not store with a contract code", async () => {
  const r = rig(); await bootstrap(r)
  r.receive({ type: "gridcraft:save-request", requestId: "editor-8", document: "doc", revision: "4" }); r.receive(saved("editor-8", "4"))
  expect(r.adapter.saveFailed("editor-8", "Saving was not confirmed.")).toBe(true)
  expect(latest(r)).toEqual({ type: "gridcraft:save-failed", requestId: "editor-8", code: "failed", error: "" })
})
it("settles a second file of an already saved revision before acknowledging", async () => {
  const r = rig(); await bootstrap(r)
  const a = r.adapter.snapshot(signal()); const first = latest(r)
  const b = r.adapter.snapshot(signal()); const second = latest(r)
  r.receive(saved(first.requestId)); r.receive(saved(second.requestId))
  const [taken] = await Promise.all([a, b])
  expect(await r.adapter.ack(await receipt(r, "doc:3", taken.bytes))).toBe(true)
  const types = r.port.postMessage.mock.calls.slice(-2).map(([m]) => m as Record<string, unknown>)
  expect(types).toEqual([{ type: "gridcraft:save-failed", requestId: first.requestId, code: "conflict", error: "" }, { type: "gridcraft:save-ack", requestId: second.requestId, document: "doc", revision: "3", version: "2" }])
})
it("names the editor's revision as its native state, so an untouched workbook is never saved as a rewritten file", async () => {
  // The editor's file of an unedited workbook is not the stored file byte for byte; the coordinator
  // tells it is untouched by the revision mounted at open (as for Word's nativeState).
  const r = rig(); const mounted = await bootstrap(r)
  const snapshot = r.adapter.snapshot(signal()); r.receive(saved(latest(r).requestId, "2"))
  expect((await snapshot).nativeState).toBe(mounted.nativeState)
  r.receive({ type: "gridcraft:changed", document: "doc", revision: "5", dirty: true })
  const edited = r.adapter.snapshot(signal()); r.receive(saved(latest(r).requestId, "5"))
  expect((await edited).nativeState).not.toBe(mounted.nativeState)
})

// The cells the person changed, as the editor named them with its save, go with the snapshot.
it("hands on the cells the person changed with the file the editor saved", async () => {
  const r = rig(); await bootstrap(r)
  const snapshot = r.adapter.snapshot(signal()); r.receive({ ...saved(latest(r).requestId, "2"), touched: ["'L'!B2"] })
  expect((await snapshot).touched).toEqual(["'L'!B2"])
  const silent = r.adapter.snapshot(signal()); r.receive(saved(latest(r).requestId, "2"))
  expect((await silent).touched, "an editor that does not say").toBeUndefined()
})

// Contract v2 addendum: fidelity_loss holds for one revision. Autosave asks once per revision,
// the host hears the parts, and a later save of a newer revision clears it.
const lossy = (requestId: unknown, parts = [{ code: "charts", count: 2 }]) => ({ type: "gridcraft:save", requestId, code: "fidelity_loss", error: "", parts })
function refusalRig() {
  const onSaveRefused = vi.fn()
  const port = { postMessage: vi.fn(), onmessage: null, start: vi.fn(), close: vi.fn() } satisfies NativeStructuredPort
  const frame = { postMessage: vi.fn() }
  const options: NativeEditorPortOptions = { applicationId: "gridcraft", enginePin: "pinned-engine", bundle, frame, scope: { workspaceId: "w", pageId: "p", baseVersion: "1", generation: "9", sessionId: "s" }, createChannel: () => ({ port1: port, port2: {} }), onChanged: vi.fn(), onSaveRequest: vi.fn(), onError: vi.fn(), requireAccessProtocol: true }
  const adapter = createGridcraftNativePort({ ...options, fileName: "Plan.xlsm", onSaveRefused })
  const receive = (data: unknown) => (port as NativeStructuredPort).onmessage?.({ data })
  adapter.loaded(); adapter.handleWindowMessage({ source: frame, origin: "null", data: { type: "gridcraft:hello", protocol: 2, bundle } })
  receive({ type: "gridcraft:ready", version: "v", accessProtocol: 1, readOnly: true })
  return { port, options, adapter, receive, onOpened: vi.fn(), onSaveRefused }
}
const saveRequests = (r: { port: { postMessage: { mock: { calls: unknown[][] } } } }) => r.port.postMessage.mock.calls.filter(([m]) => (m as { type: string }).type === "gridcraft:save-request").length
it("asks the editor once per revision after it refused for fidelity, and tells the host which parts", async () => {
  const r = refusalRig(); await bootstrap(r as unknown as Rig)
  r.receive({ type: "gridcraft:changed", document: "doc", revision: "3", dirty: true })
  const first = r.adapter.snapshot(signal()); r.receive(lossy(latest(r as unknown as Rig).requestId))
  await expect(first).rejects.toThrow("fidelity_loss")
  expect(r.onSaveRefused).toHaveBeenLastCalledWith({ code: "fidelity_loss", parts: [{ code: "charts", count: 2 }] })
  // The same revision is not asked again: the refusal holds for it.
  await expect(r.adapter.snapshot(signal())).rejects.toThrow("fidelity_loss")
  expect(saveRequests(r)).toBe(1)
  // A newer revision is asked again, and its stored save clears the refusal.
  r.receive({ type: "gridcraft:changed", document: "doc", revision: "4", dirty: true })
  const second = r.adapter.snapshot(signal()); const request = latest(r as unknown as Rig)
  expect(saveRequests(r)).toBe(2)
  r.receive(saved(request.requestId, "4"))
  const taken = await second
  expect(await r.adapter.ack(await receipt(r as unknown as Rig, "doc:4", taken.bytes))).toBe(true)
  expect(r.onSaveRefused).toHaveBeenLastCalledWith(null)
})
it("hands the host the file a fidelity refusal carries, with the revision it is of", async () => {
  const r = refusalRig(); await bootstrap(r as unknown as Rig)
  r.receive({ type: "gridcraft:changed", document: "doc", revision: "3", dirty: true })
  const first = r.adapter.snapshot(signal()); r.receive({ ...lossy(latest(r as unknown as Rig).requestId), bytes: new Uint8Array([0x50, 0x4b, 1]).buffer })
  await expect(first).rejects.toThrow("fidelity_loss")
  expect(r.onSaveRefused).toHaveBeenLastCalledWith({ code: "fidelity_loss", parts: [{ code: "charts", count: 2 }], format: "xlsm", editorRevision: "doc:3", bytes: new Uint8Array([0x50, 0x4b, 1]) })
  r.receive({ type: "gridcraft:save-request", requestId: "editor-8", document: "doc", revision: "4" })
  r.receive({ ...lossy("editor-8"), bytes: new Uint8Array([0x50, 0x4b, 2]).buffer })
  expect(r.onSaveRefused).toHaveBeenLastCalledWith({ code: "fidelity_loss", parts: [{ code: "charts", count: 2 }], format: "xlsm", editorRevision: "doc:4", bytes: new Uint8Array([0x50, 0x4b, 2]) })
})
it("tells the host when a save the person started in the editor was refused for fidelity", async () => {
  const r = refusalRig(); await bootstrap(r as unknown as Rig)
  r.receive({ type: "gridcraft:save-request", requestId: "editor-9", document: "doc", revision: "5" })
  r.receive(lossy("editor-9", [{ code: "pivot_tables", count: 1 }]))
  expect(r.onSaveRefused).toHaveBeenCalledWith({ code: "fidelity_loss", parts: [{ code: "pivot_tables", count: 1 }] })
  expect(r.options.onSaveRequest).not.toHaveBeenCalled()
})
