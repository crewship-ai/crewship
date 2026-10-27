// @vitest-environment node
import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { createSingleEditorSave, NativeSaveError, type NativeSaveRequest, type NativeSaveState } from "./single-editor-save"
import type { NativeDraft } from "./native-draft"

beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => vi.unstubAllGlobals())
const receipt = (r: NativeSaveRequest) => ({ ...r, version: String(BigInt(r.baseVersion) + 1n), durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash })
const deferred = <T,>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
function fixture() {
  let value = 1, nativeState = "state:1", captures = 0, stored: NativeDraft | undefined
  let onSnapshot: (() => void) | undefined
  const requests: NativeSaveRequest[] = [], states: NativeSaveState[] = []
  const onReceipt = vi.fn(), confirm = vi.fn(async (draft: NativeDraft) => { if (stored?.editorRevision === draft.editorRevision) stored = undefined })
  const send = vi.fn(async (r: NativeSaveRequest): Promise<unknown> => receipt(r))
  const controller = createSingleEditorSave({ workspaceId: "w", pageId: "p", applicationId: "wordcraft", enginePin: "engine", baseVersion: "1", generation: "1", sessionId: "session", idleMs: 5,
    journal: { async persist(d) { stored = d }, confirm, async recovery() { return [] }, onRetire() { return () => {} }, async close() {}, async revokeRead() {} },
    engine: { async snapshot(_signal, id) { captures++; onSnapshot?.(); return { bytes: new Uint8Array([value]), editorRevision: id ?? `rpc:${captures}`, nativeState } }, async validate() {} },
    transport: { async save(r) { requests.push(r); return send(r) } }, onState(state) { states.push(state) }, onReceipt,
  })
  return { controller, requests, states, onReceipt, send, confirm, get captures() { return captures }, get stored() { return stored }, set onSnapshot(callback: (() => void) | undefined) { onSnapshot = callback }, edit(nextValue: number, nextState: string) { value = nextValue; nativeState = nextState; controller.edited() } }
}
const settled = async (r: ReturnType<typeof fixture>, captures: number) => vi.waitFor(() => { expect(r.captures).toBeGreaterThanOrEqual(captures); expect(r.states.at(-1)).toEqual({ phase: "saved", dirty: false }) })

it("coalesces a queued idle RPC with identical confirmed native state and bytes, while saving the next real edit", async () => {
  const r = fixture()
  r.onSnapshot = () => { if (r.captures === 1) r.controller.edited() } // A changed notification arrives during the manual snapshot.
  try {
    await r.controller.save("native-save-1")
    await settled(r, 2)
    expect(r.requests).toHaveLength(1)
    expect(r.onReceipt).toHaveBeenCalledTimes(1)
    expect(r.stored).toBeUndefined()
    r.edit(2, "state:2")
    await settled(r, 3)
    expect(r.requests).toHaveLength(2)
    expect(r.requests[1].bytes).toEqual(new Uint8Array([2]))
    expect(r.requests[1].contentHash).not.toBe(r.requests[0].contentHash)
    expect(r.onReceipt).toHaveBeenCalledTimes(2)
  } finally { await r.controller.close() }
})

it("does not drop changed bytes even if an engine reports the same native state", async () => {
  const r = fixture()
  try { await r.controller.save("manual"); r.edit(2, "state:1"); await settled(r, 2); expect(r.requests).toHaveLength(2); expect(r.requests[1].bytes).toEqual(new Uint8Array([2])) } finally { await r.controller.close() }
})

it("requires a fresh receipt for a different native state even when its bytes are identical", async () => {
  const r = fixture()
  try { await r.controller.save("manual"); r.edit(1, "state:2"); await settled(r, 2); expect(r.requests).toHaveLength(2); expect(r.onReceipt).toHaveBeenCalledTimes(2) } finally { await r.controller.close() }
})

it("keeps both explicit native tickets and an explicit transfer save on fresh exact receipts", async () => {
  const r = fixture()
  try {
    await r.controller.save("ticket-one"); await r.controller.save("ticket-two"); await r.controller.save()
    expect(r.requests).toHaveLength(3)
    expect(r.requests.map(x => x.editorRevision)).toEqual(["ticket-one", "ticket-two", "rpc:3"])
    expect(new Set(r.requests.map(x => x.contentHash)).size).toBe(1)
    expect(r.onReceipt).toHaveBeenCalledTimes(3)
  } finally { await r.controller.close() }
})

