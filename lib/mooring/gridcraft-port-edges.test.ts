// GridCraft editor ↔ host, protocol 2: the guards the main suite does not reach.
// Every malformed or out-of-turn message must end the port, never bind, store or ack anything.
import { afterEach, describe, expect, it, vi } from "vitest"
import { createGridcraftPort, type GridcraftPortOptions } from "./gridcraft-port"
import type { NativeStructuredPort } from "./native-editor-port"

afterEach(() => { vi.useRealTimers() })
const bundle = "a".repeat(64)
function rig(overrides: Partial<GridcraftPortOptions> = {}) {
  const port = { postMessage: vi.fn(), onmessage: null, start: vi.fn(), close: vi.fn() } satisfies NativeStructuredPort
  const frame = { postMessage: vi.fn() }, peer = {}
  const options: GridcraftPortOptions = { bundle, frame, createChannel: () => ({ port1: port, port2: peer }), onChanged: vi.fn(), onDirty: vi.fn(), onSave: vi.fn(), onSaveRefused: vi.fn(), onExport: vi.fn(), onError: vi.fn(), onReady: vi.fn(), onEditAttempt: vi.fn(), onHeldInputDropped: vi.fn(), ...overrides }
  const adapter = createGridcraftPort(options)
  const hello = { source: frame, origin: "null", data: { type: "gridcraft:hello", protocol: 2, bundle } }
  const receive = (data: unknown) => (port as NativeStructuredPort).onmessage?.({ data })
  return { port, frame, peer, options, adapter, hello, receive }
}
type Rig = ReturnType<typeof rig>
const signal = () => new AbortController().signal
const latest = (r: Rig) => r.port.postMessage.mock.calls.at(-1)![0] as Record<string, unknown>
const workbook = () => new Uint8Array([0x50, 0x4b, 3, 4, 9]).buffer
const book = () => ({ name: "Book.xlsx", bytes: new Uint8Array(workbook()) })
const opts = { calculate: false, autoFit: false }
function connect(r: Rig) { r.adapter.loaded(); expect(r.adapter.handleWindowMessage(r.hello)).toBe(true); r.receive({ type: "gridcraft:ready", version: "4870243e", accessProtocol: 1, readOnly: true }) }
async function open(r: Rig) {
  connect(r)
  const opening = r.adapter.open(book(), opts, signal(), 30_000)
  r.receive({ type: "gridcraft:opened", requestId: latest(r).requestId, document: "doc-1", revision: "3", format: "xlsx", calculated: false, warnings: [] })
  return opening
}
const savedFile = (requestId: unknown, revision = "4", document = "doc-1") => ({ type: "gridcraft:save", requestId, document, revision, name: "Book.xlsx", format: "xlsx", bytes: workbook() })
const closedWith = (r: Rig, reason: string) => { expect(r.options.onError).toHaveBeenCalledWith(reason); expect(r.port.close).toHaveBeenCalled() }
const cell = [{ command: "cell.set" as const, params: { cell: "'L'!B2", input: "1" } }]

