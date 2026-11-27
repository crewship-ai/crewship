// @vitest-environment node
import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { createSingleEditorSave, type NativeSaveRequest, type NativeSaveState } from "./single-editor-save"
import type { NativeDraft } from "./native-draft"

beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => vi.unstubAllGlobals())

const receipt = (r: NativeSaveRequest) => ({ ...r, version: String(BigInt(r.baseVersion) + 1n), durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash })
// An engine export: indented JSON of about `pretty` bytes whose compact form is a third of it.
const pretty = (bytes: number, tag = 1, piece = "    1,\n") => {
  const head = `{\n  "d": [\n    ${tag},\n`, tail = "    0\n  ]\n}"
  const n = Math.max(0, Math.floor((bytes - head.length - tail.length) / piece.length))
  return new TextEncoder().encode(head + piece.repeat(n) + tail)
}
function fixture(exportBytes: () => Uint8Array) {
  let captures = 0, inFlight = 0, maxInFlight = 0
  const requests: NativeSaveRequest[] = [], states: NativeSaveState[] = []
  const controller = createSingleEditorSave({ workspaceId: "w", pageId: "p", applicationId: "gridcraft", enginePin: "engine", baseVersion: "1", generation: "1", sessionId: "session", idleMs: 5,
    journal: { async persist() {}, async confirm(_d: NativeDraft) {}, async recovery() { return [] }, onRetire() { return () => {} }, async close() {}, async revokeRead() {}, async discardRecovered() {} },
    engine: { async snapshot(_s, id) { inFlight++; maxInFlight = Math.max(maxInFlight, inFlight); await new Promise(r => setTimeout(r, 20)); captures++; inFlight--; return { bytes: exportBytes(), editorRevision: id ?? `rpc:${captures}`, nativeState: undefined, revision: undefined } }, async validate() {} },
    transport: { async save(r) { requests.push(r); return receipt(r) } }, onState(s) { states.push(s) }, onReceipt: vi.fn(), timeoutMs: 20_000,
  })
  return { controller, requests, states, get captures() { return captures }, get maxInFlight() { return maxInFlight } }
}

it("saves the compact form of an indented export, hashed once on those bytes", async () => {
  const exported = pretty(9 << 20)
  const r = fixture(() => exported)
  try {
    await r.controller.save("native-save-1")
    expect(r.requests).toHaveLength(1)
    const sent = r.requests[0]
    expect(sent.bytes.byteLength).toBeLessThan(exported.byteLength / 2)
    expect(new TextDecoder().decode(sent.bytes)).not.toMatch(/[\n]/)
    expect(JSON.parse(new TextDecoder().decode(sent.bytes))).toEqual(JSON.parse(new TextDecoder().decode(exported)))
    const digest = Buffer.from(await webcrypto.subtle.digest("SHA-256", sent.bytes)).toString("hex")
    expect(sent.contentHash).toBe(`sha256:${digest}`)
  } finally { await r.controller.close() }
}, 30_000)

it("a document that is still over 32 MiB compact is not sent, says why and keeps the work in the editor", async () => {
  // "1,\n" is 3 bytes indented and 2 compact: 52 MiB indented is about 34.6 MiB compact, over the limit
  const exported = pretty(52 << 20, 1, "1,\n")
  const r = fixture(() => exported)
  try {
    await r.controller.save("native-save-1").catch(() => {})
    // nothing is transmitted (the draft limit is the same 32 MiB, so no draft row either); the editor keeps the document
    expect(r.requests).toHaveLength(0)
    expect(r.states.some(s => s.phase === "draft" && s.reason === "document_too_large")).toBe(true)
    expect(r.states.at(-1)?.phase).not.toBe("saved")
  } finally { await r.controller.close() }
}, 60_000)

it("two saves at once never run two exports together", async () => {
  const r = fixture(() => pretty(2 << 20))
  try {
    await Promise.all([r.controller.save("native-save-a"), r.controller.save("native-save-b")])
    expect(r.maxInFlight).toBe(1)
  } finally { await r.controller.close() }
}, 30_000)

// The idle waits of large and small documents: single-editor-save-large-idle.test.ts (fake time only).
