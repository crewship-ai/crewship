// @vitest-environment node
// Concurrent editing: the agent's log is replayed over the person's unsaved work, and the
// result is saved on top of the agent's version. Nothing captured before the replay may be saved, or kept
// for recovery, as if it were on top of the agent's version: that would silently drop the agent's change.
import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { NativeDraft } from "./native-draft"
import { createSingleEditorSave, NativeSaveError, type NativeSaveRequest } from "./single-editor-save"

beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => vi.unstubAllGlobals())

const receipt = (r: NativeSaveRequest) => ({ ...r, version: String(BigInt(r.baseVersion) + 1n), durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash })
const sha = async (b: Uint8Array) => `sha256:${Buffer.from(await webcrypto.subtle.digest("SHA-256", new Uint8Array(b))).toString("hex")}`
const enc = (s: string) => new TextEncoder().encode(s)
const dec = (b: Uint8Array) => new TextDecoder().decode(b)

function fixture() {
  let doc = enc('{"v":"base"}'), captures = 0, touched: string[] | "unknown" | undefined
  let gate: Promise<void> | undefined
  const requests: NativeSaveRequest[] = [], rows: { base: string; text: string }[] = [], states: { phase: string; reason?: string }[] = []
  let refusing: () => boolean = () => false, refuseAs: "moved" | "conflict" = "moved"
  const controller = createSingleEditorSave({ workspaceId: "w", pageId: "p", applicationId: "gridcraft", enginePin: "engine", baseVersion: "1", generation: "1", sessionId: "s", idleMs: 5,
    journal: { async persist(d: NativeDraft) { rows.push({ base: d.baseVersion, text: dec(d.bytes) }) }, async confirm() {}, async recovery() { return [] }, onRetire() { return () => {} }, async close() {}, async revokeRead() {}, async discardRecovered() {} },
    engine: { async snapshot(_s, id) { captures++; const bytes = doc; if (gate) await gate; return { bytes, editorRevision: id ?? `rpc:${captures}`, nativeState: undefined, revision: undefined, ...(touched !== undefined ? { touched } : {}) } }, async validate() {} },
    transport: { async save(r) { requests.push(r); if (refusing()) throw new NativeSaveError(refuseAs); return receipt(r) } }, onState(s) { states.push(s as { phase: string; reason?: string }) }, onReceipt: vi.fn(), timeoutMs: 5000,
  })
  return { controller, requests, rows, states, set touched(v: string[] | "unknown" | undefined) { touched = v }, refuseNext(f: () => boolean, as: "moved" | "conflict" = "moved") { refusing = f; refuseAs = as }, set doc(v: string) { doc = enc(v) }, hold() { let open!: () => void; gate = new Promise(r => { open = r }); return () => { gate = undefined; open() } }, get captures() { return captures } }
}
const sent = (r: ReturnType<typeof fixture>) => r.requests.map(q => ({ base: q.baseVersion, text: dec(q.bytes) }))

it("saves the person's work with the agent's change on top of the agent's version, and nothing captured before it", async () => {
  const r = fixture()
  try {
    r.doc = '{"v":"person"}'
    const release = r.hold()
    r.controller.edited()
    await vi.waitFor(() => expect(r.captures).toBe(1), { timeout: 3000 })
    // The replay ran in the editor while that capture was being taken; the editor now holds both.
    r.doc = '{"v":"person+agent"}'
    expect(r.controller.rebaseBase("5", await sha(enc('{"v":"agent"}')))).toBe(true)
    release()
    await vi.waitFor(() => expect(sent(r)).toContainEqual({ base: "5", text: '{"v":"person+agent"}' }), { timeout: 3000 })
    expect(sent(r), "the earlier capture is never sent on top of the agent's version").not.toContainEqual({ base: "5", text: '{"v":"person"}' })
    expect(r.rows, "and never kept for recovery as if it were").not.toContainEqual({ base: "5", text: '{"v":"person"}' })
  } finally { await r.controller.close() }
})

it("keeps unsaved work unsaved: after the move there is still something to save", async () => {
  const r = fixture()
  try {
    r.doc = '{"v":"person+agent"}'
    expect(r.controller.rebaseBase("5", await sha(enc('{"v":"agent"}')))).toBe(true)
    await vi.waitFor(() => expect(sent(r)).toEqual([{ base: "5", text: '{"v":"person+agent"}' }]), { timeout: 3000 })
  } finally { await r.controller.close() }
})

it("refuses to move to a version that is not newer, or with a malformed hash", async () => {
  const r = fixture()
  try {
    const hash = await sha(enc("x"))
    expect(r.controller.rebaseBase("1", hash)).toBe(false)
    expect(r.controller.rebaseBase("0", hash)).toBe(false)
    expect(r.controller.rebaseBase("5", "sha256:nothex")).toBe(false)
  } finally { await r.controller.close() }
})

