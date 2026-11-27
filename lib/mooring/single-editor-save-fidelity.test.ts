// @vitest-environment node
// A save that would lose or change macros, signatures or customXml is refused with 422 fidelity_loss and
// nothing is stored. The person's work is never lost: it stays in the editor and in the local draft, and their own save
// asks the server again.
import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { NativeDraft } from "./native-draft"
import { createSingleEditorSave, NativeSaveError, type NativeSaveRequest } from "./single-editor-save"

beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => vi.unstubAllGlobals())

const receipt = (r: NativeSaveRequest) => ({ ...r, version: String(BigInt(r.baseVersion) + 1n), durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash })
const enc = (s: string) => new TextEncoder().encode(s), dec = (b: Uint8Array) => new TextDecoder().decode(b)

function fixture() {
  let doc = enc('{"v":"base"}'), captures = 0, refuse = true
  const requests: NativeSaveRequest[] = [], rows: string[] = [], states: { phase: string; reason?: string }[] = [], discarded: unknown[] = []
  const controller = createSingleEditorSave({ workspaceId: "w", pageId: "p", applicationId: "gridcraft", enginePin: "engine", baseVersion: "1", generation: "1", sessionId: "s", idleMs: 5,
    journal: { async persist(d: NativeDraft) { rows.push(dec(d.bytes)) }, async confirm(...a: unknown[]) { discarded.push(a) }, async recovery() { return [] }, onRetire() { return () => {} }, async close() {}, async revokeRead() {}, async discardRecovered() {} },
    engine: { async snapshot(_s, id) { captures++; return { bytes: doc, editorRevision: id ?? `rpc:${captures}`, nativeState: undefined, revision: undefined } }, async validate() {} },
    transport: { async save(r) { requests.push(r); if (refuse) throw new NativeSaveError("fidelity_loss"); return receipt(r) } }, onState(s) { states.push(s as { phase: string; reason?: string }) }, onReceipt: vi.fn(), timeoutMs: 5000,
  })
  return { controller, requests, rows, states, discarded, set doc(v: string) { doc = enc(v) }, allow() { refuse = false } }
}

it("keeps the person's work in the local draft when the server refuses a save that would drop macros or signatures", async () => {
  const r = fixture()
  try {
    r.doc = '{"v":"person"}'
    r.controller.edited()
    await vi.waitFor(() => expect(r.states).toContainEqual(expect.objectContaining({ phase: "draft", reason: "fidelity_loss" })), { timeout: 3000 })
    expect(r.rows, "the work is in the local draft").toContain('{"v":"person"}')
    expect(r.discarded, "and the draft is not retired as saved").toEqual([])
    expect(r.requests, "sent once, not again and again").toHaveLength(1)
  } finally { await r.controller.close() }
})

// As for a full quota: autosave does not send the refused file again and again; the person's own save (Ctrl+S) asks again.
it("asks the server again when the person saves, and saves when the file keeps those parts", async () => {
  const r = fixture()
  try {
    r.doc = '{"v":"person"}'
    r.controller.edited()
    await vi.waitFor(() => expect(r.states.at(-1)).toMatchObject({ phase: "draft", reason: "fidelity_loss" }), { timeout: 3000 })
    r.allow(); r.doc = '{"v":"person, fixed"}'
    r.controller.edited()
    await new Promise(resolve => setTimeout(resolve, 50))
    expect(r.requests, "autosave does not send it again").toHaveLength(1)
    await r.controller.save()
    await vi.waitFor(() => expect(r.states.at(-1)?.phase).toBe("saved"), { timeout: 3000 })
    expect(dec(r.requests.at(-1)!.bytes)).toBe('{"v":"person, fixed"}')
  } finally { await r.controller.close() }
})
