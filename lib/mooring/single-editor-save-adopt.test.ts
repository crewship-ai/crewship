// @vitest-environment node
import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { createSingleEditorSave, type NativeSaveRequest, type NativeSaveState } from "./single-editor-save"

beforeEach(() => vi.stubGlobal("crypto", webcrypto))
afterEach(() => vi.unstubAllGlobals())

const receipt = (r: NativeSaveRequest) => ({ ...r, version: String(BigInt(r.baseVersion) + 1n), durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash })
const sha = async (b: Uint8Array) => `sha256:${Buffer.from(await webcrypto.subtle.digest("SHA-256", b)).toString("hex")}`
function fixture(content: () => Uint8Array) {
  let captures = 0
  const requests: NativeSaveRequest[] = [], states: NativeSaveState[] = []
  const controller = createSingleEditorSave({ workspaceId: "w", pageId: "p", applicationId: "gridcraft", enginePin: "engine", baseVersion: "1", generation: "1", sessionId: "s", idleMs: 5,
    journal: { async persist() {}, async confirm() {}, async recovery() { return [] }, onRetire() { return () => {} }, async close() {}, async revokeRead() {}, async discardRecovered() {} },
    engine: { async snapshot(_s, id) { captures++; return { bytes: content(), editorRevision: id ?? `rpc:${captures}`, nativeState: undefined, revision: undefined } }, async validate() {} },
    transport: { async save(r) { requests.push(r); return receipt(r) } }, onState(s) { states.push(s) }, onReceipt: vi.fn(), timeoutMs: 5000,
  })
  return { controller, requests, states, get captures() { return captures } }
}
const enc = (s: string) => new TextEncoder().encode(s)

it("after adopting the agent's version the same content saves nothing and other content saves on top of it", async () => {
  let doc = enc('{"v":"agent"}')
  const r = fixture(() => doc)
  try {
    expect(r.controller.adoptVersion("5", await sha(enc('{"v":"agent"}')))).toBe(true)
    r.controller.edited()
    await vi.waitFor(() => expect(r.captures).toBeGreaterThanOrEqual(1), { timeout: 3000 })
    await new Promise(resolve => setTimeout(resolve, 100))
    expect(r.requests, "identical to the adopted version: nothing to save").toHaveLength(0)
    doc = enc('{"v":"person edited after the agent"}')
    r.controller.edited()
    await vi.waitFor(() => expect(r.requests).toHaveLength(1), { timeout: 3000 })
    expect(r.requests[0].baseVersion, "a new version on top of the agent's, never an overwrite").toBe("5")
  } finally { await r.controller.close() }
})

it("refuses to adopt over unsaved work, an older version or a malformed hash, and adopts nothing then", async () => {
  const r = fixture(() => enc('{"v":1}'))
  try {
    const hash = await sha(enc("x"))
    expect(r.controller.adoptVersion("1", hash), "not newer than the base").toBe(false)
    expect(r.controller.adoptVersion("0", hash)).toBe(false)
    expect(r.controller.adoptVersion("5", "sha256:nothex")).toBe(false)
    r.controller.edited() // unsaved work of the person
    expect(r.controller.adoptVersion("5", hash)).toBe(false)
  } finally { await r.controller.close() }
})