it("after the server refused a save because the Sheet moved on, takes the newer version as its base and saves on top", async () => {
  const r = fixture()
  let refuse = true
  try {
    // The first save meets a version saved meanwhile (409 moved).
    r.doc = '{"v":"person"}'
    r.refuseNext(() => refuse)
    r.controller.edited()
    await vi.waitFor(() => expect(r.states.some(s => s.phase === "draft" && s.reason === "moved")).toBe(true), { timeout: 3000 })
    refuse = false
    r.doc = '{"v":"person+agent"}'
    expect(r.controller.rebaseBase("5", await sha(enc('{"v":"agent"}'))), "a save refused as moved is what a rebase answers").toBe(true)
    await vi.waitFor(() => expect(sent(r).at(-1)).toEqual({ base: "5", text: '{"v":"person+agent"}' }), { timeout: 3000 })
  } finally { await r.controller.close() }
})

it("answers only a save refused as moved, never another conflict", async () => {
  const r = fixture()
  try {
    r.refuseNext(() => true, "conflict")
    r.controller.edited()
    await vi.waitFor(() => expect(r.states.some(s => s.phase === "draft" && s.reason === "conflict")).toBe(true), { timeout: 3000 })
    expect(r.controller.rebaseBase("5", await sha(enc("x")))).toBe(false)
  } finally { await r.controller.close() }
})

it("marks the save of Keep mine, and only that one", async () => {
  const r = fixture()
  try {
    r.doc = '{"v":"mine"}'
    expect(r.controller.rebaseBase("5", await sha(enc('{"v":"agent"}')), { keepLocal: true })).toBe(true)
    await vi.waitFor(() => expect(r.requests).toHaveLength(1), { timeout: 3000 })
    expect(r.requests[0]).toMatchObject({ baseVersion: "5", rebase: "keep-local" })
    r.doc = '{"v":"mine, later"}'
    r.controller.edited()
    await vi.waitFor(() => expect(r.requests).toHaveLength(2), { timeout: 3000 })
    expect(r.requests[1].rebase).toBeUndefined()
  } finally { await r.controller.close() }
})

it("saves again after a refusal as moved when it is lifted, and lifts nothing else", async () => {
  const r = fixture()
  let refuse = true
  try {
    r.refuseNext(() => refuse)
    r.doc = '{"v":"person"}'
    r.controller.edited()
    await vi.waitFor(() => expect(r.states.some(s => s.phase === "draft" && s.reason === "moved")).toBe(true), { timeout: 3000 })
    const before = r.requests.length
    refuse = false
    expect(r.controller.retryMoved()).toBe(true)
    await vi.waitFor(() => expect(r.requests.length).toBeGreaterThan(before), { timeout: 3000 })
    expect(r.requests.at(-1)!.baseVersion, "the same base: the server decides again").toBe("1")
    expect(r.controller.retryMoved(), "nothing to lift now").toBe(false)
  } finally { await r.controller.close() }
})

// The cells the person changed go with the save they were captured for; Keep mine, and a save
// whose base moved after the capture, say unknown (everything touched), never nothing.
it("sends the cells the person changed with their save, and unknown for Keep mine", async () => {
  const r = fixture()
  try {
    r.touched = ["'L'!B2"]
    r.doc = '{"v":"person"}'
    r.controller.edited()
    await vi.waitFor(() => expect(r.requests).toHaveLength(1), { timeout: 3000 })
    expect(r.requests[0].touched).toEqual(["'L'!B2"])
    r.doc = '{"v":"mine"}'
    expect(r.controller.rebaseBase("5", await sha(enc('{"v":"agent"}')), { keepLocal: true })).toBe(true)
    await vi.waitFor(() => expect(r.requests).toHaveLength(2), { timeout: 3000 })
    expect(r.requests[1]).toMatchObject({ rebase: "keep-local", touched: "unknown" })
  } finally { await r.controller.close() }
})

it("keeps the cells for a save tried again on the same base", async () => {
  const r = fixture()
  let refuse = true
  try {
    r.touched = ["'L'!B2"]
    r.refuseNext(() => refuse)
    r.doc = '{"v":"person"}'
    r.controller.edited()
    await vi.waitFor(() => expect(r.states.some(s => s.phase === "draft" && s.reason === "moved")).toBe(true), { timeout: 3000 })
    refuse = false
    expect(r.controller.retryMoved()).toBe(true)
    await vi.waitFor(() => expect(r.requests.length).toBeGreaterThan(1), { timeout: 3000 })
    expect(r.requests.at(-1)!.touched, "the same base: the same cells").toEqual(["'L'!B2"])
  } finally { await r.controller.close() }
})
