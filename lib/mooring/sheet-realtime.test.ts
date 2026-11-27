// @vitest-environment node
import { createHash, webcrypto } from "node:crypto"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import contract from "../../testdata/craft/sheet-realtime-contract.json"
import { createNativeDocumentClient } from "./native-document-client"
import { readMergedSheetSave, readSheetPresence, readSheetRealtimeAuthority, type SheetRange } from "./sheet-realtime"
import type { NativeDraft, NativeRecovery } from "./native-draft"
import { createSingleEditorSave, type NativeSaveRequest } from "./single-editor-save"

beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => vi.unstubAllGlobals())

const submitted: NativeSaveRequest = {
  workspaceId: "w", pageId: "p", applicationId: "gridcraft", enginePin: "a".repeat(64), baseVersion: "1",
  generation: "2", sessionId: "session", editorRevision: "doc:4", attemptId: "attempt-1",
  bytes: Uint8Array.of(80, 75, 3), contentHash: `sha256:${"a".repeat(64)}`, format: "xlsx",
}
function merged(request: NativeSaveRequest) {
  const bytes = Buffer.from([80, 75, 4])
  const hash = "sha256:" + createHash("sha256").update(bytes).digest("hex")
  const { bytes: _bytes, attemptId, ...binding } = request
  return {
    kind: "merged", submitted: { attempt_id: attemptId, base_version: request.baseVersion, editor_revision: request.editorRevision, content_hash: request.contentHash },
    receipt: { ...binding, baseVersion: "2", version: "3", issuedBy: "crewship", durability: "node", phase: "committed", contentHash: hash, candidateHash: hash },
    head_content_hash: hash, bytes: bytes.toString("base64"),
    overlap: [], agent_ranges: [{ sheet: "Other", a1: "B2" }],
  }
}

it("binds the merged reply to the original attempt and verifies the canonical bytes and footprint", async () => {
  const value = merged(submitted)
  expect(await readMergedSheetSave(value, submitted)).toMatchObject({ bytes: Uint8Array.of(80, 75, 4), agentRanges: [{ sheet: "Other", a1: "B2" }] })
  expect(await readMergedSheetSave(value, { ...submitted, attemptId: "another-attempt" })).toBeUndefined()
  expect(await readMergedSheetSave({ ...value, bytes: "AAAA" }, submitted)).toBeUndefined()
  expect(await readMergedSheetSave({ ...value, agent_ranges: [{ sheet: "Other", a1: "https://example.test" }] }, submitted)).toBeUndefined()
  expect(await readMergedSheetSave({ ...value, receipt: { ...value.receipt, sessionId: "other" } }, submitted)).toBeUndefined()
})

it("adopts a merged reconcile using the original capture and never exact-ACKs it", async () => {
  const save = vi.fn(async () => { throw Error("response lost") })
  const reconcile = vi.fn(async (request: NativeSaveRequest) => merged(request))
  const adoptMerged = vi.fn(async () => ({ nativeState: "doc:saved-5", dirty: true }))
  const onReceipt = vi.fn()
  const confirm = vi.fn(async () => {})
  const controller = createSingleEditorSave({
    workspaceId: "w", pageId: "p", applicationId: "gridcraft", enginePin: "a".repeat(64), baseVersion: "1", generation: "2", sessionId: "session", format: "xlsx", idleMs: 30_000,
    journal: { async persist() {}, confirm, async recovery() { return [] }, onRetire() { return () => {} }, async close() {}, async revokeRead() {}, async discardRecovered() {} },
    engine: { async snapshot() { return { bytes: submitted.bytes, editorRevision: "doc:4", nativeState: "doc:4", captureId: "capture-1" } } },
    transport: { save, reconcile }, adoptMerged, onReceipt, onState() {},
  })
  try {
    controller.edited()
    await controller.save("manual").catch(() => {})
    await controller.reconcile()
    expect(adoptMerged).toHaveBeenCalledWith(expect.objectContaining({ receipt: expect.objectContaining({ version: "3" }) }), "capture-1")
    expect(onReceipt).not.toHaveBeenCalled()
    expect(confirm).toHaveBeenCalledWith(expect.objectContaining({ editorRevision: "doc:4" }))
    expect(controller.currentBase()).toBe("3")
    expect(controller.confirmedVersion()).toBeUndefined()
  } finally { await controller.close() }
})

it("keeps presence metadata bounded and excludes cell data", () => {
  const presence = { op_id: "op-1", run_id: "run-1", agent: { id: "agent-1", name: "Morgan" }, phase: "started", ranges: [{ sheet: "Data", a1: "A2:A10", value: "hidden" }], ttl_ms: 1500 }
  expect(readSheetPresence(presence)).toEqual({ phase: "started", presence: { actorId: "agent-1", label: "Morgan", activityId: "op-1", ttlMs: 1500, ranges: [{ sheet: "Data", a1: "A2:A10" }] } })
  expect(readSheetPresence({ ...presence, ttl_ms: 999_999 })).toBeUndefined()
})


