// @vitest-environment node
import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { createSingleEditorSave, type NativeSaveRequest, type NativeSaveState } from "./single-editor-save"

beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => vi.unstubAllGlobals())
const receipt = (r: NativeSaveRequest) => ({ ...r, version: String(BigInt(r.baseVersion) + 1n), durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash })

// A freshly opened editor: the server confirmed v3 with bytes [3]. `engineBytes`
// is what the engine exports (equal unless it normalizes or a draft was restored).
function opened(applicationId: "wordcraft" | "gridcraft", engineBytes = 3, loadedBytes = 3) {
  let value = engineBytes, captures = 0
  const requests: NativeSaveRequest[] = [], states: NativeSaveState[] = []
  const controller = createSingleEditorSave({ workspaceId: "w", pageId: "p", applicationId, enginePin: "engine", baseVersion: "3", generation: "1", sessionId: "s", idleMs: 5,
    loaded: { bytes: new Uint8Array([loadedBytes]) },
    journal: { async persist() {}, async confirm() {}, async recovery() { return [] }, onRetire() { return () => {} }, async close() {}, async revokeRead() {} },
    engine: { async snapshot(_s, id) { captures++; return { bytes: new Uint8Array([value]), editorRevision: id ?? `rpc:${captures}`, ...(applicationId === "wordcraft" ? { nativeState: `state:${captures}` } : {}) } }, async validate() {} },
    transport: { async save(r) { requests.push(r); return receipt(r) }, async reconcile(r) { return receipt(r) } },
    onState(s) { states.push(s) }, timeoutMs: 1000,
  })
  return { controller, requests, states, edit(v: number) { value = v; controller.edited() } }
}

for (const app of ["wordcraft", "gridcraft"] as const) {
  it(`${app}: open v3, release without a change -> no save`, async () => {
    const r = opened(app)
    try { await r.controller.flush(); expect(r.requests).toHaveLength(0); expect(r.controller.confirmedVersion()).toBe("3") } finally { await r.controller.close() }
  })
  it(`${app}: open v3, change, release -> saves`, async () => {
    const r = opened(app)
    try {
      r.edit(4); expect(r.controller.confirmedVersion()).toBeUndefined()
      await r.controller.flush(); expect(r.requests).toHaveLength(1); expect(r.requests[0].baseVersion).toBe("3"); expect(r.controller.confirmedVersion()).toBe("4")
    } finally { await r.controller.close() }
  })
  it(`${app}: open v3 with a restored local draft, release -> the draft is saved`, async () => {
    const r = opened(app, 9, 3)
    try { await r.controller.flush(); expect(r.requests).toHaveLength(1) } finally { await r.controller.close() }
  })
  it(`${app}: engine normalizes on load -> skip does not apply, nothing lost`, async () => {
    const r = opened(app, 5, 3)
    try { await r.controller.flush(); expect(r.requests).toHaveLength(1) } finally { await r.controller.close() }
  })
  it(`${app}: a native Ctrl+S ticket still transmits unchanged bytes`, async () => {
    const r = opened(app)
    try { await r.controller.save("ticket-1"); expect(r.requests).toHaveLength(1) } finally { await r.controller.close() }
  })
}