describe("construction and connect", () => {
  it("refuses a bundle that is not a sha256 hex, a missing frame or a deadline out of range", () => {
    expect(() => createGridcraftPort({ ...rig().options, bundle: "A".repeat(64) })).toThrow("invalid_native_port")
    expect(() => createGridcraftPort({ ...rig().options, frame: undefined as never })).toThrow("invalid_native_port")
    for (const timeoutMs of [0, Number.NaN, 10 * 60_000 + 1]) expect(() => createGridcraftPort({ ...rig().options, timeoutMs })).toThrow("invalid_native_deadline")
  })
  it("ends the port when the editor frame loads a second time", () => {
    const r = rig(); connect(r); r.adapter.loaded(); closedWith(r, "native_editor_reloaded")
    expect(r.adapter.handleWindowMessage(r.hello), "a closed port ignores every hello").toBe(false)
  })
  it("ends the port when the channel cannot be created", () => {
    const r = rig({ createChannel: () => { throw new Error("boom") } }); r.adapter.loaded()
    expect(r.adapter.handleWindowMessage(r.hello)).toBe(false)
    expect(r.options.onError).toHaveBeenCalledWith("native_port_connect")
  })
  it("ends the port when a second ready arrives", () => {
    const r = rig(); connect(r)
    r.receive({ type: "gridcraft:ready", version: "4870243e", accessProtocol: 1, readOnly: true })
    closedWith(r, "native_access_boot_writable")
  })
  it.each([
    ["a non-object", "ready"], ["a message without a type", { version: "1" }], ["a ready of another access protocol", { type: "gridcraft:ready", version: "1", accessProtocol: 2, readOnly: true }],
  ])("ends the port on %s", (_label, message) => {
    const r = rig(); r.adapter.loaded(); r.adapter.handleWindowMessage(r.hello); r.receive(message); closedWith(r, "native_port_protocol")
  })
  it("closes once, reports once, and drops every later message", async () => {
    const r = rig(); await open(r)
    r.receive({ type: "gridcraft:failed", code: "panic", error: "" }); r.receive({ type: "gridcraft:failed", code: "panic", error: "" })
    r.receive({ type: "gridcraft:dirty", dirty: true })
    expect(r.options.onError).toHaveBeenCalledTimes(1); expect(r.options.onDirty).not.toHaveBeenCalled()
    r.adapter.close(); expect(r.port.close).toHaveBeenCalledTimes(1)
  })
  it("ends the port when posting to it throws", async () => {
    const r = rig(); connect(r)
    r.port.postMessage.mockImplementation(() => { throw new Error("DataCloneError") })
    await expect(r.adapter.open(book(), opts, signal(), 30_000)).rejects.toThrow("native_port_send")
    expect(r.options.onError).toHaveBeenCalledWith("native_port_send")
  })
})

describe("open", () => {
  it("refuses to open before the editor is ready, without a port, on an aborted signal or with bad arguments", async () => {
    const early = rig(); early.adapter.loaded(); early.adapter.handleWindowMessage(early.hello)
    await expect(early.adapter.open(book(), opts, signal(), 30_000)).rejects.toThrow("native_access_not_ready")
    const none = rig()
    await expect(none.adapter.open(book(), opts, signal(), 30_000)).rejects.toThrow("native_port_unavailable")
    const r = rig(); connect(r)
    const ac = new AbortController(); ac.abort()
    await expect(r.adapter.open(book(), opts, ac.signal, 30_000)).rejects.toThrow("native_port_unavailable")
    await expect(r.adapter.open(book(), opts, signal(), 0)).rejects.toThrow("invalid_native_deadline")
    await expect(r.adapter.open(book(), { calculate: "yes" } as never, signal(), 30_000)).rejects.toThrow("invalid_native_open")
    await expect(r.adapter.open({ name: "Book.xlsx", bytes: [1, 2] as never }, opts, signal(), 30_000)).rejects.toThrow("invalid_native_open")
    await expect(r.adapter.open({ name: "", bytes: new Uint8Array(workbook()) }, opts, signal(), 30_000)).rejects.toThrow("open_unsupported")
  })
  it("refuses a second open while the first is still running", async () => {
    const r = rig(); connect(r)
    void r.adapter.open(book(), opts, signal(), 30_000).catch(() => {})
    await expect(r.adapter.open(book(), opts, signal(), 30_000)).rejects.toThrow("native_open_bound")
  })
  it("ends the port when an open is aborted, since a late import cannot be undone", async () => {
    const r = rig(); connect(r)
    const ac = new AbortController()
    const opening = r.adapter.open(book(), opts, ac.signal, 30_000)
    ac.abort()
    await expect(opening).rejects.toThrow("native_open_unconfirmed")
    closedWith(r, "native_open_unconfirmed")
  })
  it.each([
    ["a document id with a colon", { document: "doc:1" }], ["another format", { format: "xlsb" }], ["more than 64 warnings", { warnings: Array.from({ length: 65 }, () => ({ code: "w", count: 1 })) }],
    ["a warning without a count", { warnings: [{ code: "w", count: 0 }] }], ["calculated not a boolean", { calculated: "no" }],
  ])("ends the port on an opened answer with %s and binds nothing", async (_label, over) => {
    const r = rig(); connect(r)
    const opening = r.adapter.open(book(), opts, signal(), 30_000)
    r.receive({ type: "gridcraft:opened", requestId: latest(r).requestId, document: "doc-1", revision: "3", format: "xlsx", calculated: false, warnings: [], ...over })
    await expect(opening).rejects.toThrow()
    expect(r.adapter.document).toBeUndefined()
    closedWith(r, "native_port_protocol")
  })
  it("ends the port on an answer to a request that was never made or of another kind", async () => {
    const a = rig(); connect(a); a.receive({ type: "gridcraft:opened", requestId: "never", document: "doc-1", revision: "3", format: "xlsx", calculated: false, warnings: [] })
    closedWith(a, "native_port_protocol")
    const b = rig(); connect(b); b.receive({ type: "gridcraft:opened", requestId: "bad id!", document: "doc-1", revision: "3", format: "xlsx", calculated: false, warnings: [] })
    closedWith(b, "native_port_protocol")
    const c = rig(); await open(c)
    void c.adapter.requestSave(signal(), 30_000).catch(() => {})
    c.receive({ type: "gridcraft:executed", requestId: latest(c).requestId, document: "doc-1", revision: "4", applied: 1 })
    closedWith(c, "native_port_protocol")
  })
})

