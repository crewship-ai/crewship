// @vitest-environment node
import { webcrypto } from "node:crypto"
import { expect, it, vi } from "vitest"
import { createSingleEditorSave, type NativeSaveRequest } from "./single-editor-save"

it("does not continually schedule redundant versions while the next snapshot is already preparing", async () => {
  vi.stubGlobal("crypto", webcrypto)
  let firstAck!: (value: unknown) => void, secondStarted!: () => void, thirdSent!: () => void
  const ack = new Promise<unknown>(resolve => { firstAck = resolve })
  const preparing = new Promise<void>(resolve => { secondStarted = resolve })
  const third = new Promise<boolean>(resolve => { thirdSent = () => resolve(true) })
  const requests: NativeSaveRequest[] = []
  let captures = 0
  const receipt = (r: NativeSaveRequest) => ({ ...r, version: String(BigInt(r.baseVersion) + 1n), durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: r.contentHash })
  const controller = createSingleEditorSave({ workspaceId: "w", pageId: "p", applicationId: "wordcraft", enginePin: "synthetic-engine", baseVersion: "1", generation: "1", sessionId: "session", idleMs: 5, onState() {}, journal: { async persist() {}, async confirm() {}, async recovery() { return [] }, onRetire() { return () => {} }, async close() {}, async revokeRead() {} }, engine: {
    async snapshot() {
      captures++
      if (captures === 1) return { bytes: new Uint8Array([1]), editorRevision: "revision:1" }
      secondStarted()
      await new Promise(resolve => setTimeout(resolve, 40))
      return { bytes: new Uint8Array([2]), editorRevision: "revision:2" }
    }, async validate() {},
  }, transport: { async save(r) {
    requests.push(r)
    if (requests.length === 1) return ack
    if (requests.length === 3) thirdSent()
    return receipt(r)
  } } })
  try {
    const original = controller.save()
    await vi.waitFor(() => expect(requests).toHaveLength(1))
    controller.edited() // Only one new edit; a second snapshot starts before the first ACK.
    await preparing
    firstAck(receipt(requests[0]))
    await original
    const redundant = await Promise.race([third, new Promise<boolean>(resolve => setTimeout(() => resolve(false), 500))])
    console.log("native-save-coalesce-review", JSON.stringify(requests.map(r => ({ base: r.baseVersion, revision: r.editorRevision, hash: r.contentHash }))))
    expect(redundant).toBe(false)
  } finally { await controller.close(); vi.unstubAllGlobals() }
})
