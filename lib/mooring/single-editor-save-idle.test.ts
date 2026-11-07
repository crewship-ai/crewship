// @vitest-environment node
import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { createSingleEditorSave, NativeSaveError, type NativeSaveRequest, type NativeSaveState } from "./single-editor-save"
import type { NativeDraft } from "./native-draft"

beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => vi.unstubAllGlobals())
const receipt = (r: NativeSaveRequest) => ({ ...r, version: String(BigInt(r.baseVersion) + 1n), durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash })
const deferred = <T,>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
function fixture(timeoutMs = 10_000, applicationId: "wordcraft" | "gridcraft" = "wordcraft") {
  let value = 1, nativeState = "state:1", captures = 0, stored: NativeDraft | undefined
  let onSnapshot: (() => void) | undefined, revision: string | undefined
  let persistGate: Promise<void> | undefined
  let wrapSnapshot: ((taken: ReturnType<typeof takeSnapshot>) => Promise<ReturnType<typeof takeSnapshot>>) | undefined
  const takeSnapshot = (id?: string) => { captures++; onSnapshot?.(); return { bytes: new Uint8Array([value]), editorRevision: id ?? `rpc:${captures}`, nativeState, revision } }
  const requests: NativeSaveRequest[] = [], states: NativeSaveState[] = []
  const onReceipt = vi.fn(), confirm = vi.fn(async (draft: NativeDraft) => { if (stored?.editorRevision === draft.editorRevision) stored = undefined })
  const send = vi.fn(async (r: NativeSaveRequest): Promise<unknown> => receipt(r))
  const controller = createSingleEditorSave({ workspaceId: "w", pageId: "p", applicationId, enginePin: "engine", baseVersion: "1", generation: "1", sessionId: "session", idleMs: 5,
    journal: { async persist(d) { await persistGate; stored = d }, confirm, async recovery() { return [] }, onRetire() { return () => {} }, async close() {}, async revokeRead() {}, async discardRecovered() {} },
    engine: { async snapshot(_signal, id) { const taken = takeSnapshot(id); return wrapSnapshot ? wrapSnapshot(taken) : taken }, async validate() {} },
    transport: { async save(r) { requests.push(r); return send(r) } }, onState(state) { states.push(state) }, onReceipt, timeoutMs,
  })
  return { controller, requests, states, onReceipt, send, confirm, get captures() { return captures }, get stored() { return stored }, set onSnapshot(callback: (() => void) | undefined) { onSnapshot = callback }, set revision(next: string | undefined) { revision = next }, set persistGate(next: Promise<void> | undefined) { persistGate = next }, set wrapSnapshot(next: typeof wrapSnapshot) { wrapSnapshot = next }, edit(nextValue: number, nextState: string, changed?: string) { value = nextValue; nativeState = nextState; controller.edited(changed) } }
}
const settled = async (r: ReturnType<typeof fixture>, captures: number) => vi.waitFor(() => { expect(r.captures).toBeGreaterThanOrEqual(captures); expect(r.states.at(-1)).toEqual({ phase: "saved", dirty: false }) })

it("flush waits for the actual clean Saved state after a changed pulse during its identical native snapshot", async () => {
  const r = fixture()
  r.onSnapshot = () => { if (r.captures === 1) r.controller.edited() }
  try {
    await r.controller.flush()
    expect(r.states.at(-1)).toEqual({ phase: "saved", dirty: false })
    expect(r.captures).toBeGreaterThanOrEqual(2)
    expect(r.requests).toHaveLength(1)
    expect(r.onReceipt).toHaveBeenCalledTimes(1)
    expect(r.stored).toBeUndefined()
  } finally { await r.controller.close() }
})

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

