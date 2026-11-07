import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { createSingleEditorSave, NativeSaveError, type NativeSaveRequest, type NativeSaveState } from "./single-editor-save"
import type { NativeDraftJournal } from "./native-draft"

// Read revocation is terminal (#4721, ported from #4723): whichever call learns
// of it, the lifecycle stops, the journal is purged, and the host sees one
// `revoked` state it cannot mistake for an ordinary close or retirement.

beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })
const receipt = (r: NativeSaveRequest) => ({ durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash, workspaceId: r.workspaceId, pageId: r.pageId, applicationId: r.applicationId, enginePin: r.enginePin, generation: r.generation, sessionId: r.sessionId, baseVersion: r.baseVersion, editorRevision: r.editorRevision, contentHash: r.contentHash, version: (BigInt(r.baseVersion) + 1n).toString() })
function rig() {
  const journal = { persist: vi.fn().mockResolvedValue(undefined), confirm: vi.fn().mockResolvedValue(undefined), recovery: vi.fn().mockResolvedValue([]), onRetire: vi.fn(() => () => {}), close: vi.fn().mockResolvedValue(undefined), revokeRead: vi.fn().mockResolvedValue(undefined), discardRecovered: vi.fn().mockResolvedValue(undefined) } satisfies NativeDraftJournal
  const engine = { snapshot: vi.fn().mockResolvedValue({ bytes: new Uint8Array([1, 2]), editorRevision: "doc:1:0" }), validate: vi.fn().mockResolvedValue(undefined) }
  const transport = { save: vi.fn(async (r: NativeSaveRequest) => receipt(r)), reconcile: vi.fn(async (r: NativeSaveRequest) => receipt(r)) }
  const states: NativeSaveState[] = []
  const controller = createSingleEditorSave({ workspaceId: "w", pageId: "p", applicationId: "wordcraft", enginePin: "engine-pin", generation: "1", sessionId: "tab-a", baseVersion: "1", journal, engine, transport, onState: s => { states.push(s) }, idleMs: 20 })
  return { journal, engine, transport, states, controller }
}
const terminal = (states: NativeSaveState[]) => states.slice(states.findIndex(s => s.phase === "revoked"))

it("a host read revocation ends in one terminal revoked state, never in stopped", async () => {
  const r = rig()
  r.controller.edited()
  await r.controller.revokeRead()
  expect(r.journal.revokeRead).toHaveBeenCalledOnce()
  expect(r.states.at(-1)).toEqual({ phase: "revoked", dirty: true, reason: "read_revoked" })
  // Nothing after it, not even from a later edit, close or second revocation.
  r.controller.edited()
  await r.controller.revokeRead()
  await r.controller.close()
  expect(terminal(r.states)).toHaveLength(1)
  expect(r.states.some(s => s.phase === "stopped")).toBe(false)
})

it("a save that reports read_revoked ends revoked, and Ctrl+S and a flush reject with read_revoked", async () => {
  const r = rig()
  r.transport.save.mockRejectedValueOnce(new NativeSaveError("read_revoked"))
  const press = r.controller.save("ticket-1")
  await expect(press).rejects.toBeInstanceOf(NativeSaveError)
  await expect(press).rejects.toMatchObject({ reason: "read_revoked" })
  expect(r.journal.revokeRead).toHaveBeenCalledOnce()
  expect(r.states.at(-1)).toMatchObject({ phase: "revoked", reason: "read_revoked" })
  await expect(r.controller.flush()).rejects.toMatchObject({ reason: "read_revoked" })
  await expect(r.controller.save("ticket-2")).rejects.toMatchObject({ reason: "read_revoked" })
  expect(r.transport.save).toHaveBeenCalledOnce()
  expect(r.journal.confirm).not.toHaveBeenCalled()
  expect(terminal(r.states)).toHaveLength(1)
})

it("a flush waiting on a save in flight rejects with read_revoked when that save reports it", async () => {
  const r = rig()
  let refuse!: (error: unknown) => void
  r.transport.save.mockImplementationOnce(() => new Promise((_, reject) => { refuse = reject }))
  const flushing = r.controller.flush()
  await vi.waitFor(() => expect(r.transport.save).toHaveBeenCalledOnce())
  refuse(new NativeSaveError("read_revoked"))
  await expect(flushing).rejects.toMatchObject({ reason: "read_revoked" })
  expect(r.journal.revokeRead).toHaveBeenCalledOnce()
  expect(r.states.at(-1)).toMatchObject({ phase: "revoked" })
})

// The save's reply and a write retirement can meet inside one microtask
// checkpoint. `ticks` delays the retirement after the transport refused. Below
// 3 ticks the retirement's abort wins the race inside the controller, so the
// refusal is never received: the capture stands and reconcile is asked (it is
// offline here). From 3 ticks the refusal has been received; on main, ticks 3-5
// met the write fence in transmit and the draft survived as a capture.
const sweep = (ticks: number) => {
  const r = rig()
  r.transport.reconcile.mockRejectedValue(new Error("offline"))
  let retiring: Promise<boolean> | undefined
  r.transport.save.mockImplementationOnce(() => {
    const refusal = Promise.reject(new NativeSaveError("read_revoked"))
    void (async () => { for (let i = 0; i < ticks; i++) await undefined; retiring = r.controller.revokeWrite() })()
    return refusal
  })
  return { r, retired: async () => { await r.controller.save().catch(() => {}); await vi.waitFor(() => expect(retiring).toBeDefined()); return retiring! } }
}
it.each([0, 1, 2])("a read refusal the retirement aborted first is never received: the capture stands (%i ticks)", async ticks => {
  const { r, retired } = sweep(ticks)
  await expect(retired()).resolves.toBe(true)
  expect(r.journal.revokeRead).not.toHaveBeenCalled()
  expect(r.transport.reconcile).toHaveBeenCalledOnce()
  await r.controller.close()
})
it.each([3, 4, 5, 6, 8, 11])("a received read refusal is handled ahead of a write retirement %i ticks later", async ticks => {
  const { r, retired } = sweep(ticks)
  await expect(retired()).resolves.toBe(false)
  expect(r.journal.revokeRead).toHaveBeenCalledOnce()
  expect(r.journal.confirm).not.toHaveBeenCalled()
  expect(terminal(r.states)).toEqual([expect.objectContaining({ phase: "revoked", reason: "read_revoked" })])
  await r.controller.close()
})