describe("before a workbook is bound", () => {
  it("ignores the empty workbook's change, dirty and save-request, and a close", () => {
    const r = rig(); connect(r)
    r.receive({ type: "gridcraft:changed", document: "empty", revision: "1", dirty: true })
    r.receive({ type: "gridcraft:dirty", dirty: true })
    r.receive({ type: "gridcraft:save-request", requestId: "e1", document: "empty", revision: "1" })
    r.receive({ type: "gridcraft:closed" })
    expect(r.options.onChanged).not.toHaveBeenCalled(); expect(r.options.onDirty).not.toHaveBeenCalled(); expect(r.options.onError).not.toHaveBeenCalled()
  })
  it("refuses saves, execute and write access", async () => {
    const r = rig(); connect(r)
    await expect(r.adapter.requestSave(signal(), 30_000)).rejects.toThrow("native_open_required")
    await expect(r.adapter.execute(cell, {}, signal())).rejects.toThrow("native_open_required")
    await expect(r.adapter.setReadOnly(false, signal())).rejects.toThrow("native_open_required")
  })
  it("never takes a file the editor sends before the open", () => {
    const r = rig(); connect(r); r.receive(savedFile("x")); closedWith(r, "native_document_changed")
  })
})

describe("the bound workbook", () => {
  it("passes dirty, and ends the port on a change, save-request or file of another document", async () => {
    const r = rig(); await open(r)
    r.receive({ type: "gridcraft:dirty", dirty: false }); expect(r.options.onDirty).toHaveBeenCalledWith(false)
    const a = rig(); await open(a); a.receive({ type: "gridcraft:changed", document: "doc-2", revision: "4", dirty: true }); closedWith(a, "native_document_changed")
    const b = rig(); await open(b); b.receive({ type: "gridcraft:save-request", requestId: "e1", document: "doc-2", revision: "4" }); closedWith(b, "native_document_changed")
    const c = rig(); await open(c); c.receive(savedFile("e1", "4", "doc-2")); closedWith(c, "native_document_changed")
  })
  it("keeps an observer's exception from deciding anything", async () => {
    const r = rig({ onDirty: () => { throw new Error("observer") } }); await open(r)
    r.receive({ type: "gridcraft:dirty", dirty: true })
    expect(r.options.onError).not.toHaveBeenCalled(); expect(r.port.close).not.toHaveBeenCalled()
  })
  it("passes edit attempts only while the confirmed access is read-only", async () => {
    const r = rig(); await open(r)
    const unlocking = r.adapter.setReadOnly(false, signal()); r.receive({ type: "gridcraft:access", requestId: latest(r).requestId, readOnly: false }); await unlocking
    r.receive({ type: "gridcraft:edit-attempt", document: "doc-1", kind: "pointer" })
    r.receive({ type: "gridcraft:edit-attempt", document: "doc-2", kind: "pointer" })
    expect(r.options.onEditAttempt).not.toHaveBeenCalled()
    r.receive({ type: "gridcraft:edit-attempt", document: "doc-1", kind: "wheel" }); closedWith(r, "native_port_protocol")
  })
  it("ends the port on a held-input notice with an unknown reason", async () => {
    const r = rig(); await open(r); r.receive({ type: "gridcraft:held-input-dropped", document: "doc-1", reason: "because" }); closedWith(r, "native_port_protocol")
  })
  it("ends the port on an export without a file or a name", async () => {
    const a = rig(); await open(a); a.receive({ type: "gridcraft:export", name: "", bytes: workbook() }); closedWith(a, "native_port_protocol")
    const b = rig(); await open(b); b.receive({ type: "gridcraft:export", name: "Book.pdf", bytes: new ArrayBuffer(0) }); closedWith(b, "native_port_protocol")
    const c = rig(); await open(c); c.receive({ type: "gridcraft:export", name: "Book.pdf", bytes: new Uint8Array(3) }); closedWith(c, "native_port_protocol")
  })
})

