// @vitest-environment node
import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { DocumentDraft } from "../document-queue"
import { openNativeDraftJournal, type NativeDraft, type NativeDraftJournal } from "./native-draft"
import { createSingleEditorSave, type NativeSaveRequest, type NativeSaveState } from "./single-editor-save"

// #4852 after #4864: the paths where one local journal step could still stop
// native saving for good, and an identity switch while a write is pending.
// The real native-draft journal runs here (its serial queue, row ownership and
// retirement); only the IndexedDB store under it is an in-memory double.
const mock = vi.hoisted(() => ({ open: vi.fn(), purge: vi.fn(), removeRow: vi.fn(), retire: undefined as undefined | (() => Promise<void>) }))
vi.mock("../document-queue", async importOriginal => ({
  ...await importOriginal<typeof import("../document-queue")>(),
  openDocumentDrafts: mock.open,
  purgeDocumentDrafts: mock.purge,
  removeDocumentDraftRow: mock.removeRow,
  registerDocumentDraftWriter: (_human: string, retire: () => Promise<void>) => { mock.retire = retire; return () => {} },
}))
vi.mock("../protected-document-drafts", () => ({ openProtectedDocumentDrafts: mock.open }))

const rows = new Map<string, DocumentDraft>()
const store = { exclusive: true, load: vi.fn(async () => [...rows.values()]), put: vi.fn(async (row: DocumentDraft) => { rows.set(row.id, row) }), remove: vi.fn(async (id: string) => { rows.delete(id) }), close: vi.fn() }
beforeEach(() => {
  vi.stubGlobal("crypto", webcrypto)
  rows.clear(); store.put.mockClear(); store.remove.mockClear(); store.load.mockClear()
  store.put.mockImplementation(async (row: DocumentDraft) => { rows.set(row.id, row) })
  store.remove.mockImplementation(async (id: string) => { rows.delete(id) })
  mock.open.mockReset(); mock.open.mockResolvedValue(store); mock.retire = undefined
})
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

const receipt = (r: NativeSaveRequest) => ({ ...r, bytes: undefined, version: String(BigInt(r.baseVersion) + 1n), durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash })
const never = () => new Promise<never>(() => {})

async function rig(options: { journal?: NativeDraftJournal; timeoutMs?: number; idleMs?: number } = {}) {
  const journal = options.journal ?? await openNativeDraftJournal({ human: "alice", workspace: "w", page: "p", applicationId: "gridcraft" })
  let value = 1, captures = 0
  const requests: NativeSaveRequest[] = [], states: NativeSaveState[] = []
  const onReceipt = vi.fn()
  const send = vi.fn(async (r: NativeSaveRequest): Promise<unknown> => receipt(r))
  const snapshot = vi.fn(async (_signal: AbortSignal, id?: string) => { captures++; return { bytes: new Uint8Array([value]), editorRevision: id ?? `grid:${captures}` } })
  const controller = createSingleEditorSave({ workspaceId: "w", pageId: "p", applicationId: "gridcraft", enginePin: "engine", baseVersion: "1", generation: "1", sessionId: "session",
    idleMs: options.idleMs ?? 5, timeoutMs: options.timeoutMs ?? 200, journal,
    engine: { snapshot, async validate() {} },
    transport: { async save(r) { requests.push(r); return send(r) } },
    onState(state) { states.push(state) }, onReceipt,
  })
  return { controller, journal, requests, states, onReceipt, send, snapshot, edit(next: number) { value = next; controller.edited() } }
}
type Rig = Awaited<ReturnType<typeof rig>>
const last = (r: Rig) => r.states.at(-1)
// A journal double for paths that need one exact confirm to misbehave.
function scriptedJournal() {
  let stored: NativeDraft | undefined
  const journal = {
    persist: vi.fn(async (d: NativeDraft) => { stored = d }),
    confirm: vi.fn(async (d: NativeDraft) => { if (stored?.editorRevision === d.editorRevision) stored = undefined }),
    async recovery() { return [] }, onRetire() { return () => {} }, async close() {}, async revokeRead() {}, async discardRecovered() {},
  } satisfies NativeDraftJournal
  return { journal, get stored() { return stored } }
}

