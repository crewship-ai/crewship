// GridCraft editor ↔ host, mooring protocol 2: the workbook file is the source of truth.
import { afterEach, expect, it, vi } from "vitest"
import { createGridcraftPort, GridcraftSaveRefused, type GridcraftPortOptions } from "./gridcraft-port"
import type { NativeStructuredPort } from "./native-editor-port"

afterEach(() => { vi.useRealTimers() })
const bundle = "a".repeat(64)
function rig(overrides: Partial<GridcraftPortOptions> = {}) {
  const port = { postMessage: vi.fn(), onmessage: null, start: vi.fn(), close: vi.fn() } satisfies NativeStructuredPort
  const frame = { postMessage: vi.fn() }, peer = {}
  const options: GridcraftPortOptions = { bundle, frame, createChannel: () => ({ port1: port, port2: peer }), onChanged: vi.fn(), onDirty: vi.fn(), onSave: vi.fn(), onExport: vi.fn(), onError: vi.fn(), onReady: vi.fn(), onEditAttempt: vi.fn(), onHeldInputDropped: vi.fn(), ...overrides }
  const adapter = createGridcraftPort(options)
  const hello = { source: frame, origin: "null", data: { type: "gridcraft:hello", protocol: 2, bundle } }
  const receive = (data: unknown) => (port as NativeStructuredPort).onmessage?.({ data })
  return { port, frame, peer, options, adapter, hello, receive }
}
type Rig = ReturnType<typeof rig>
const signal = () => new AbortController().signal
const latest = (r: Rig) => r.port.postMessage.mock.calls.at(-1)![0] as Record<string, unknown>
const workbook = () => new Uint8Array([0x50, 0x4b, 3, 4, 9]).buffer
function connect(r: Rig) { r.adapter.loaded(); expect(r.adapter.handleWindowMessage(r.hello)).toBe(true); r.receive({ type: "gridcraft:ready", version: "4870243e", accessProtocol: 1, readOnly: true }) }
async function open(r: Rig, format: "xlsx" | "xlsm" = "xlsx") {
  connect(r)
  const opening = r.adapter.open({ name: `Book.${format}`, bytes: new Uint8Array(workbook()) }, { calculate: false, autoFit: false }, signal(), 30_000)
  const request = latest(r)
  r.receive({ type: "gridcraft:opened", requestId: request.requestId, document: "doc-1", revision: "3", format, calculated: false, warnings: [{ code: "vba_preserved", count: 1 }] })
  return { opened: await opening, request }
}
const savedFile = (requestId: unknown, revision = "4", document = "doc-1") => ({ type: "gridcraft:save", requestId, document, revision, name: "Book.xlsx", format: "xlsx", bytes: workbook() })