it("opens the server contract's chat admission and proves the matching lease", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(Response.json(contract.session))
    .mockResolvedValueOnce(Response.json(contract.lease))
  const client = createNativeDocumentClient({ workspaceId: "workspace-1", pageId: "sheet-1", chatId: "chat-1", fetch })
  const signal = new AbortController().signal
  try {
    const opened = await client.open(signal)
    expect(opened).toMatchObject({ realtime: { canWrite: true, generation: "1" } })
    expect(fetch.mock.calls[0]?.[1]?.body).toBe('{"chat_id":"chat-1"}')
    const lease = await client.lease(signal)
    expect(readSheetRealtimeAuthority(lease, opened.realtime?.generation)).toEqual({ canEdit: true, generation: "1" })
    expect(readSheetRealtimeAuthority(lease, "2")).toEqual({ canEdit: true })
  } finally {
    client.close()
  }
})

it.each([contract.merged, contract.merged_unchanged])("accepts the canonical merge contract for $submitted.attempt_id", async value => {
  const { receipt, submitted: attempt } = value
  const request: NativeSaveRequest = {
    ...submitted, workspaceId: receipt.workspaceId, pageId: receipt.pageId, enginePin: receipt.enginePin,
    generation: receipt.generation, sessionId: receipt.sessionId, attemptId: attempt.attempt_id,
    baseVersion: attempt.base_version, editorRevision: attempt.editor_revision, contentHash: attempt.content_hash,
  }
  const parsed = await readMergedSheetSave(value, request)
  expect(parsed?.receipt).toEqual(receipt)
  expect(parsed?.bytes).toEqual(new Uint8Array(Buffer.from(value.bytes, "base64")))
  expect(parsed?.agentRanges).toEqual(value.agent_ranges)
  expect(await readMergedSheetSave({ ...value, receipt: { ...receipt, version: request.baseVersion } }, request)).toBeUndefined()
})

it("uses the shared presence snapshot, start and end messages", () => {
  expect(contract.presence_snapshot.operations.map(readSheetPresence)).toEqual([readSheetPresence(contract.presence_started)])
  expect(readSheetPresence(contract.presence_started)).toMatchObject({
    phase: "started", presence: { activityId: "operation-1", label: "Agent One", ranges: [{ sheet: "L", a1: "B3:B5" }] },
  })
  expect(readSheetPresence(contract.presence_ended)).toMatchObject({ phase: "ended", presence: { activityId: "operation-1", ttlMs: 0, ranges: [] } })
})

const overlap = [{ sheet: "Other", a1: "B2:B258" }]

function saveRig(transport: Parameters<typeof createSingleEditorSave>[0]["transport"]) {
  let revision = 4
  let bytes = submitted.bytes
  const onReceipt = vi.fn()
  const onConflict = vi.fn()
  const adoptMerged = vi.fn(async () => ({ nativeState: "doc:5", dirty: false }))
  const persist = vi.fn(async () => {})
  const confirm = vi.fn(async () => {})
  const archiveConflict = vi.fn(async (draft: NativeDraft, ranges: SheetRange[]): Promise<NativeRecovery> =>
    ({ draft, overlap: ranges, actions: ["compare", "copy"] }))
  const controller = createSingleEditorSave({
    ...submitted, idleMs: 30_000,
    journal: { persist, confirm, archiveConflict, async recovery() { return [] }, onRetire() { return () => {} }, async close() {}, async revokeRead() {}, async discardRecovered() {} },
    engine: { async snapshot() { return { bytes, editorRevision: `doc:${revision}`, nativeState: `doc:${revision}`, captureId: `capture-${revision}` } } },
    transport, adoptMerged, onReceipt, onConflict, onState() {},
  })
  return {
    controller, adoptMerged, onReceipt, onConflict, persist, confirm, archiveConflict,
    edit() { revision++; bytes = Uint8Array.of(80, 75, 3, 4, revision); controller.edited(`doc:${revision}`) },
  }
}

function exactReceipt(request: NativeSaveRequest, unchanged = false) {
  const { bytes: _bytes, attemptId: _attempt, ...binding } = request
  return { ...binding, version: unchanged ? request.baseVersion : String(BigInt(request.baseVersion) + 1n), ...(unchanged ? { unchanged: true } : {}),
    durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: request.contentHash }
}

