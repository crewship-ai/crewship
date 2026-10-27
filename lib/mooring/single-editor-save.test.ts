import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { createSingleEditorSave, NativeSaveError, type NativeSaveRequest } from "./single-editor-save"
import type { NativeDraftJournal } from "./native-draft"

beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
const receipt = (r: NativeSaveRequest) => ({ durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash, workspaceId: r.workspaceId, pageId: r.pageId, applicationId: r.applicationId, enginePin: r.enginePin, generation: r.generation, sessionId: r.sessionId, baseVersion: r.baseVersion, editorRevision: r.editorRevision, contentHash: r.contentHash, version: (BigInt(r.baseVersion) + 1n).toString() })
function rig(baseVersion = "1", generation = "1") {
  const journal = { persist: vi.fn().mockResolvedValue(undefined), confirm: vi.fn().mockResolvedValue(undefined), recovery: vi.fn().mockResolvedValue([]), onRetire: vi.fn(() => () => {}), close: vi.fn().mockResolvedValue(undefined), revokeRead: vi.fn().mockResolvedValue(undefined) } satisfies NativeDraftJournal
  const engine = { snapshot: vi.fn().mockResolvedValue({ bytes: new Uint8Array([1, 2]), editorRevision: "doc:1:0" }), validate: vi.fn().mockResolvedValue(undefined) }
  const transport = { save: vi.fn(async (r: NativeSaveRequest) => receipt(r)), reconcile: vi.fn(async (r: NativeSaveRequest) => receipt(r)) }
  const state = vi.fn()
  const controller = createSingleEditorSave({ workspaceId: "w", pageId: "p", applicationId: "wordcraft", enginePin: "engine-pin", generation, sessionId: "tab-a", baseVersion, journal, engine, transport, onState: state, idleMs: 20 })
  return { journal, engine, transport, state, controller }
}

it("durably journals whole native bytes before validating and sending a version", async () => {
  const r = rig(), disk = deferred<void>()
  r.journal.persist.mockReturnValueOnce(disk.promise)
  r.engine.snapshot.mockResolvedValueOnce({ bytes: new Uint8Array(5 * 1024 * 1024), editorRevision: "doc:1:0" })
  const saving = r.controller.save()
  await vi.waitFor(() => expect(r.journal.persist).toHaveBeenCalledOnce())
  expect(r.transport.save).not.toHaveBeenCalled()
  disk.resolve()
  await saving
  expect(r.transport.save.mock.calls[0][0].bytes.byteLength).toBe(5 * 1024 * 1024)
  expect(r.journal.persist.mock.invocationCallOrder[0]).toBeLessThan(r.transport.save.mock.invocationCallOrder[0])
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "saved", dirty: false }))
  await r.controller.close()
})

it("never sends or reports Saved if durable storage is unavailable", async () => {
  const r = rig()
  r.journal.persist.mockRejectedValueOnce(new Error("disk unavailable"))
  await r.controller.save()
  expect(r.transport.save).not.toHaveBeenCalled()
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "draft", dirty: true, reason: "storage" }))
  await r.controller.close()
})

it("keeps a lost acknowledgement uncertain, never retries blindly, and reconciles exact content", async () => {
  const r = rig()
  r.transport.save.mockRejectedValueOnce(new Error("lost ACK"))
  await r.controller.save()
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "uncertain", dirty: true }))
  expect(r.journal.confirm).not.toHaveBeenCalled()
  await r.controller.save()
  expect(r.transport.save).toHaveBeenCalledOnce()
  await r.controller.reconcile()
  expect(r.transport.reconcile).toHaveBeenCalledOnce()
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "saved", dirty: false }))
  await r.controller.close()
})

it("rejects an ACK with another generation or content and preserves the local draft", async () => {
  const r = rig()
  r.transport.save.mockImplementationOnce(async req => ({ ...receipt(req), generation: "2" }))
  await r.controller.save()
  expect(r.journal.confirm).not.toHaveBeenCalled()
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "uncertain" }))
  r.transport.reconcile.mockImplementationOnce(async req => ({ ...receipt(req), contentHash: "wrong" }))
  await r.controller.reconcile()
  expect(r.journal.confirm).not.toHaveBeenCalled()
  await r.controller.close()
})