it("connects only a protocol 2 editor of the pinned bundle and says so in connect", () => {
  const r = rig(); r.adapter.loaded()
  expect(r.adapter.handleWindowMessage(r.hello)).toBe(true)
  expect(r.frame.postMessage).toHaveBeenCalledWith({ type: "gridcraft:connect", protocol: 2, bundle }, "*", [r.peer])
  const old = rig(); old.adapter.loaded()
  expect(old.adapter.handleWindowMessage({ ...old.hello, data: { ...old.hello.data, protocol: 1 } })).toBe(false)
  expect(old.options.onError).toHaveBeenCalledWith("native_editor_handshake"); expect(old.frame.postMessage).not.toHaveBeenCalled()
})
it("refuses an editor that does not start read-only", () => {
  const r = rig(); r.adapter.loaded(); r.adapter.handleWindowMessage(r.hello)
  r.receive({ type: "gridcraft:ready", version: "x", accessProtocol: 1, readOnly: false })
  expect(r.options.onError).toHaveBeenCalledWith("native_access_boot_writable"); expect(r.port.close).toHaveBeenCalled()
})
it("opens the workbook file once, transferring its bytes, and binds the opened document", async () => {
  const r = rig(); const { opened, request } = await open(r, "xlsm")
  expect(request).toEqual({ type: "gridcraft:open", requestId: expect.stringMatching(/^[A-Za-z0-9_-]{1,64}$/), name: "Book.xlsm", bytes: expect.any(ArrayBuffer), calculate: false, autoFit: false })
  expect(r.port.postMessage.mock.calls.at(-1)![1]).toEqual([request.bytes])
  expect(opened).toEqual({ document: "doc-1", revision: "3", format: "xlsm", calculated: false, warnings: [{ code: "vba_preserved", count: 1 }] })
  await expect(r.adapter.open({ name: "Book.xlsx", bytes: new Uint8Array(workbook()) }, { calculate: false, autoFit: false }, signal(), 30_000)).rejects.toThrow("native_open_bound")
})
it("never posts a file that is not .xlsx or .xlsm, or is empty", async () => {
  const r = rig(); connect(r); const before = r.port.postMessage.mock.calls.length
  await expect(r.adapter.open({ name: "Book.xlsb", bytes: new Uint8Array(workbook()) }, { calculate: false, autoFit: false }, signal(), 30_000)).rejects.toThrow("open_unsupported")
  await expect(r.adapter.open({ name: "Book.xlsx", bytes: new Uint8Array() }, { calculate: false, autoFit: false }, signal(), 30_000)).rejects.toThrow("invalid_native_open")
  expect(r.port.postMessage).toHaveBeenCalledTimes(before)
})
it("rejects a refused open with its code only, never the editor's text", async () => {
  const r = rig(); connect(r)
  const opening = r.adapter.open({ name: "Book.xlsx", bytes: new Uint8Array(workbook()) }, { calculate: false, autoFit: false }, signal(), 30_000)
  r.receive({ type: "gridcraft:opened", requestId: latest(r).requestId, code: "open_password", error: "Sheet 'Secret' is locked" })
  const failure = await opening.catch((e: Error) => e)
  expect(failure).toBeInstanceOf(Error); expect((failure as Error).message).toBe("open_password"); expect(String((failure as Error).cause ?? "")).not.toContain("Secret")
})
it("closes the port when an open outlives its deadline, so a late import binds nothing", async () => {
  vi.useFakeTimers()
  const r = rig(); connect(r)
  const opening = r.adapter.open({ name: "Book.xlsx", bytes: new Uint8Array(workbook()) }, { calculate: false, autoFit: false }, signal(), 30_000); void opening.catch(() => {})
  await vi.advanceTimersByTimeAsync(30_000)
  await expect(opening).rejects.toThrow("native_request_timeout")
  expect(r.port.close).toHaveBeenCalled(); expect(r.options.onError).toHaveBeenCalledWith("native_open_unconfirmed")
})
it("reports changes of the bound document and closes on another document", async () => {
  const r = rig(); await open(r)
  r.receive({ type: "gridcraft:changed", document: "doc-1", revision: "4", dirty: true })
  expect(r.options.onChanged).toHaveBeenCalledWith({ document: "doc-1", revision: "4", dirty: true })
  r.receive({ type: "gridcraft:dirty", dirty: false }); expect(r.options.onDirty).toHaveBeenCalledWith(false)
  r.receive({ type: "gridcraft:changed", document: "doc-2", revision: "1", dirty: false })
  expect(r.options.onError).toHaveBeenCalledWith("native_document_changed")
})
it("asks the editor for the file and acknowledges exactly that revision with the stored version", async () => {
  const r = rig(); await open(r)
  const saving = r.adapter.requestSave(signal(), 30_000)
  const request = latest(r); expect(request).toEqual({ type: "gridcraft:save-request", requestId: expect.any(String) })
  r.receive(savedFile(request.requestId))
  const file = await saving
  expect(file).toEqual({ requestId: request.requestId, document: "doc-1", revision: "4", name: "Book.xlsx", format: "xlsx", bytes: expect.any(Uint8Array), source: "host" })
  expect(r.adapter.ack(file.requestId, "7")).toBe(true)
  expect(latest(r)).toEqual({ type: "gridcraft:save-ack", requestId: request.requestId, document: "doc-1", revision: "4", version: "7" })
  expect(r.adapter.ack(file.requestId, "7")).toBe(false)
})
it("hands an editor-started save to the host and refuses a file nobody asked for", async () => {
  const r = rig(); await open(r)
  r.receive({ type: "gridcraft:save-request", requestId: "editor-1", document: "doc-1", revision: "5" })
  r.receive(savedFile("editor-1", "5"))
  expect(r.options.onSave).toHaveBeenCalledWith(expect.objectContaining({ requestId: "editor-1", revision: "5", source: "editor" }))
  r.receive(savedFile("nobody", "5"))
  expect(r.options.onError).toHaveBeenCalledWith("native_port_protocol")
})
it("never acknowledges an older revision after a newer one and says conflict instead", async () => {
  const r = rig(); await open(r)
  const first = r.adapter.requestSave(signal(), 30_000); const a = latest(r)
  const second = r.adapter.requestSave(signal(), 30_000); const b = latest(r)
  r.receive(savedFile(a.requestId, "4")); r.receive(savedFile(b.requestId, "6"))
  const [older, newer] = await Promise.all([first, second])
  expect(r.adapter.ack(newer.requestId, "8")).toBe(true)
  expect(r.adapter.ack(older.requestId, "9")).toBe(false)
  expect(r.adapter.saveFailed(older.requestId, "conflict")).toBe(true)
  expect(latest(r)).toEqual({ type: "gridcraft:save-failed", requestId: older.requestId, code: "conflict", error: "" })
})
it("sends only the contract's save failure codes", async () => {
  const r = rig(); await open(r)
  const saving = r.adapter.requestSave(signal(), 30_000); r.receive(savedFile(latest(r).requestId)); const file = await saving
  expect(r.adapter.saveFailed(file.requestId, "quota" as never)).toBe(false)
  expect(r.adapter.saveFailed(file.requestId, "too_large")).toBe(true)
  expect(r.adapter.saveFailed(file.requestId, "too_large")).toBe(false)
})
it("keeps at most 4 saves unanswered on either side", async () => {
  const r = rig(); await open(r)
  for (let i = 0; i < 4; i++) void r.adapter.requestSave(signal(), 30_000).catch(() => {})
  await expect(r.adapter.requestSave(signal(), 30_000)).rejects.toThrow("native_save_backpressure")
  const e = rig(); await open(e)
  for (let i = 0; i < 4; i++) e.receive({ type: "gridcraft:save-request", requestId: `e${i}`, document: "doc-1", revision: String(4 + i) })
  e.receive({ type: "gridcraft:save-request", requestId: "e4", document: "doc-1", revision: "8" })
  expect(e.options.onError).toHaveBeenCalledWith("native_save_backpressure")
})
it("offers an export for download and never as a version", async () => {
  const r = rig(); await open(r)
  r.receive({ type: "gridcraft:export", name: "Book.pdf", bytes: workbook() })
  expect(r.options.onExport).toHaveBeenCalledWith({ name: "Book.pdf", bytes: expect.any(Uint8Array) }); expect(r.options.onSave).not.toHaveBeenCalled()
})
it("rejects an agent replay refused by the editor with its code", async () => {
  const r = rig(); await open(r)
  const running = r.adapter.execute([{ command: "cell.set", params: { cell: "B2", input: "12" } }], { baseRevision: "4" }, signal())
  const request = latest(r)
  expect(request).toEqual({ type: "gridcraft:execute", requestId: expect.any(String), commands: JSON.stringify([{ command: "cell.set", params: { cell: "B2", input: "12" } }]), saved: true, baseRevision: "4" })
  r.receive({ type: "gridcraft:executed", requestId: request.requestId, code: "stale_base", error: "changed" })
  await expect(running).rejects.toThrow("stale_base")
})
// The agent's log replayed over the person's unsaved state; the person's cells win.
it("replays over the person's work with keep human and a label, and returns the cells the person kept", async () => {
  const r = rig(); await open(r)
  const commands = [{ command: "edit.clearContents" as const, params: { range: "'L'!B8:B577" } }]
  const running = r.adapter.execute(commands, { baseRevision: "4", keep: "human", label: "Morgan: clear Loan ID" }, signal())
  const request = latest(r)
  expect(request).toEqual({ type: "gridcraft:execute", requestId: expect.any(String), commands: JSON.stringify(commands), saved: true, baseRevision: "4", keep: "human", label: "Morgan: clear Loan ID" })
  const conflicts = [{ sheet: "L", cell: "B9", agent: "", kept: "LN-0042" }]
  r.receive({ type: "gridcraft:executed", requestId: request.requestId, document: "doc-1", revision: "6", applied: 1, conflicts })
  await expect(running).resolves.toEqual({ documentId: "doc-1", revision: "doc-1:6", applied: 1, conflicts, conflictsTruncated: false })
})
it("refuses a replay that cannot be made safely, and never takes a malformed conflict list", async () => {
  const r = rig(); await open(r)
  await expect(r.adapter.execute([{ command: "cell.set", params: { cell: "B2", input: "1" } }], { baseRevision: "4", keep: "human", label: "x".repeat(81) }, signal())).rejects.toThrow("native_execute_invalid")
  const refused = r.adapter.execute([{ command: "cell.set", params: { cell: "B2", input: "1" } }], { baseRevision: "4", keep: "human", label: "Morgan" }, signal())
  r.receive({ type: "gridcraft:executed", requestId: latest(r).requestId, code: "not_replayable", error: "" })
  await expect(refused).rejects.toThrow("not_replayable")
  const bad = r.adapter.execute([{ command: "cell.set", params: { cell: "B2", input: "1" } }], { baseRevision: "4", keep: "human", label: "Morgan" }, signal()); void bad.catch(() => {})
  r.receive({ type: "gridcraft:executed", requestId: latest(r).requestId, document: "doc-1", revision: "7", applied: 1, conflicts: [{ sheet: "L", cell: "B9", agent: "x".repeat(300), kept: "" }] })
  await expect(bad).rejects.toThrow()
})
it("applies read-only access only as the editor confirms it", async () => {
  const r = rig(); await open(r)
  const unlocking = r.adapter.setReadOnly(false, signal())
  expect(latest(r)).toEqual({ type: "gridcraft:set-access", requestId: expect.any(String), readOnly: false })
  r.receive({ type: "gridcraft:access", requestId: latest(r).requestId, readOnly: false })
  await expect(unlocking).resolves.toBe(false)
  const locking = r.adapter.setReadOnly(true, signal()); void locking.catch(() => {})
  r.receive({ type: "gridcraft:access", requestId: latest(r).requestId, readOnly: false })
  expect(r.options.onError).toHaveBeenCalledWith("native_access_mismatch")
})
it.each([
  ["an unknown message", { type: "gridcraft:native-exported", requestId: "x" }],
  ["an extra key", { type: "gridcraft:dirty", dirty: true, note: "x" }],
  ["an error text over 512 characters", { type: "gridcraft:failed", code: "x", error: "e".repeat(513) }],
  ["a revision that is not a u64", { type: "gridcraft:changed", document: "doc-1", revision: "-1", dirty: true }],
])("closes the port on %s", async (_label, message) => {
  const r = rig(); await open(r)
  r.receive(message)
  expect(r.options.onError).toHaveBeenCalledWith("native_port_protocol"); expect(r.port.close).toHaveBeenCalled()
})
it("closes on an engine failure and on the bound workbook closing", async () => {
  const a = rig(); await open(a); a.receive({ type: "gridcraft:failed", code: "panic", error: "x" }); expect(a.options.onError).toHaveBeenCalledWith("native_engine_failed")
  const b = rig(); await open(b); b.receive({ type: "gridcraft:closed" }); expect(b.options.onError).toHaveBeenCalledWith("native_document_changed")
})
it("passes edit attempts of the bound read-only workbook without content", async () => {
  const r = rig(); await open(r)
  r.receive({ type: "gridcraft:edit-attempt", document: "doc-1", kind: "key" }); expect(r.options.onEditAttempt).toHaveBeenCalledWith("key")
  r.receive({ type: "gridcraft:held-input-dropped", document: "doc-1", reason: "refused" }); expect(r.options.onHeldInputDropped).toHaveBeenCalledWith("refused")
})