describe("saves", () => {
  it("refuses a save deadline out of range", async () => {
    const r = rig(); await open(r)
    await expect(r.adapter.requestSave(signal(), 10 * 60_000 + 1)).rejects.toThrow("invalid_native_deadline")
  })
  it("rejects a host save that outlives its deadline or is aborted, and keeps the port", async () => {
    vi.useFakeTimers()
    const r = rig(); await open(r)
    const late = r.adapter.requestSave(signal(), 1000); const caught = late.catch(e => e)
    await vi.advanceTimersByTimeAsync(1001)
    expect((await caught).message).toBe("native_request_timeout")
    const ac = new AbortController(); const aborted = r.adapter.requestSave(ac.signal, 1000).catch(e => e); ac.abort()
    expect((await aborted).message).toBe("native_request_aborted")
    expect(r.options.onError).not.toHaveBeenCalled()
  })
  it("ends the port on an editor save-request repeating an id or malformed", async () => {
    const a = rig(); await open(a)
    a.receive({ type: "gridcraft:save-request", requestId: "e1", document: "doc-1", revision: "4" })
    a.receive({ type: "gridcraft:save-request", requestId: "e1", document: "doc-1", revision: "5" }); closedWith(a, "native_port_protocol")
    const b = rig(); await open(b); b.receive({ type: "gridcraft:save-request", requestId: "e1", document: "doc-1", revision: "x" }); closedWith(b, "native_port_protocol")
  })
  it("ends the port when an editor-started file is not the revision it asked to save", async () => {
    const r = rig(); await open(r)
    r.receive({ type: "gridcraft:save-request", requestId: "e1", document: "doc-1", revision: "4" })
    r.receive(savedFile("e1", "5"))
    expect(r.options.onSave).not.toHaveBeenCalled(); closedWith(r, "native_port_protocol")
  })
  it.each([
    ["a format the host does not store", { format: "csv" }], ["an empty name", { name: "" }], ["empty bytes", { bytes: new ArrayBuffer(0) }], ["an extra key", { note: "x" }],
  ])("ends the port on a saved file with %s", async (_label, over) => {
    const r = rig(); await open(r)
    r.receive({ type: "gridcraft:save-request", requestId: "e1", document: "doc-1", revision: "4" })
    r.receive({ ...savedFile("e1", "4"), ...over })
    expect(r.options.onSave).not.toHaveBeenCalled(); closedWith(r, "native_port_protocol")
  })
  it("ends the port on a refusal for a save nobody started", async () => {
    const r = rig(); await open(r)
    r.receive({ type: "gridcraft:save", requestId: "nobody", code: "failed", error: "" }); closedWith(r, "native_port_protocol")
  })
  it("tells the host of an editor-started save refused with a plain code", async () => {
    const r = rig(); await open(r)
    r.receive({ type: "gridcraft:save-request", requestId: "e1", document: "doc-1", revision: "4" })
    r.receive({ type: "gridcraft:save", requestId: "e1", code: "write_failed", error: "disk" })
    expect(r.options.onSaveRefused).toHaveBeenCalledWith({ requestId: "e1", revision: "4", code: "write_failed", parts: [] })
    expect(r.options.onError).not.toHaveBeenCalled()
  })
  it.each([
    ["more than 32 parts", Array.from({ length: 33 }, (_, i) => ({ code: `p${i}`, count: 1 }))], ["no parts", []], ["a part with a zero count", [{ code: "charts", count: 0 }]], ["a part code in capitals", [{ code: "Charts", count: 1 }]],
  ])("ends the port on a fidelity refusal with %s", async (_label, parts) => {
    const r = rig(); await open(r)
    const saving = r.adapter.requestSave(signal(), 30_000); const caught = saving.catch(e => e)
    r.receive({ type: "gridcraft:save", requestId: latest(r).requestId, code: "fidelity_loss", error: "", parts })
    expect(await caught).toBeInstanceOf(Error)
    closedWith(r, "native_port_protocol")
  })
  it("acks only a stored version that is a u64, and nothing after the port closed", async () => {
    const r = rig(); await open(r)
    const saving = r.adapter.requestSave(signal(), 30_000); r.receive(savedFile(latest(r).requestId)); const file = await saving
    expect(r.adapter.ack(file.requestId, "v7")).toBe(false)
    expect(r.adapter.ack("unknown", "7")).toBe(false)
    r.adapter.close()
    expect(r.adapter.ack(file.requestId, "7")).toBe(false)
    expect(r.adapter.saveFailed(file.requestId, "failed"), "close drops every unanswered file").toBe(false)
  })
})