it("keeps explicit native tickets on their own exact confirmation without a second save (#5075)", async () => {
  const r = fixture()
  try {
    await r.controller.save("ticket-one"); await r.controller.save("ticket-two"); await r.controller.save()
    expect(r.requests.map(x => x.editorRevision)).toEqual(["ticket-one"])
    expect(r.onReceipt.mock.calls.map(call => [call[0].editorRevision, call[0].version, call[0].unchanged])).toEqual([["ticket-one", "2", undefined], ["ticket-two", "2", true], ["rpc:3", "2", true]])
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
    r.edit(2, "state:2")
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


it("flush rejects an unknown ACK while waiting for the newer state to settle", async () => {
  const r = fixture()
  r.onSnapshot = () => { if (r.captures === 1) r.controller.edited(); else if (r.captures === 2) r.edit(2, "state:2") }
  r.send.mockImplementationOnce(async request => receipt(request)).mockRejectedValueOnce(Error("lost acknowledgement"))
  try {
    await expect(r.controller.flush()).rejects.toThrow("unconfirmed")
    expect(r.states.at(-1)).toMatchObject({ phase: "uncertain", dirty: true })
    expect(r.requests).toHaveLength(2)
    expect(r.stored).toBeDefined()
  } finally { await r.controller.close() }
})

it("flush rejects storage failure while a redundant snapshot is settling", async () => {
  const r = fixture(), originalConfirm = r.confirm.getMockImplementation()!
  r.onSnapshot = () => { if (r.captures === 1) r.controller.edited() }
  r.confirm.mockImplementationOnce(originalConfirm).mockRejectedValueOnce(Error("storage failed"))
  try {
    await expect(r.controller.flush()).rejects.toThrow("storage")
    // The server holds exactly these bytes; only the local row is left (#4870).
    expect(r.states.at(-1)).toMatchObject({ phase: "draft", dirty: true, reason: "unretired" })
    expect(r.requests).toHaveLength(1)
    expect(r.stored).toBeDefined()
  } finally { await r.controller.close() }
})

it.each(["write", "read", "close"] as const)("flush rejects %s retirement while a redundant snapshot is settling", async retirement => {
  const r = fixture(), entered = deferred<void>(), release = deferred<void>(), originalConfirm = r.confirm.getMockImplementation()!
  r.onSnapshot = () => { if (r.captures === 1) r.controller.edited() }
  r.confirm.mockImplementationOnce(originalConfirm).mockImplementationOnce(async draft => { entered.resolve(); await release.promise; await originalConfirm(draft) })
  const flush = r.controller.flush()
  const rejected = expect(flush).rejects.toThrow(retirement === "write" ? "write_revoked" : "native_session_retired")
  try {
    await entered.promise
    const retired = retirement === "write" ? r.controller.revokeWrite() : retirement === "read" ? r.controller.revokeRead() : r.controller.close()
    await rejected
    release.resolve(); await retired
    expect(r.requests).toHaveLength(1)
    expect(r.states.at(-1)?.phase).not.toBe("saved")
  } finally { release.resolve(); await r.controller.close() }
})

it("flush times out if local settling hangs and cannot be revived by its late completion", async () => {
  const r = fixture(20), entered = deferred<void>(), release = deferred<void>(), originalConfirm = r.confirm.getMockImplementation()!
  r.onSnapshot = () => { if (r.captures === 1) r.controller.edited() }
  r.confirm.mockImplementationOnce(originalConfirm).mockImplementationOnce(async draft => { entered.resolve(); await release.promise; await originalConfirm(draft) })
  const flush = r.controller.flush(), rejected = expect(flush).rejects.toThrow("native_flush_timeout")
  try {
    await entered.promise; await rejected
    expect(r.requests).toHaveLength(1)
    release.resolve()
    await settled(r, 2)
    await expect(flush).rejects.toThrow("native_flush_timeout")
  } finally { release.resolve(); await r.controller.close() }
})

describe("a hold while access is confirmed", () => {
  const typed = async (r: ReturnType<typeof fixture>) => { r.edit(2, "state:2"); await vi.waitFor(() => expect(r.stored?.bytes[0]).toBe(2)) }
  const held = () => { const r = fixture(); r.send.mockImplementation(() => new Promise(() => {})); return r }
  it("input during a hold is not captured, and a loss of Edit keeps the row from before it", async () => {
    const r = held()
    try {
      await typed(r)
      const row = r.stored, captures = r.captures
      r.controller.hold()
      r.edit(3, "state:3")
      await new Promise(done => setTimeout(done, 400))
      expect(await r.controller.revokeWrite()).toBe(true)
      expect(r.captures).toBe(captures)
      expect(r.stored).toBe(row)
    } finally { await r.controller.close() }
  })
  it("input during a hold is saved once it is released", async () => {
    const r = fixture()
    try {
      r.controller.hold()
      r.edit(3, "state:3")
      await new Promise(done => setTimeout(done, 400))
      expect(r.requests).toHaveLength(0)
      r.controller.release()
      await vi.waitFor(() => expect(r.requests).toHaveLength(1))
      expect(r.requests[0].bytes[0]).toBe(3)
    } finally { await r.controller.close() }
  })
  it("an autosave a hold replaces is still captured, without sending, and a loss of Edit reports that row", async () => {
    const r = fixture()
    try {
      r.edit(3, "state:3")
      r.controller.hold()
      expect(await r.controller.revokeWrite()).toBe(true)
      expect(r.stored?.bytes[0]).toBe(3)
      expect(r.requests).toHaveLength(0)
    } finally { await r.controller.close() }
  })
  it("a pre-return capture that fails is no success: the buffer is kept", async () => {
    const r = fixture()
    r.onSnapshot = () => { throw new Error("engine") }
    try {
      r.edit(3, "state:3")
      r.controller.hold()
      expect(await r.controller.revokeWrite()).toBe(false)
    } finally { await r.controller.close() }
  })
  it("a snapshot answering after held input does not replace the row", async () => {
    const r = held()
    try {
      await typed(r)
      r.onSnapshot = () => { r.controller.hold(); r.edit(9, "state:9") }
      r.edit(3, "state:3")
      await new Promise(done => setTimeout(done, 400))
      expect(r.stored?.bytes[0]).toBe(2)
      expect(await r.controller.revokeWrite()).toBe(false)
      expect(r.stored?.bytes[0]).toBe(2)
    } finally { await r.controller.close() }
  })
  it("a snapshot the engine dates at the hold is written though held input was heard first", async () => {
    const r = held()
    try {
      let answer!: () => void
      const late = new Promise<void>(done => { answer = done })
      r.revision = "10:18:0"
      r.onSnapshot = () => { r.onSnapshot = undefined; void answerAfter() }
      const answerAfter = async () => { r.controller.hold(); r.edit(9, "state:9", "10:19:0"); answer() }
      r.edit(3, "state:3", "10:18:0")
      await late
      await vi.waitFor(() => expect(r.stored).toBeDefined())
      expect(await r.controller.revokeWrite()).toBe(true)
    } finally { await r.controller.close() }
  })
  it("a capture on hide reaches the journal alone, and the autosave sends it afterwards", async () => {
    const r = fixture()
    try {
      r.edit(3, "state:3")
      await r.controller.captureLocal()
      expect(r.stored?.bytes[0]).toBe(3)
      expect(r.requests).toHaveLength(0)
      await vi.waitFor(() => expect(r.requests).toHaveLength(1))
    } finally { await r.controller.close() }
  })
  it("the row from a capture on hide is the row before the return", async () => {
    const r = held()
    try {
      r.edit(3, "state:3")
      await r.controller.captureLocal()
      const captures = r.captures
      r.controller.hold()
      r.edit(9, "state:9")
      expect(await r.controller.revokeWrite()).toBe(true)
      expect(r.captures).toBe(captures)
      expect(r.stored?.bytes[0]).toBe(3)
    } finally { await r.controller.close() }
  })
  it("a capture on hide is not taken during a hold or without unsaved work", async () => {
    const r = fixture()
    try {
      await r.controller.captureLocal()
      r.controller.hold()
      r.edit(9, "state:9")
      await r.controller.captureLocal()
      expect(r.captures).toBe(0)
    } finally { await r.controller.close() }
  })
  it.each([["wordcraft", "10:1:0", "10:2:0", "10:3:0"], ["gridcraft", "doc:1", "doc:2", "doc:3"]] as const)("%s: a delayed older capture does not vouch for work confirmed by an earlier hold", async (application, older, confirmedWork, later) => {
    const r = fixture(10_000, application)
    r.send.mockImplementation(() => new Promise(() => {}))
    let answer!: () => void
    try {
      r.edit(2, "state:2", older)
      r.revision = older
      const gate = new Promise<void>(done => { answer = done })
      r.wrapSnapshot = async taken => { r.wrapSnapshot = undefined; await gate; return taken }
      void r.controller.captureLocal()
      await vi.waitFor(() => expect(r.captures).toBe(1))
      r.controller.hold()
      r.edit(3, "state:3", confirmedWork)
      r.controller.release()
      r.controller.hold()
      r.edit(4, "state:4", later)
      answer()
      await new Promise(done => setTimeout(done, 50))
      expect(await r.controller.revokeWrite()).toBe(false)
    } finally { await r.controller.close() }
  })
  it("a proof earned in one hold is not credited with a later hold's count", async () => {
    const r = held()
    let answer!: () => void, stored!: () => void
    try {
      r.edit(2, "state:2", "10:1:0")
      r.revision = "10:1:0"
      const answered = new Promise<void>(done => { answer = done })
      r.persistGate = new Promise<void>(done => { stored = done })
      r.wrapSnapshot = async taken => { r.wrapSnapshot = undefined; await answered; return taken }
      void r.controller.captureLocal()
      await vi.waitFor(() => expect(r.captures).toBe(1))
      r.controller.hold()
      r.edit(3, "state:3", "10:2:0")
      answer()
      await new Promise(done => setTimeout(done, 20))
      r.controller.release()
      r.controller.hold()
      r.edit(4, "state:4", "10:3:0")
      stored()
      await new Promise(done => setTimeout(done, 20))
      expect(await r.controller.revokeWrite()).toBe(false)
    } finally { await r.controller.close() }
  })
  it("a release during a retirement does not change what that retirement answers", async () => {
    const r = held()
    try {
      r.edit(2, "state:2")
      await r.controller.captureLocal()
      r.controller.hold()
      const retiring = r.controller.revokeWrite()
      r.controller.release()
      expect(await retiring).toBe(true)
    } finally { await r.controller.close() }
  })
  it("a Sheet export raises its own revision, so it counts as older only below the first held edit", async () => {
    const run = async (held: string, answered: string) => {
      const r = fixture(10_000, "gridcraft"); r.send.mockImplementation(() => new Promise(() => {}))
      try {
        r.revision = answered
        r.onSnapshot = () => { r.onSnapshot = undefined; r.controller.hold(); r.edit(9, "state:9", held) }
        r.edit(3, "state:3", "doc:5")
        await new Promise(done => setTimeout(done, 400))
        return r.stored !== undefined
      } finally { await r.controller.close() }
    }
    expect(await run("doc:7", "doc:6")).toBe(true)
    expect(await run("doc:6", "doc:6")).toBe(false)
  })
  it("a snapshot of a later revision stays out", async () => {
    const r = held()
    try {
      await typed(r)
      r.edit(3, "state:3", "10:18:0")
      r.controller.hold()
      r.revision = "10:19:0"
      r.edit(9, "state:9", "10:19:0")
      await new Promise(done => setTimeout(done, 50))
      expect(r.stored?.bytes[0]).toBe(2)
      expect(await r.controller.revokeWrite()).toBe(false)
    } finally { await r.controller.close() }
  })
  it("nothing is sent during a hold, not even a capture from before it", async () => {
    const r = fixture()
    try {
      r.edit(3, "state:3")
      r.controller.hold()
      await vi.waitFor(() => expect(r.stored?.bytes[0]).toBe(3))
      await new Promise(done => setTimeout(done, 50))
      expect(r.requests).toHaveLength(0)
      r.controller.release()
      await vi.waitFor(() => expect(r.requests).toHaveLength(1))
    } finally { await r.controller.close() }
  })
  it("typing confirmed by a release reaches the journal while a save is uncertain", async () => {
    const r = fixture(20)
    r.send.mockImplementation(() => new Promise(() => {}))
    try {
      r.edit(2, "state:2")
      await vi.waitFor(() => expect(r.states.at(-1)?.phase).toBe("uncertain"))
      r.controller.hold()
      await new Promise(done => setTimeout(done, 20))
      r.edit(3, "state:3")
      r.controller.release()
      await vi.waitFor(() => expect(r.stored?.bytes[0]).toBe(3))
      expect(r.requests).toHaveLength(1)
    } finally { await r.controller.close() }
  })
  it("a release after Edit is gone captures nothing", async () => {
    const r = held()
    try {
      await typed(r)
      r.controller.hold()
      r.edit(3, "state:3")
      await r.controller.revokeWrite()
      const captures = r.captures
      r.controller.release()
      await new Promise(done => setTimeout(done, 50))
      expect(r.captures).toBe(captures)
      expect(r.stored?.bytes[0]).toBe(2)
    } finally { await r.controller.close() }
  })
  it("a save asked during a hold is refused, not captured", async () => {
    const r = fixture()
    try {
      r.controller.hold()
      await expect(r.controller.save("native-save-1")).rejects.toThrow("native_save_held")
      await expect(r.controller.flush()).rejects.toThrow("native_save_held")
      expect(r.captures).toBe(0)
    } finally { await r.controller.close() }
  })
  it("a hold does not start once Edit is gone, so a retirement's capture still runs", async () => {
    const r = fixture()
    try {
      const retiring = r.controller.revokeWrite()
      r.controller.hold()
      expect(await retiring).toBe(true)
      expect(r.captures).toBe(1)
    } finally { await r.controller.close() }
  })
})