// Contract: calculated in changed is a boolean and goes from false to true only.
it("passes calculated on with a change, and closes when it goes back to false", async () => {
  const r = rig(); await open(r)
  r.receive({ type: "gridcraft:changed", document: "doc-1", revision: "4", dirty: true, calculated: true })
  expect(r.options.onChanged).toHaveBeenLastCalledWith({ document: "doc-1", revision: "4", dirty: true, calculated: true })
  r.receive({ type: "gridcraft:changed", document: "doc-1", revision: "5", dirty: true })
  expect(r.options.onChanged).toHaveBeenLastCalledWith({ document: "doc-1", revision: "5", dirty: true })
  r.receive({ type: "gridcraft:changed", document: "doc-1", revision: "6", dirty: true, calculated: false })
  expect(r.options.onError).toHaveBeenCalledWith("native_port_protocol")
  for (const calculated of ["yes", 1, null]) {
    const s = rig(); await open(s)
    s.receive({ type: "gridcraft:changed", document: "doc-1", revision: "4", dirty: true, calculated })
    expect(s.options.onError, String(calculated)).toHaveBeenCalledWith("native_port_protocol")
  }
})
// Contract v2 addendum: the editor refuses to write a file that would drop parts of the workbook.
const lossy = (requestId: unknown, parts: unknown = [{ code: "charts", count: 2 }, { code: "pivot_tables", count: 1 }]) => ({ type: "gridcraft:save", requestId, code: "fidelity_loss", error: "would drop 2 charts in 'Q3 sales'", parts })
it("rejects a host save the editor refused for fidelity with the code and the parts, never its text, and stays open", async () => {
  const r = rig(); await open(r)
  const saving = r.adapter.requestSave(signal(), 30_000)
  r.receive(lossy(latest(r).requestId))
  const refused = await saving.catch((error: unknown) => error)
  expect(refused).toBeInstanceOf(GridcraftSaveRefused)
  expect(refused).toMatchObject({ message: "fidelity_loss", code: "fidelity_loss", parts: [{ code: "charts", count: 2 }, { code: "pivot_tables", count: 1 }] })
  expect(String((refused as Error).message)).not.toContain("Q3")
  expect(r.port.close).not.toHaveBeenCalled(); expect(r.options.onError).not.toHaveBeenCalled()
  // The next save is asked as usual.
  const again = r.adapter.requestSave(signal(), 30_000); r.receive(savedFile(latest(r).requestId)); await expect(again).resolves.toMatchObject({ revision: "4" })
})
it("tells the host of an editor-started save the editor then refused, and takes no file for it", async () => {
  const onSaveRefused = vi.fn()
  const r = rig({ onSaveRefused }); await open(r)
  r.receive({ type: "gridcraft:save-request", requestId: "editor-2", document: "doc-1", revision: "6" })
  r.receive(lossy("editor-2", [{ code: "vba_project", count: 1 }]))
  expect(onSaveRefused).toHaveBeenCalledWith({ requestId: "editor-2", revision: "6", code: "fidelity_loss", parts: [{ code: "vba_project", count: 1 }] })
  expect(r.options.onSave).not.toHaveBeenCalled(); expect(r.port.close).not.toHaveBeenCalled()
  r.receive(savedFile("editor-2", "6"))
  expect(r.options.onError).toHaveBeenCalledWith("native_port_protocol")
})
// A fidelity refusal may carry the file as it would be written, for a local draft only.
const pkFile = () => { const b = new Uint8Array([0x50, 0x4b, 3, 4, 9]); return b.buffer }
it("keeps the file a fidelity refusal carries, for the host's local draft, and never as a save", async () => {
  const onSaveRefused = vi.fn()
  const r = rig({ onSaveRefused }); await open(r)
  const saving = r.adapter.requestSave(signal(), 30_000)
  r.receive({ ...lossy(latest(r).requestId), bytes: pkFile() })
  const refused = await saving.catch((error: unknown) => error) as GridcraftSaveRefused
  expect(refused).toBeInstanceOf(GridcraftSaveRefused)
  expect(Array.from(refused.bytes ?? [])).toEqual([0x50, 0x4b, 3, 4, 9])
  r.receive({ type: "gridcraft:save-request", requestId: "editor-3", document: "doc-1", revision: "7" })
  r.receive({ ...lossy("editor-3", [{ code: "charts", count: 1 }]), bytes: pkFile() })
  expect(onSaveRefused).toHaveBeenCalledWith({ requestId: "editor-3", revision: "7", code: "fidelity_loss", parts: [{ code: "charts", count: 1 }], bytes: new Uint8Array([0x50, 0x4b, 3, 4, 9]) })
  expect(r.options.onSave).not.toHaveBeenCalled(); expect(r.port.close).not.toHaveBeenCalled()
  // Without a file, as before: no bytes.
  const plain = r.adapter.requestSave(signal(), 30_000); r.receive(lossy(latest(r).requestId))
  expect(((await plain.catch((e: unknown) => e)) as GridcraftSaveRefused).bytes).toBeUndefined()
})
it("closes on a fidelity refusal whose file is empty or not a transferred buffer", async () => {
  for (const bytes of [new ArrayBuffer(0), new Uint8Array([0x50, 0x4b]), "UEs="]) {
    const s = rig(); await open(s)
    const pending = s.adapter.requestSave(signal(), 30_000)
    s.receive({ ...lossy(latest(s).requestId), bytes })
    await expect(pending).rejects.toThrow()
    expect(s.port.close).toHaveBeenCalled()
  }
})
it("rejects other save errors with their code, and closes on a malformed refusal", async () => {
  const r = rig(); await open(r)
  const saving = r.adapter.requestSave(signal(), 30_000)
  r.receive({ type: "gridcraft:save", requestId: latest(r).requestId, code: "failed", error: "" })
  await expect(saving).rejects.toThrow("failed")
  for (const parts of [[{ code: "charts", count: 0 }], [{ code: "Charts!", count: 1 }], [{ code: "charts", count: 1, name: "Q3" }], "charts", Array.from({ length: 33 }, () => ({ code: "charts", count: 1 }))]) {
    const s = rig(); await open(s)
    const pending = s.adapter.requestSave(signal(), 30_000)
    s.receive(lossy(latest(s).requestId, parts))
    await expect(pending).rejects.toThrow()
    expect(s.options.onError).toHaveBeenCalledWith("native_port_protocol")
  }
})
// GridCraft: over unsaved work the editor applies the replay to its saved version too and
// names that version apart (savedRevision, e.g. "saved-3"); the next replay names it as its base, never `revision`.
it("takes the saved version's own revision from a replay over unsaved work, and names it as the next base", async () => {
  const r = rig(); await open(r)
  const commands = [{ command: "cell.set" as const, params: { cell: "'L'!B2", input: "12" } }]
  const first = r.adapter.execute(commands, { baseRevision: "doc-1:4", keep: "human", label: "Morgan: change" }, signal())
  const one = latest(r)
  r.receive({ type: "gridcraft:executed", requestId: one.requestId, document: "doc-1", revision: "7", savedRevision: "saved-3", applied: 1 })
  await expect(first).resolves.toEqual({ documentId: "doc-1", revision: "doc-1:7", savedRevision: "doc-1:saved-3", applied: 1 })
  const second = r.adapter.execute(commands, { baseRevision: "doc-1:saved-3", keep: "human", label: "Morgan: change" }, signal())
  expect(latest(r)).toMatchObject({ type: "gridcraft:execute", baseRevision: "saved-3" })
  r.receive({ type: "gridcraft:executed", requestId: latest(r).requestId, document: "doc-1", revision: "8", savedRevision: "saved-4", applied: 1 })
  await expect(second).resolves.toMatchObject({ savedRevision: "doc-1:saved-4" })
  for (const bad of ["saved-", "saved-x", "../1", ""]) {
    const refused = r.adapter.execute(commands, { baseRevision: `doc-1:${bad}` }, signal())
    await expect(refused, bad).rejects.toThrow("native_execute_invalid")
  }
})