it("reconciles the original plain realtime no-op after a lost reply, then publishes a different edit", async () => {
  const save = vi.fn(async (request: NativeSaveRequest) => exactReceipt(request))
    .mockRejectedValueOnce(Error("response lost"))
  const reconcile = vi.fn(async (request: NativeSaveRequest) => exactReceipt(request, true))
    .mockImplementationOnce(async (request: NativeSaveRequest) => ({ ...exactReceipt(request, true), editorRevision: "different" }))
  const r = saveRig({ save, reconcile })
  try {
    await expect(r.controller.save("manual")).rejects.toThrow("unconfirmed")
    await r.controller.reconcile()
    expect(r.onReceipt).not.toHaveBeenCalled()
    await r.controller.reconcile()
    expect(reconcile.mock.calls.map(([request]) => request.attemptId)).toEqual([save.mock.calls[0][0].attemptId, save.mock.calls[0][0].attemptId])
    expect(r.onReceipt).toHaveBeenCalledWith(expect.objectContaining({ unchanged: true, version: "1" }), "capture-4")
    r.edit()
    await r.controller.save("next")
    expect(save).toHaveBeenCalledTimes(2)
    expect(save.mock.calls[1][0]).toMatchObject({ baseVersion: "1", editorRevision: "doc:5" })
    expect(r.controller.currentBase()).toBe("2")
  } finally { await r.controller.close() }
})

it.each(["save", "proactive"] as const)("keeps the captured overlap before adopting in the %s path", async mode => {
  const save = vi.fn(async (request: NativeSaveRequest) => ({ ...merged(request), overlap }))
  const r = saveRig({ save })
  try {
    if (mode === "save") await r.controller.save("manual")
    else expect(await r.controller.catchUp("3")).toBe(true)
    expect(r.archiveConflict).toHaveBeenCalledWith(expect.objectContaining({ editorRevision: "doc:4", bytes: submitted.bytes }), overlap)
    expect(r.archiveConflict.mock.invocationCallOrder[0]).toBeLessThan(r.adoptMerged.mock.invocationCallOrder[0])
    expect(r.onConflict).toHaveBeenCalledWith(expect.objectContaining({ overlap }))
    expect(r.confirm).toHaveBeenCalledOnce()
    expect(r.onReceipt).not.toHaveBeenCalled()
  } finally { await r.controller.close() }
})

it("keeps the original editor and draft when conflict storage refuses", async () => {
  const r = saveRig({ save: async request => ({ ...merged(request), overlap }) })
  r.archiveConflict.mockRejectedValueOnce(Error("storage full"))
  try {
    expect(await r.controller.catchUp("3")).toBe(false)
    expect(r.persist).toHaveBeenCalledOnce()
    expect(r.adoptMerged).not.toHaveBeenCalled()
    expect(r.confirm).not.toHaveBeenCalled()
    expect(r.onConflict).not.toHaveBeenCalled()
    expect(r.controller.currentBase()).toBe("1")
  } finally { await r.controller.close() }
})

it("accepts complete bounded server footprints and refuses an oversized footprint", async () => {
  const value = merged(submitted)
  const ranges = Array.from({ length: 256 }, (_, i) => ({ sheet: "Other", a1: `A${i + 1}` }))
  expect((await readMergedSheetSave({ ...value, agent_ranges: ranges, overlap: ranges }, submitted))?.overlap).toHaveLength(256)
  expect(await readMergedSheetSave({ ...value, agent_ranges: [...ranges, overlap[0]] }, submitted)).toBeUndefined()
  const event = { ...contract.presence_started, ranges: ranges.slice(0, 32) }
  expect(readSheetPresence(event)?.presence.ranges).toHaveLength(32)
  expect(readSheetPresence({ ...event, truncated: true })?.truncated).toBe(true)
  expect(readSheetPresence({ ...event, ranges: ranges.slice(0, 33) })).toBeUndefined()
})

it("keeps the draft after too_many_ranges and never retries that base automatically", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(Response.json({ ...contract.session, session_id: "session", page_id: "p", generation: "2" }))
    .mockResolvedValueOnce(Response.json({ reason: "too_many_ranges" }, { status: 422 }))
  const client = createNativeDocumentClient({ workspaceId: "w", pageId: "p", chatId: "chat-1", fetch })
  await client.open(new AbortController().signal)
  const r = saveRig(client)
  try {
    await expect(r.controller.save("manual")).rejects.toThrow("too_many_ranges")
    expect(await r.controller.catchUp("2")).toBe(false)
    await expect(r.controller.save("again")).rejects.toThrow("too_many_ranges")
    expect(fetch.mock.calls.filter(([url]) => String(url).includes("/document/save"))).toHaveLength(1)
    expect(r.persist).toHaveBeenCalled()
    expect(r.confirm).not.toHaveBeenCalled()
    expect(r.controller.currentBase()).toBe("1")
  } finally { await r.controller.close(); client.close() }
})