it("times out a missing ACK and ignores its later response without replay", async () => {
  vi.useFakeTimers()
  const r = rig(), ack = deferred<ReturnType<typeof receipt>>()
  r.transport.save.mockReturnValueOnce(ack.promise)
  const saving = r.controller.save()
  await vi.waitFor(() => expect(r.transport.save).toHaveBeenCalledOnce())
  await vi.advanceTimersByTimeAsync(10_000)
  await saving
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "uncertain", dirty: true }))
  ack.resolve(receipt(r.transport.save.mock.calls[0][0]))
  await Promise.resolve()
  expect(r.journal.confirm).not.toHaveBeenCalled()
  expect(r.transport.save).toHaveBeenCalledOnce()
  await r.controller.close()
})

it("ignores a late old-generation ACK after write revocation", async () => {
  const r = rig(), ack = deferred<ReturnType<typeof receipt>>()
  r.transport.save.mockReturnValueOnce(ack.promise)
  const saving = r.controller.save()
  await vi.waitFor(() => expect(r.transport.save).toHaveBeenCalledOnce())
  r.controller.revokeWrite()
  ack.resolve(receipt(r.transport.save.mock.calls[0][0]))
  await saving
  expect(r.journal.confirm).not.toHaveBeenCalled()
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "read-only", dirty: true }))
  await r.controller.close()
})

it.each([{ durability: "memory" }, { issuedBy: "client" }, { phase: "prepared" }, { candidateHash: "other-content" }, { pageId: "foreign-page" }])("rejects an unqualified durability or publication receipt %j", async wrong => {
  const r = rig()
  r.transport.save.mockImplementationOnce(async req => ({ ...receipt(req), ...wrong }))
  await r.controller.save()
  expect(r.journal.confirm).not.toHaveBeenCalled()
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "uncertain" }))
  await r.controller.close()
})

it.each(["0", "banana", "3", "02", "-1", "9223372036854775808"])("rejects non-successor or noncanonical version %s", async version => {
  const r = rig()
  r.transport.save.mockImplementationOnce(async req => ({ ...receipt(req), version }))
  await r.controller.save()
  expect(r.journal.confirm).not.toHaveBeenCalled()
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "uncertain", dirty: true }))
  await r.controller.close()
})

it.each(["word-doc:9007199254740993:17", "grid-doc:18446744073709551615"])("preserves opaque engine revision %s without numeric conversion", async revision => {
  const r = rig()
  r.engine.snapshot.mockResolvedValueOnce({ bytes: new Uint8Array([1, 2]), editorRevision: revision })
  await r.controller.save()
  expect(r.transport.save).toHaveBeenCalledOnce()
  expect(r.transport.save.mock.calls[0][0].editorRevision).toBe(revision)
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "saved", dirty: false }))
  await r.controller.close()
})

it.each(["01", "-1", "abc", "9223372036854775808"])("rejects invalid canonical base version %s", base => {
  expect(() => rig(base)).toThrow()
})

it.each([["0", "1"], ["9007199254740993", "9007199254740994"]])("checks the exact successor %s→%s without losing precision", async (base, version) => {
  const r = rig(base)
  r.transport.save.mockImplementationOnce(async req => ({ ...receipt(req), version }))
  await r.controller.save()
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "saved", dirty: false }))
  await r.controller.close()
})

it.each(["conflict", "quota"] as const)("preserves drafts on %s and does not automatically retry", async reason => {
  const r = rig()
  r.transport.save.mockRejectedValueOnce(new NativeSaveError(reason))
  await r.controller.save()
  await r.controller.save()
  expect(r.journal.confirm).not.toHaveBeenCalled()
  expect(r.transport.save).toHaveBeenCalledOnce()
  await r.controller.close()
})

it("keeps edits during an in-flight save dirty and persists them without replaying the original", async () => {
  const r = rig(), ack = deferred<ReturnType<typeof receipt>>()
  r.transport.save.mockReturnValueOnce(ack.promise)
  const first = r.controller.save()
  await vi.waitFor(() => expect(r.transport.save).toHaveBeenCalledOnce())
  r.engine.snapshot.mockResolvedValue({ bytes: new Uint8Array([3]), editorRevision: "doc:2:0" })
  r.controller.edited()
  await r.controller.save()
  expect(r.journal.persist).toHaveBeenCalledTimes(2)
  expect(r.transport.save).toHaveBeenCalledOnce()
  ack.resolve(receipt(r.transport.save.mock.calls[0][0]))
  await first
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ dirty: true }))
  await r.controller.save()
  expect(r.transport.save.mock.calls[1][0]).toMatchObject({ baseVersion: "2", editorRevision: "doc:2:0" })
  await r.controller.close()
})

