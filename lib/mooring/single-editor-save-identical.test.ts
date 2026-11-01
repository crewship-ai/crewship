// @vitest-environment node
import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { createSingleEditorSave, type NativeSaveRequest, type NativeSaveState } from "./single-editor-save"

beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => vi.unstubAllGlobals())
const receipt = (r: NativeSaveRequest) => ({ ...r, version: String(BigInt(r.baseVersion) + 1n), durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash })

function fixture(applicationId: "wordcraft" | "gridcraft", lose = false) {
  let value = 1, captures = 0, lost = lose
  const requests: NativeSaveRequest[] = [], states: NativeSaveState[] = []
  const controller = createSingleEditorSave({ workspaceId: "w", pageId: "p", applicationId, enginePin: "engine", baseVersion: "1", generation: "1", sessionId: "session", idleMs: 5,
    journal: { async persist() {}, async confirm() {}, async recovery() { return [] }, onRetire() { return () => {} }, async close() {}, async revokeRead() {} },
    engine: { async snapshot(_s, id) { captures++; return { bytes: new Uint8Array([value]), editorRevision: id ?? `rpc:${captures}`, ...(applicationId === "wordcraft" ? { nativeState: "state:1" } : {}) } }, async validate() {} },
    transport: {
      async save(r) { requests.push(r); if (lost) { lost = false; throw new Error("net") } return receipt(r) },
      async reconcile(r) { return receipt(r) },
    },
    onState(s) { states.push(s) }, timeoutMs: 1000,
  })
  return { controller, requests, states, edit(v: number) { value = v; controller.edited() } }
}
const clean = (r: ReturnType<typeof fixture>) => vi.waitFor(() => expect(r.states.at(-1)).toEqual({ phase: "saved", dirty: false }))

for (const app of ["wordcraft", "gridcraft"] as const) {
  it(`${app}: a host release flush after a confirmed save creates no version from identical bytes`, async () => {
    const r = fixture(app)
    try {
      r.edit(2); await clean(r)
      expect(r.requests).toHaveLength(1)
      await r.controller.flush()
      expect(r.requests).toHaveLength(1)
      r.edit(3); await clean(r)
      expect(r.requests).toHaveLength(2)
    } finally { await r.controller.close() }
  })

  it(`${app}: after a lost ack is reconciled, identical bytes are not saved again`, async () => {
    const r = fixture(app, true)
    try {
      r.edit(2)
      await vi.waitFor(() => expect(r.states.at(-1)?.phase).toBe("uncertain"))
      await r.controller.reconcile()
      await clean(r)
      await new Promise(done => setTimeout(done, 50))
      await r.controller.flush()
      expect(r.requests).toHaveLength(1)
    } finally { await r.controller.close() }
  })
}