it("does not use an unknown ACK to confirm a repeated idle snapshot or mark it Saved", async () => {
  const r = fixture(); r.send.mockRejectedValueOnce(new Error("lost acknowledgement"))
  try {
    await expect(r.controller.save("manual")).rejects.toThrow("unconfirmed"); r.edit(1, "state:1")
    await vi.waitFor(() => expect(r.captures).toBeGreaterThanOrEqual(2))
    expect(r.requests).toHaveLength(1); expect(r.confirm).not.toHaveBeenCalled(); expect(r.onReceipt).not.toHaveBeenCalled()
    expect(r.states.at(-1)).toEqual(expect.objectContaining({ phase: "uncertain", dirty: true }))
    expect(r.stored).toBeDefined()
  } finally { await r.controller.close() }
})

it("retains a newer draft and dirty edit while an older redundant idle confirmation is queued", async () => {
  const r = fixture(), entered = deferred<void>(), release = deferred<void>(), nextAck = deferred<unknown>()
  try {
    await r.controller.save("manual")
    const exactConfirm = r.confirm.getMockImplementation()!
    r.confirm.mockImplementationOnce(async draft => { entered.resolve(); await release.promise; await exactConfirm(draft) })
    r.edit(1, "state:1"); await entered.promise
    r.send.mockImplementationOnce(() => nextAck.promise)
    r.edit(2, "state:2")
    await vi.waitFor(() => { expect(r.requests).toHaveLength(2); expect(r.stored?.bytes).toEqual(new Uint8Array([2])) })
    const older = r.confirm.mock.calls[1][0].editorRevision
    release.resolve(); await new Promise(done => setTimeout(done, 20))
    expect(r.stored?.bytes).toEqual(new Uint8Array([2]))
    expect(r.stored?.editorRevision).not.toBe(older)
    expect(r.states.at(-1)?.dirty).toBe(true)
    expect(r.onReceipt).toHaveBeenCalledTimes(1)
    nextAck.resolve(receipt(r.requests[1])); await settled(r, 3)
    expect(r.stored).toBeUndefined(); expect(r.onReceipt).toHaveBeenCalledTimes(2)
  } finally { release.resolve(); await r.controller.close() }
})

it.each(["unconfirmed", "conflict"] as const)("does not replace a newer manual %s result with an older idle confirmation", async failure => {
  const r = fixture(), entered = deferred<void>(), release = deferred<void>()
  try {
    await r.controller.save("manual-one")
    const exactConfirm = r.confirm.getMockImplementation()!
    r.confirm.mockImplementationOnce(async draft => { entered.resolve(); await release.promise; await exactConfirm(draft) })
    r.edit(1, "state:1"); await entered.promise
    r.send.mockRejectedValueOnce(failure === "unconfirmed" ? new Error("lost newer acknowledgement") : new NativeSaveError("conflict"))
    await expect(r.controller.save("manual-two")).rejects.toThrow(failure)
    expect(r.stored?.editorRevision).toBe("manual-two")
    release.resolve(); await new Promise(done => setTimeout(done, 20))
    expect(r.states.at(-1)).toEqual(expect.objectContaining({ phase: failure === "unconfirmed" ? "uncertain" : "draft", dirty: true }))
    expect(r.stored?.editorRevision).toBe("manual-two")
    expect(r.requests).toHaveLength(2); expect(r.onReceipt).toHaveBeenCalledTimes(1)
  } finally { release.resolve(); await r.controller.close() }
})

it("does not let an older idle confirmation replace a newer confirmed state and version", async () => {
  const r = fixture(), entered = deferred<void>(), release = deferred<void>()
  try {
    await r.controller.save("manual-one")
    const exactConfirm = r.confirm.getMockImplementation()!
    r.confirm.mockImplementationOnce(async draft => { entered.resolve(); await release.promise; await exactConfirm(draft) })
    r.edit(1, "state:1"); await entered.promise
    r.edit(2, "state:2"); await r.controller.save("manual-two")
    expect(r.states.at(-1)).toEqual({ phase: "saved", dirty: false })
    const beforeRelease = r.states.length
    release.resolve(); await new Promise(done => setTimeout(done, 20))
    expect(r.states.slice(beforeRelease)).toEqual([])
    expect(r.states.at(-1)).toEqual({ phase: "saved", dirty: false })
    expect(r.requests).toHaveLength(2); expect(r.onReceipt).toHaveBeenCalledTimes(2)
    expect(r.stored).toBeUndefined()
  } finally { release.resolve(); await r.controller.close() }
})