// The editor names the build it runs in ready; the host compares it with the server's before a replay.
it("takes the editor's build from ready, and refuses a malformed one", () => {
  const r = rig(); r.adapter.loaded(); expect(r.adapter.handleWindowMessage(r.hello)).toBe(true)
  r.receive({ type: "gridcraft:ready", version: "0.19.1", accessProtocol: 1, readOnly: true, build: "0.19.1+59f6b6c14a37 patch dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd" })
  expect(r.options.onReady).toHaveBeenCalledWith("0.19.1", "0.19.1+59f6b6c14a37 patch dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
  const bad = rig(); bad.adapter.loaded(); expect(bad.adapter.handleWindowMessage(bad.hello)).toBe(true)
  bad.receive({ type: "gridcraft:ready", version: "0.19.1", accessProtocol: 1, readOnly: true, build: "dev" })
  expect(bad.options.onReady).not.toHaveBeenCalled()
})
// A save names the cells the person changed since the saved version, or "unknown"; never "nothing"
// when it does not know. Anything the port cannot read as such counts as unknown.
it("carries the cells the person changed with a save, and takes anything it cannot read as unknown", async () => {
  const cases: [unknown, unknown][] = [
    [["'L'!B2", "'Q 3'!A1:C9"], ["'L'!B2", "'Q 3'!A1:C9"]],
    [[], []],
    ["unknown", "unknown"],
    [["B2"], "unknown"],
    [["'L'!B2", 5], "unknown"],
    ["nothing", "unknown"],
    [Array.from({ length: 2001 }, (_, i) => `'L'!A${i + 1}`), "unknown"],
  ]
  for (const [touched, want] of cases) {
    const r = rig(); await open(r)
    r.receive({ type: "gridcraft:save-request", requestId: "editor-1", document: "doc-1", revision: "5" })
    r.receive({ ...savedFile("editor-1", "5"), touched })
    expect(r.options.onSave, JSON.stringify(touched).slice(0, 40)).toHaveBeenCalledWith(expect.objectContaining({ requestId: "editor-1", touched: want }))
  }
  const old = rig(); await open(old)
  old.receive({ type: "gridcraft:save-request", requestId: "editor-1", document: "doc-1", revision: "5" })
  old.receive(savedFile("editor-1", "5"))
  expect((old.options.onSave as ReturnType<typeof vi.fn>).mock.calls[0][0].touched, "an editor that does not say").toBeUndefined()
})