describe("access", () => {
  it("refuses a second access change while one runs, and before ready", async () => {
    const r = rig(); await open(r)
    void r.adapter.setReadOnly(false, signal()).catch(() => {})
    await expect(r.adapter.setReadOnly(true, signal())).rejects.toThrow("native_access_unavailable")
    const early = rig(); early.adapter.loaded(); early.adapter.handleWindowMessage(early.hello)
    await expect(early.adapter.setReadOnly(true, signal())).rejects.toThrow("native_access_unavailable")
    await expect(r.adapter.setReadOnly("no" as never, signal())).rejects.toThrow("native_access_unavailable")
  })
  it("ends the port when an access change outlives its deadline, is aborted or is refused", async () => {
    vi.useFakeTimers()
    const a = rig({ timeoutMs: 500 }); await open(a)
    void a.adapter.setReadOnly(false, signal()).catch(() => {})
    await vi.advanceTimersByTimeAsync(501); closedWith(a, "native_access_unconfirmed")
    vi.useRealTimers()
    const b = rig(); await open(b)
    const ac = new AbortController(); const changing = b.adapter.setReadOnly(false, ac.signal).catch(e => e); ac.abort()
    expect((await changing).message).toBe("native_access_unconfirmed"); closedWith(b, "native_access_unconfirmed")
    const c = rig(); await open(c)
    const refused = c.adapter.setReadOnly(false, signal()).catch(e => e)
    c.receive({ type: "gridcraft:access", requestId: latest(c).requestId, code: "denied", error: "" })
    expect((await refused).message).toBe("denied"); closedWith(c, "native_access_unconfirmed")
  })
})

