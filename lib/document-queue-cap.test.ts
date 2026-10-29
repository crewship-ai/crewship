import { afterEach, expect, it, vi } from "vitest"
import { DocumentStorageError, openDocumentDrafts } from "./document-queue"

afterEach(() => vi.unstubAllGlobals())

function storage() {
  vi.stubGlobal("navigator", {})
  const put = vi.fn(() => ({}))
  const db = { close: vi.fn(), transaction: () => {
    const tx = { objectStore: () => ({ put }), oncomplete: null as null | (() => void), abort: vi.fn() }
    queueMicrotask(() => tx.oncomplete?.())
    return tx
  } }
  vi.stubGlobal("indexedDB", { open: () => {
    const request = { result: db, onsuccess: null as null | (() => void) }
    queueMicrotask(() => request.onsuccess?.())
    return request
  } })
  return put
}

it("keeps the legacy 4 MiB journal cap and opts a native document into a bounded larger row", async () => {
  const put = storage()
  const draft = { id: "large-native", schema: 1, epoch: "native", attempted: false, update: "", snapshot: "x".repeat(5 * 1024 * 1024) }
  const legacy = await openDocumentDrafts("h", "w", "p")
  await expect(legacy.put(draft)).rejects.toBeInstanceOf(DocumentStorageError)
  expect(put).not.toHaveBeenCalled()
  legacy.close()
  const native = await openDocumentDrafts("h", "w", "p", { maxDraftBytes: 64 * 1024 * 1024 })
  await native.put(draft)
  expect(put).toHaveBeenCalledOnce()
  native.close()
})

it("rejects a cap above 64 MiB before opening private storage", async () => {
  storage()
  const open = vi.spyOn(indexedDB, "open")
  await expect(openDocumentDrafts("h", "w", "p", { maxDraftBytes: 64 * 1024 * 1024 + 1 })).rejects.toBeInstanceOf(DocumentStorageError)
  expect(open).not.toHaveBeenCalled()
})
