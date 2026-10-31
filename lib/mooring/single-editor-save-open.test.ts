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

// #4699: an engine bootstrapped from the loaded bytes [3] whose export is its
// normalized rendition [5]. Like the real port, nativeState names the engine's
// own revision, so it changes with the content and not per capture.
function mounted(applicationId: "wordcraft" | "gridcraft") {
  let value = 5
  let during: (() => void) | undefined
  const requests: NativeSaveRequest[] = [], confirmed: number[] = []
  const state = () => (applicationId === "wordcraft" ? { nativeState: `revision:${value}` } : {})
  const mountedState = applicationId === "wordcraft" ? "revision:5" : undefined
  const controller = createSingleEditorSave({ workspaceId: "w", pageId: "p", applicationId, enginePin: "engine", baseVersion: "3", generation: "1", sessionId: "s", idleMs: 5,
    loaded: { bytes: new Uint8Array([3]), ...(mountedState ? { mountedState } : {}) },
    journal: { async persist() {}, async confirm(d) { confirmed.push(d.bytes[0]) }, async recovery() { return [] }, onRetire() { return () => {} }, async close() {}, async revokeRead() {} },
    engine: { async snapshot(_s, id) { const hook = during; during = undefined; hook?.(); return { bytes: new Uint8Array([value]), editorRevision: id ?? `rpc:${value}`, ...state() } }, async validate() {} },
    transport: { async save(r) { requests.push(r); return receipt(r) }, async reconcile(r) { return receipt(r) } },
    onState() {}, timeoutMs: 1000,
  })
  return { controller, requests, confirmed, edit(v: number) { value = v; controller.edited() }, typeDuringSnapshot(v: number) { during = () => { value = v } } }
}

it("wordcraft: mounted from the loaded bytes, normalized export, no edit -> no save (#4699)", async () => {
  const r = mounted("wordcraft")
  try { await r.controller.flush(); expect(r.requests).toHaveLength(0); expect(r.controller.confirmedVersion()).toBe("3") } finally { await r.controller.close() }
})
it("wordcraft: mounted from the loaded bytes, then an edit -> saves over v3", async () => {
  const r = mounted("wordcraft")
  try {
    r.edit(6); await r.controller.flush()
    expect(r.requests.map(q => q.bytes[0])).toEqual([6]); expect(r.requests[0].baseVersion).toBe("3"); expect(r.controller.confirmedVersion()).toBe("4")
  } finally { await r.controller.close() }
})
it("wordcraft: text typed while the capture runs, its changed event late -> still saved", async () => {
  const r = mounted("wordcraft")
  try {
    r.typeDuringSnapshot(6)
    await r.controller.flush()
    expect(r.requests.map(q => q.bytes[0])).toEqual([6]); expect(r.controller.confirmedVersion()).toBe("4")
  } finally { await r.controller.close() }
})
it("wordcraft: mounted from the loaded bytes, a native Ctrl+S ticket still transmits", async () => {
  const r = mounted("wordcraft")
  try { await r.controller.save("ticket-1"); expect(r.requests).toHaveLength(1) } finally { await r.controller.close() }
})
it("wordcraft: an untouched final capture on revoke leaves no local draft to offer", async () => {
  const r = mounted("wordcraft")
  try { expect(await r.controller.revokeWrite()).toBe(true); expect(r.requests).toHaveLength(0); expect(r.confirmed).toEqual([5]) } finally { await r.controller.close() }
})
it("wordcraft: an edited final capture on revoke stays a local draft", async () => {
  const r = mounted("wordcraft")
  try { r.edit(6); expect(await r.controller.revokeWrite()).toBe(true); expect(r.confirmed).not.toContain(6) } finally { await r.controller.close() }
})
it("gridcraft: no engine revision state -> a differing export is still saved", async () => {
  const r = mounted("gridcraft")
  try { await r.controller.flush(); expect(r.requests.map(q => q.bytes[0])).toEqual([5]) } finally { await r.controller.close() }
})
