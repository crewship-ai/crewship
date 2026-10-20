import { afterEach, expect, it, vi } from "vitest"
import { purgeDocumentDrafts, purgeOtherDocumentDrafts } from "./document-queue"

afterEach(() => { vi.unstubAllGlobals() })

// Browsers and test environments can expose indexedDB without IDBKeyRange, or a
// broken open(): the best-effort purge must resolve, never reject.
it("resolves when indexedDB exists but IDBKeyRange does not", async () => {
  vi.stubGlobal("indexedDB", { open: () => { throw new Error("unavailable") } })
  vi.stubGlobal("IDBKeyRange", undefined)
  await expect(purgeDocumentDrafts("human", "w", "page")).resolves.toBeUndefined()
  await expect(purgeOtherDocumentDrafts(null)).resolves.toBeUndefined()
})

it("resolves when indexedDB is absent", async () => {
  vi.stubGlobal("indexedDB", undefined)
  await expect(purgeDocumentDrafts("human", "w", "page")).resolves.toBeUndefined()
  await expect(purgeOtherDocumentDrafts("human")).resolves.toBeUndefined()
})