it("(a) an idle capture of committed bytes whose row retirement fails retries by itself instead of blocking for good", async () => {
  const s = scriptedJournal()
  const r = await rig({ journal: s.journal })
  try {
    r.edit(2)
    await vi.waitFor(() => expect(last(r)).toEqual({ phase: "saved", dirty: false }))
    expect(r.requests).toHaveLength(1)
    // The engine reports a change whose export equals the committed bytes
    // (Sheet: same content); the idle path only retires the local row, and
    // that retirement fails once (e.g. the IDB deadline after a stall).
    s.journal.confirm.mockRejectedValueOnce(Error("storage deadline"))
    r.edit(2)
    await vi.waitFor(() => expect(r.states).toContainEqual(expect.objectContaining({ phase: "draft", dirty: true })))
    // No edit, no Ctrl+S, no flush: the editor still settles on Saved.
    await vi.waitFor(() => expect(last(r)).toEqual({ phase: "saved", dirty: false }))
    expect(r.requests).toHaveLength(1)
    expect(s.stored).toBeUndefined()
    expect(r.controller.confirmedVersion()).toBe("2")
  } finally { await r.controller.close() }
})

it("(a) the idle retry is bounded: storage that keeps failing stays visible and recoverable, with no loop and no send", async () => {
  const s = scriptedJournal()
  const r = await rig({ journal: s.journal })
  try {
    r.edit(2)
    await vi.waitFor(() => expect(last(r)).toEqual({ phase: "saved", dirty: false }))
    s.journal.confirm.mockRejectedValue(Error("storage deadline"))
    r.edit(2)
    // The first failure, then exactly one retry, then it stops.
    await vi.waitFor(() => expect(s.journal.confirm).toHaveBeenCalledTimes(3))
    vi.useFakeTimers()
    await vi.advanceTimersByTimeAsync(5_000)
    vi.useRealTimers()
    expect(s.journal.confirm).toHaveBeenCalledTimes(3)
    expect(r.requests).toHaveLength(1)
    expect(last(r)).toEqual({ phase: "draft", dirty: true, reason: "unretired" })
    // Storage recovers; the next edit saves normally.
    s.journal.confirm.mockResolvedValue(undefined)
    r.edit(3)
    await vi.waitFor(() => expect(r.requests).toHaveLength(2))
    await vi.waitFor(() => expect(last(r)).toEqual({ phase: "saved", dirty: false }))
  } finally { await r.controller.close() }
})

it("(b) a journal confirm that never answers after a committed save ends in a visible state, not pending forever", async () => {
  const s = scriptedJournal()
  const r = await rig({ journal: s.journal, timeoutMs: 100 })
  try {
    s.journal.confirm.mockImplementationOnce(never)
    r.edit(2)
    await vi.waitFor(() => expect(r.requests).toHaveLength(1))
    // Bounded by the save deadline: the server committed, the row is kept.
    await vi.waitFor(() => expect(r.states).toContainEqual(expect.objectContaining({ phase: "draft", dirty: true })), { timeout: 2_000 })
    // The one retry finds the bytes committed: no duplicate version, the row
    // is retired and the original validated receipt is delivered once.
    await vi.waitFor(() => expect(last(r)).toEqual({ phase: "saved", dirty: false }))
    expect(r.requests).toHaveLength(1)
    expect(s.stored).toBeUndefined()
    expect(r.onReceipt).toHaveBeenCalledOnce()
    expect(r.onReceipt).toHaveBeenCalledWith(expect.objectContaining({ version: "2", editorRevision: r.requests[0].editorRevision }))
  } finally { await r.controller.close() }
})

it("(b) a native Ctrl+S during a hung confirm answers within the deadline instead of waiting forever", async () => {
  const s = scriptedJournal()
  const r = await rig({ journal: s.journal, timeoutMs: 100, idleMs: 20 })
  try {
    s.journal.confirm.mockImplementationOnce(never)
    const first = r.controller.save()
    await vi.waitFor(() => expect(s.journal.confirm).toHaveBeenCalledOnce())
    r.edit(3)
    // Before the fix this ticket waited on `pending.done` for ever. Now it
    // ends (here as a visible failure the engine reports) and is never
    // replayed; the edit itself is then saved by the ordinary retry.
    const outcome = await r.controller.save("ctrl-s").then(() => "sent", (error: Error) => error.message)
    await first
    expect(outcome).toBe("storage")
    await vi.waitFor(() => expect(r.requests).toHaveLength(2))
    expect(r.requests[1].editorRevision).not.toBe("ctrl-s")
    expect(r.requests[1].bytes).toEqual(new Uint8Array([3]))
    expect(r.requests[1].baseVersion).toBe("2")
    await vi.waitFor(() => expect(last(r)).toEqual({ phase: "saved", dirty: false }))
    expect(r.requests).toHaveLength(2)
  } finally { await r.controller.close() }
})