describe("execute", () => {
  it.each([
    ["no commands", [], {}], ["65 commands", Array.from({ length: 65 }, () => cell[0]), {}], ["an unknown command", [{ command: "sheet.delete" }], {}],
    ["params that are an array", [{ command: "cell.set", params: [] }], {}], ["params that are null", [{ command: "cell.set", params: null }], {}],
    ["more than 64 KiB", [{ command: "range.setValues", params: { range: "'L'!A1", values: [["x".repeat(65 * 1024)]] } }], {}],
    ["a base revision that is not a u64", cell, { baseRevision: "doc-1:x" }], ["keep without a label", cell, { keep: "human" }], ["keep without the saved version it is based on", cell, { keep: "human", label: "Morgan" }], ["a saved-version base without keep (only keep takes saved-<n>)", cell, { baseRevision: "doc-1:saved-3" }],
    ["keep other than human", cell, { keep: "agent", label: "Morgan" }], ["a label without keep", cell, { label: "Morgan" }],
  ])("refuses to send %s", async (_label, commands, options) => {
    const r = rig(); await open(r); const before = r.port.postMessage.mock.calls.length
    await expect(r.adapter.execute(commands as never, options as never, signal())).rejects.toThrow("native_execute_invalid")
    expect(r.port.postMessage).toHaveBeenCalledTimes(before)
  })
  it("sends only the revision part of a document-qualified base", async () => {
    const r = rig(); await open(r)
    void r.adapter.execute(cell, { baseRevision: "doc-1:12" }, signal()).catch(() => {})
    expect(latest(r)).toMatchObject({ baseRevision: "12", saved: true })
    expect(JSON.parse(latest(r).commands as string)).toEqual(cell)
  })
  it("sends empty params for a recalculation", async () => {
    const r = rig(); await open(r)
    void r.adapter.execute([{ command: "formulas.calculateNow" }], {}, signal()).catch(() => {})
    expect(JSON.parse(latest(r).commands as string)).toEqual([{ command: "formulas.calculateNow", params: {} }])
  })
  it("returns a truncated conflict list as truncated", async () => {
    const r = rig(); await open(r)
    const running = r.adapter.execute(cell, { baseRevision: "4", keep: "human", label: "Morgan" }, signal())
    r.receive({ type: "gridcraft:executed", requestId: latest(r).requestId, document: "doc-1", revision: "6", applied: 1, conflicts: [{ sheet: "L", cell: "$B$2", agent: "1", kept: "2" }], conflictsTruncated: true })
    await expect(running).resolves.toMatchObject({ conflictsTruncated: true, conflicts: [{ cell: "$B$2" }] })
  })
  it.each([
    ["conflictsTruncated without conflicts", { conflictsTruncated: true }], ["conflictsTruncated false", { conflicts: [], conflictsTruncated: false }],
    ["more than 200 conflicts", { conflicts: Array.from({ length: 201 }, () => ({ sheet: "L", cell: "A1", agent: "", kept: "" })) }],
    ["a conflict cell that is a range", { conflicts: [{ sheet: "L", cell: "A1:B2", agent: "", kept: "" }] }],
    ["a conflict with an extra key", { conflicts: [{ sheet: "L", cell: "A1", agent: "", kept: "", formula: "=1" }] }],
    ["applied 0", { applied: 0 }], ["applied over 64", { applied: 65 }],
  ])("ends the port on an executed answer with %s", async (_label, over) => {
    const r = rig(); await open(r)
    const running = r.adapter.execute(cell, { baseRevision: "4", keep: "human", label: "Morgan" }, signal()).catch(e => e)
    r.receive({ type: "gridcraft:executed", requestId: latest(r).requestId, document: "doc-1", revision: "6", applied: 1, ...over })
    expect(await running).toBeInstanceOf(Error); closedWith(r, "native_port_protocol")
  })
  it("ends the port on an executed answer for another document", async () => {
    const r = rig(); await open(r)
    const running = r.adapter.execute(cell, {}, signal()).catch(e => e)
    r.receive({ type: "gridcraft:executed", requestId: latest(r).requestId, document: "doc-2", revision: "6", applied: 1 })
    expect((await running).message).toBe("native_document_changed")
  })
  it("rejects an execute that outlives its deadline, and refuses a ninth request in flight", async () => {
    vi.useFakeTimers()
    const r = rig({ timeoutMs: 100 }); await open(r)
    const late = r.adapter.execute(cell, {}, signal()).catch(e => e)
    await vi.advanceTimersByTimeAsync(101)
    expect((await late).message).toBe("native_request_timeout")
    for (let i = 0; i < 8; i++) void r.adapter.execute(cell, {}, signal()).catch(() => {})
    await expect(r.adapter.execute(cell, {}, signal())).rejects.toThrow("native_port_unavailable")
  })
})