it("does not let an older valid ACK hide a newer failed manual snapshot", async () => {
  const r = rig(), ack = deferred<ReturnType<typeof receipt>>()
  r.transport.save.mockReturnValueOnce(ack.promise)
  const first = r.controller.save()
  await vi.waitFor(() => expect(r.transport.save).toHaveBeenCalledOnce())
  r.engine.snapshot.mockResolvedValueOnce({ bytes: new Uint8Array([3]), editorRevision: "doc:2:0" })
  r.engine.validate.mockRejectedValueOnce(new Error("new native state failed validation"))
  await r.controller.save()
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "draft", dirty: true }))
  ack.resolve(receipt(r.transport.save.mock.calls[0][0]))
  await first
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ dirty: true }))
  expect(r.state.mock.calls.at(-1)?.[0].phase).not.toBe("saved")
  await r.controller.close()
})

it("does not persist a late snapshot after read revocation or call transport", async () => {
  const r = rig(), snapshot = deferred<{ bytes: Uint8Array; editorRevision: string }>()
  r.engine.snapshot.mockReturnValueOnce(snapshot.promise)
  const saving = r.controller.save()
  await vi.waitFor(() => expect(r.engine.snapshot).toHaveBeenCalledOnce())
  await r.controller.revokeRead()
  snapshot.resolve({ bytes: new Uint8Array([9]), editorRevision: "doc:1:0" })
  await saving
  expect(r.journal.persist).not.toHaveBeenCalled()
  expect(r.transport.save).not.toHaveBeenCalled()
  expect(r.journal.revokeRead).toHaveBeenCalledOnce()
})

it("debounces native saving without relying on closing or changing tab visibility", async () => {
  vi.useFakeTimers()
  const r = rig()
  r.controller.edited()
  await vi.advanceTimersByTimeAsync(19)
  expect(r.transport.save).not.toHaveBeenCalled()
  await vi.advanceTimersByTimeAsync(1)
  await vi.waitFor(() => expect(r.transport.save).toHaveBeenCalledOnce())
  await r.controller.close()
})


it("reconcile cannot adopt a later head instead of the pinned commit successor", async () => {
  const r = rig()
  r.transport.save.mockRejectedValueOnce(new Error("lost ACK"))
  await r.controller.save()
  r.transport.reconcile.mockImplementationOnce(async req => ({ ...receipt(req), version: "3" }))
  await r.controller.reconcile()
  expect(r.journal.confirm).not.toHaveBeenCalled()
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "uncertain", dirty: true }))
  await r.controller.close()
})

it("uses snapshot order rather than ordering opaque revision tokens", async () => {
  const r = rig()
  r.engine.snapshot.mockResolvedValueOnce({ bytes: new Uint8Array([1]), editorRevision: "z-doc:99:0" })
  await r.controller.save()
  r.engine.snapshot.mockResolvedValueOnce({ bytes: new Uint8Array([2]), editorRevision: "a-doc:1:0" })
  await r.controller.save()
  expect(r.transport.save).toHaveBeenCalledTimes(2)
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "saved", dirty: false }))
  await r.controller.close()
})

it("rejects changed bytes under the same opaque engine revision token", async () => {
  const r = rig()
  await r.controller.save()
  r.engine.snapshot.mockResolvedValueOnce({ bytes: new Uint8Array([9]), editorRevision: "doc:1:0" })
  await r.controller.save()
  expect(r.transport.save).toHaveBeenCalledOnce()
  expect(r.journal.persist).toHaveBeenCalledTimes(2)
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "draft", dirty: true, reason: "engine" }))
  await r.controller.close()
})


it("preserves a canonical int64 generation above the JavaScript integer limit", async () => {
  const r = rig("1", "9007199254740993")
  await r.controller.save()
  expect(r.transport.save.mock.calls[0][0].generation).toBe("9007199254740993")
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "saved", dirty: false }))
  await r.controller.close()
})


it("rejects an ACK for a neighboring large generation without rounding", async () => {
  const r = rig("1", "9007199254740993")
  r.transport.save.mockImplementationOnce(async req => ({ ...receipt(req), generation: "9007199254740992" }))
  await r.controller.save()
  expect(r.journal.confirm).not.toHaveBeenCalled()
  expect(r.state).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "uncertain", dirty: true }))
  await r.controller.close()
})

it.each(["0", "01", "-1", "9223372036854775808"])("rejects a noncanonical positive generation %s", generation => {
  expect(() => rig("1", generation)).toThrow()
})
