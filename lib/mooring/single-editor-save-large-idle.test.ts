// @vitest-environment node
// How long autosave waits for quiet, by document size, on fake time alone. Compacting and hashing are
// stubbed with instant, deterministic stand-ins, so no real work can race the clock: a loaded machine
// gives the same answer as an idle one. Compaction and hashing themselves: single-editor-save-large.test.ts.
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { NativeDraft } from "./native-draft"

vi.mock("./native-compact", () => ({ compactNativeJson: async (input: Uint8Array) => input }))
import { createSingleEditorSave, type NativeSaveRequest } from "./single-editor-save"

// A stand-in for SHA-256: instant, and different whenever the leading bytes or the length differ.
async function digest(_algorithm: AlgorithmIdentifier, data: BufferSource): Promise<ArrayBuffer> {
  const bytes = data instanceof Uint8Array ? data : new Uint8Array(data as ArrayBuffer)
  const out = new Uint8Array(32)
  for (let i = 0; i < Math.min(bytes.length, 64); i++) out[i % 32] = (out[i % 32] * 31 + bytes[i]) & 255
  out[30] = bytes.length & 255; out[31] = (bytes.length >>> 8) & 255
  return out.buffer
}
beforeEach(() => { vi.useFakeTimers(); vi.stubGlobal("crypto", { subtle: { digest }, getRandomValues: (a: Uint8Array) => a.fill(7), randomUUID: () => "00000000-0000-4000-8000-000000000000" }) })
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })

const receipt = (r: NativeSaveRequest) => ({ ...r, version: String(BigInt(r.baseVersion) + 1n), durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash })
// A JSON export of `bytes` bytes whose first line differs by `tag`, so every autosave really saves.
const exportOf = (bytes: number, tag: number) => {
  const head = `{"t":${tag},"d":"`, tail = `"}`
  const out = new Uint8Array(bytes).fill(0x61)
  out.set(new TextEncoder().encode(head)); out.set(new TextEncoder().encode(tail), bytes - tail.length)
  return out
}
function fixture(size: number) {
  let tag = 0
  const requests: NativeSaveRequest[] = []
  const controller = createSingleEditorSave({ workspaceId: "w", pageId: "p", applicationId: "gridcraft", enginePin: "engine", baseVersion: "1", generation: "1", sessionId: "session", idleMs: 5,
    journal: { async persist() {}, async confirm(_d: NativeDraft) {}, async recovery() { return [] }, onRetire() { return () => {} }, async close() {}, async revokeRead() {}, async discardRecovered() {} },
    engine: { async snapshot(_s, id) { tag++; return { bytes: exportOf(size, tag), editorRevision: id ?? `rpc:${tag}`, nativeState: undefined, revision: undefined } }, async validate() {} },
    transport: { async save(r) { requests.push(r); return receipt(r) } }, onState() {}, onReceipt: vi.fn(), timeoutMs: 20_000,
  })
  return { controller, requests }
}

it("a document over 8 MiB waits 3 s of quiet before autosaving", async () => {
  const r = fixture((8 << 20) + 1024)
  try {
    const first = r.controller.save("native-save-1"); await vi.advanceTimersByTimeAsync(0); await first
    const saved = r.requests.length
    expect(saved).toBe(1)
    r.controller.edited()
    await vi.advanceTimersByTimeAsync(2900)
    expect(r.requests.length, "still waiting for quiet").toBe(saved)
    await vi.advanceTimersByTimeAsync(200)
    expect(r.requests.length).toBe(saved + 1)
  } finally { await r.controller.close() }
})

it("a small document still autosaves after the short idle time", async () => {
  const r = fixture(4096)
  try {
    const first = r.controller.save("native-save-1"); await vi.advanceTimersByTimeAsync(0); await first
    const saved = r.requests.length
    r.controller.edited()
    await vi.advanceTimersByTimeAsync(400)
    expect(r.requests.length).toBe(saved + 1)
  } finally { await r.controller.close() }
})