it("(b) with the real journal, a hung row removal blocks no save silently and deletes nothing it does not own", async () => {
  const hung: Array<() => void> = []
  const r = await rig({ timeoutMs: 100, idleMs: 5 })
  try {
    // The journal's IDB row removal never answers; its queue (and so every
    // later persist) waits behind it, as a real stuck transaction would.
    store.remove.mockImplementationOnce((id: string) => new Promise<void>(resolve => { hung.push(() => { rows.delete(id); resolve() }) }))
    r.edit(2)
    await vi.waitFor(() => expect(r.requests).toHaveLength(1))
    await vi.waitFor(() => expect(r.states).toContainEqual({ phase: "draft", dirty: true, reason: "unretired" }))
    // The one automatic retry cannot write its local copy behind the stuck
    // removal either; it says so instead of waiting.
    await vi.waitFor(() => expect(last(r)).toEqual({ phase: "draft", dirty: true, reason: "storage" }))
    // A further edit cannot be captured durably while the journal is stuck:
    // it says so, and nothing is sent without a local copy.
    const before = r.states.length
    r.edit(3)
    await vi.waitFor(() => expect(r.states.slice(before + 1)).toContainEqual({ phase: "draft", dirty: true, reason: "storage" }))
    expect(r.requests).toHaveLength(1)
    expect(rows.size).toBe(1)
    // The journal answers again: the next edit is captured and saved, at the
    // committed base, and only this mount's row is ever removed.
    for (const release of hung) release()
    r.edit(4)
    await vi.waitFor(() => expect(r.requests).toHaveLength(2))
    expect(r.requests[1].bytes).toEqual(new Uint8Array([4]))
    expect(r.requests[1].baseVersion).toBe("2")
    await vi.waitFor(() => expect(last(r)).toEqual({ phase: "saved", dirty: false }))
    await vi.waitFor(() => expect(rows.size).toBe(0))
  } finally { await r.controller.close() }
})

it("reports a failed retirement as unsaved, not as on the server, when a newer edit exists", async () => {
  const s = scriptedJournal()
  const r = await rig({ journal: s.journal, idleMs: 1000 })
  let release!: () => void
  r.send.mockImplementationOnce(req => new Promise(resolve => { release = () => resolve(receipt(req)) }))
  try {
    s.journal.confirm.mockRejectedValueOnce(Error("storage deadline"))
    const first = r.controller.save()
    await vi.waitFor(() => expect(r.requests).toHaveLength(1))
    r.edit(3) // newer than what is in flight
    release(); await first
    expect(r.states).toContainEqual({ phase: "draft", dirty: true, reason: "storage" })
    expect(r.states).not.toContainEqual(expect.objectContaining({ reason: "unretired" }))
  } finally { await r.controller.close() }
})

it("an identity switch while a save is in flight leaks nothing: no receipt, no later journal write, no further send", async () => {
  const r = await rig({ idleMs: 1000 })
  let release!: () => void
  r.send.mockImplementationOnce(req => new Promise(resolve => { release = () => resolve(receipt(req)) }))
  const first = r.controller.save()
  await vi.waitFor(() => expect(r.requests).toHaveLength(1))
  const puts = store.put.mock.calls.length
  // Sign-out / account switch retires every draft writer of the old identity.
  await mock.retire!()
  expect(last(r)).toMatchObject({ phase: "stopped" })
  release(); await first
  r.edit(9)
  await expect(r.controller.save("late-ticket")).rejects.toThrow()
  expect(r.onReceipt).not.toHaveBeenCalled()
  expect(store.put.mock.calls.length).toBe(puts)
  expect(store.remove).not.toHaveBeenCalled()
  expect(r.requests).toHaveLength(1)
  expect(r.snapshot).toHaveBeenCalledOnce()
  expect(r.states.slice(r.states.findIndex(s => s.phase === "stopped")).every(s => s.phase === "stopped")).toBe(true)
})

it("an identity switch while a row retirement hangs: the deadline afterwards captures, sends and reports nothing", async () => {
  const s = scriptedJournal()
  let retire!: () => void
  const journal = { ...s.journal, onRetire(listener: () => void) { retire = listener; return () => {} } }
  const r = await rig({ journal, timeoutMs: 100, idleMs: 5 })
  s.journal.confirm.mockImplementationOnce(never)
  r.edit(2)
  await vi.waitFor(() => expect(s.journal.confirm).toHaveBeenCalledOnce())
  retire()
  const stoppedAt = r.states.length
  expect(last(r)).toMatchObject({ phase: "stopped" })
  vi.useFakeTimers()
  await vi.advanceTimersByTimeAsync(1_000)
  vi.useRealTimers()
  expect(r.states).toHaveLength(stoppedAt)
  expect(r.onReceipt).not.toHaveBeenCalled()
  expect(r.snapshot).toHaveBeenCalledOnce()
  expect(r.requests).toHaveLength(1)
})

