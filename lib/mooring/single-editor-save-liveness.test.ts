// @vitest-environment node
import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { createSingleEditorSave, NativeSaveError, type NativeSaveRequest, type NativeSaveState } from "./single-editor-save"
import type { NativeDraft } from "./native-draft"

// #4852: a holder's dirty Sheet stopped saving for good after one save whose
// local recovery row could not be retired (journal.confirm rejected, e.g. the
// 2 s IndexedDB deadline firing late after a main-thread stall). No later
// autosave or Ctrl+S ever reached the transport while the lease was held.
beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => vi.unstubAllGlobals())
const receipt = (r: NativeSaveRequest) => ({ ...r, bytes: undefined, version: String(BigInt(r.baseVersion) + 1n), durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash })
function fixture() {
  let value = 1, captures = 0, stored: NativeDraft | undefined
  const requests: NativeSaveRequest[] = [], states: NativeSaveState[] = []
  const onReceipt = vi.fn()
  const persist = vi.fn(async (d: NativeDraft) => { stored = d })
  const confirm = vi.fn(async (d: NativeDraft) => { if (stored?.editorRevision === d.editorRevision) stored = undefined })
  const send = vi.fn(async (r: NativeSaveRequest): Promise<unknown> => receipt(r))
  const controller = createSingleEditorSave({ workspaceId: "w", pageId: "p", applicationId: "gridcraft", enginePin: "engine", baseVersion: "1", generation: "1", sessionId: "session", idleMs: 5,
    journal: { persist, confirm, async recovery() { return [] }, onRetire() { return () => {} }, async close() {}, async revokeRead() {}, async discardRecovered() {} },
    engine: { async snapshot(_signal, id) { captures++; return { bytes: new Uint8Array([value]), editorRevision: id ?? `rpc:${captures}` } }, async validate() {} },
    transport: { async save(r) { requests.push(r); return send(r) } }, onState(state) { states.push(state) }, onReceipt,
  })
  return { controller, requests, states, onReceipt, persist, confirm, send, get stored() { return stored }, edit(next: number) { value = next; controller.edited() } }
}
const last = (r: ReturnType<typeof fixture>) => r.states.at(-1)

it("keeps saving later edits after one committed save could not retire its local row", async () => {
  const r = fixture()
  r.confirm.mockRejectedValueOnce(Error("storage deadline"))
  try {
    r.edit(2)
    await vi.waitFor(() => expect(r.requests).toHaveLength(1))
    await vi.waitFor(() => expect(r.states).toContainEqual({ phase: "draft", dirty: true, reason: "storage" }))
    // The next edit is a new capture: it must reach the transport again.
    r.edit(3)
    await vi.waitFor(() => expect(r.requests).toHaveLength(2))
    expect(r.requests[1].bytes).toEqual(new Uint8Array([3]))
    expect(r.requests[1].baseVersion).toBe("2")
    await vi.waitFor(() => expect(last(r)).toEqual({ phase: "saved", dirty: false }))
    expect(r.stored).toBeUndefined()
  } finally { await r.controller.close() }
})

it("a native Ctrl+S ticket still transmits after a failed row retirement", async () => {
  const r = fixture()
  r.confirm.mockRejectedValueOnce(Error("storage deadline"))
  try {
    r.edit(2)
    await vi.waitFor(() => expect(r.states).toContainEqual({ phase: "draft", dirty: true, reason: "storage" }))
    r.edit(3)
    await r.controller.save("ctrl-s")
    expect(r.requests.at(-1)?.editorRevision).toBe("ctrl-s")
    expect(r.requests.at(-1)?.bytes).toEqual(new Uint8Array([3]))
  } finally { await r.controller.close() }
})

it("surfaces the failed retirement, then retires the row itself without a duplicate version", async () => {
  const r = fixture()
  r.confirm.mockRejectedValueOnce(Error("storage deadline"))
  try {
    r.edit(2)
    await vi.waitFor(() => expect(r.states).toContainEqual({ phase: "draft", dirty: true, reason: "storage" }))
    // No further edit: the bounded retry captures the same bytes, finds them
    // committed, retires the row and reports Saved. It never sends them again.
    await vi.waitFor(() => expect(last(r)).toEqual({ phase: "saved", dirty: false }))
    expect(r.requests).toHaveLength(1)
    expect(r.confirm).toHaveBeenCalledTimes(2)
    expect(r.stored).toBeUndefined()
    expect(r.controller.confirmedVersion()).toBe("2")
  } finally { await r.controller.close() }
})

it("keeps the draft and stops after storage keeps failing, without a retry loop or a send", async () => {
  const r = fixture()
  // Storage stays broken from the failed retirement on.
  r.confirm.mockImplementationOnce(async () => { r.persist.mockRejectedValue(Error("storage gone")); throw Error("storage deadline") })
  try {
    r.edit(2)
    await vi.waitFor(() => expect(r.persist).toHaveBeenCalledTimes(2))
    await new Promise(resolve => setTimeout(resolve, 50))
    expect(r.persist).toHaveBeenCalledTimes(2)
    expect(r.requests).toHaveLength(1)
    expect(last(r)).toEqual({ phase: "draft", dirty: true, reason: "storage" })
    expect(r.stored).toBeDefined()
  } finally { await r.controller.close() }
})

it.each(["conflict", "quota"] as const)("still never retries after a server %s refusal", async reason => {
  const r = fixture()
  r.send.mockRejectedValueOnce(new NativeSaveError(reason))
  try {
    r.edit(2)
    await vi.waitFor(() => expect(last(r)).toMatchObject({ phase: "draft", reason }))
    r.edit(3)
    await new Promise(resolve => setTimeout(resolve, 50))
    expect(r.requests).toHaveLength(1)
    expect(last(r)).toMatchObject({ phase: "draft", reason })
  } finally { await r.controller.close() }
})
