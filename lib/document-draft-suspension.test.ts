// @vitest-environment node
import { expect, it, vi } from "vitest"
import { purgeOtherDocumentDrafts, registerDocumentDraftWriter, suspendOtherDocumentDrafts } from "./document-queue"
it("keeps the journal and reports a failed editor capture instead of pretending suspension succeeded", async () => {
  const storageClose = vi.fn(async () => {}), editorClose = vi.fn(async () => {})
  let available = false, retained = false
  registerDocumentDraftWriter("alice", editorClose, { suspend: async () => { if (!available) throw new Error("pending draft could not be retained"); retained = true } })
  registerDocumentDraftWriter("alice", storageClose, { storage: true })
  try {
    await expect(suspendOtherDocumentDrafts(null)).rejects.toThrow("could not be retained")
    expect(storageClose).not.toHaveBeenCalled()
    available = true
    await suspendOtherDocumentDrafts(null)
    expect(retained).toBe(true)
    expect(storageClose).toHaveBeenCalledOnce()
  } finally { await purgeOtherDocumentDrafts(null) }
})