it("a timed-out persist that lands late is never overwritten or retired by an older queued manual ticket", async () => {
  // Codex review of #4887, blocker 1. A is in flight; manual ticket B is
  // captured and waits behind A; newer edit C's local write misses the
  // controller deadline but lands later, before A's confirmation (which
  // queues behind it in the journal) answers. B must then neither rebase over
  // C's newer row nor retire it: a reload must still find C.
  // Only the controller's timers are faked, so each deadline is placed exactly.
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
  const settle = async (done: () => boolean) => { for (let i = 0; i < 1000 && !done(); i++) await new Promise(resolve => setImmediate(resolve)); expect(done()).toBe(true) }
  const base = await openNativeDraftJournal({ human: "alice", workspace: "w", page: "p", applicationId: "gridcraft" })
  const journal = { ...base, confirm: vi.fn((d: NativeDraft) => base.confirm(d)) }
  const r = await rig({ journal, timeoutMs: 100, idleMs: 1000 })
  let releaseA!: () => void
  r.send.mockImplementationOnce(req => new Promise(resolve => { releaseA = () => resolve(receipt(req)) }))
  try {
    r.edit(2)
    const a = r.controller.save()
    await settle(() => r.requests.length === 1)
    let bDone = false
    const b = r.controller.save("ticket-b").then(() => "sent", (error: Error) => error.message).finally(() => { bDone = true })
    await settle(() => store.put.mock.calls.length === 2)
    // C: a newer edit whose IndexedDB write hangs past the controller deadline.
    let landC!: () => void, rowC: DocumentDraft | undefined
    store.put.mockImplementationOnce((row: DocumentDraft) => new Promise<void>(resolve => { rowC = row; landC = () => { rows.set(row.id, row); resolve() } }))
    r.edit(3)
    let cDone = false
    void r.controller.save().finally(() => { cDone = true })
    await settle(() => store.put.mock.calls.length === 3)
    await vi.advanceTimersByTimeAsync(50)
    // A commits; its confirmation queues in the journal behind C's write.
    releaseA()
    await settle(() => journal.confirm.mock.calls.length === 1)
    // C's controller deadline passes (A's confirmation still has 50 ms) ...
    await vi.advanceTimersByTimeAsync(60)
    await settle(() => cDone)
    expect(bDone).toBe(false)
    // ... and only then does C's write land, before A's confirmation deadline.
    landC()
    await a
    await settle(() => bDone)
    await b
    // Whatever B did, C's newer content stays recoverable from this browser.
    expect(rowC).toBeDefined()
    expect([...rows.values()].map(row => row.snapshot)).toEqual([rowC!.snapshot])
    expect(r.requests.map(q => q.bytes[0])).not.toContain(3)
  } finally { vi.useRealTimers(); await r.controller.close() }
})

it("an older manual ticket never writes behind a newer local write that has not settled yet", async () => {
  // Same interleaving, but C's write is still in the journal when B rebases:
  // B cannot know whether C will land, so it fails visibly instead of queueing
  // an older row behind C. Nothing is sent for B; C lands and stays.
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
  const settle = async (done: () => boolean) => { for (let i = 0; i < 1000 && !done(); i++) await new Promise(resolve => setImmediate(resolve)); expect(done()).toBe(true) }
  const base = await openNativeDraftJournal({ human: "alice", workspace: "w", page: "p", applicationId: "gridcraft" })
  // A's confirmation answers on its own; it is not queued behind C here.
  const journal = { ...base, confirm: vi.fn(async () => {}) }
  const r = await rig({ journal, timeoutMs: 100, idleMs: 1000 })
  let releaseA!: () => void
  r.send.mockImplementationOnce(req => new Promise(resolve => { releaseA = () => resolve(receipt(req)) }))
  let landC: (() => void) | undefined, rowC: DocumentDraft | undefined
  try {
    r.edit(2)
    const a = r.controller.save()
    await settle(() => r.requests.length === 1)
    let bOutcome: string | undefined
    const b = r.controller.save("ticket-b").then(() => "sent", (error: Error) => error.message).then(outcome => { bOutcome = outcome })
    await settle(() => store.put.mock.calls.length === 2)
      store.put.mockImplementationOnce((row: DocumentDraft) => new Promise<void>(resolve => { rowC = row; landC = () => { rows.set(row.id, row); resolve() } }))
    r.edit(3)
    let cDone = false
    void r.controller.save().finally(() => { cDone = true })
    await settle(() => store.put.mock.calls.length === 3)
    await vi.advanceTimersByTimeAsync(50)
    releaseA()
    await a
    await vi.advanceTimersByTimeAsync(60)
    await settle(() => cDone && bOutcome !== undefined)
    await b
    expect(bOutcome).toBe("storage")
    expect(r.requests).toHaveLength(1)
    landC!()
    await settle(() => rows.size === 1 && [...rows.values()][0].snapshot === rowC?.snapshot)
  } finally { landC?.(); vi.useRealTimers(); await r.controller.close() }
})
