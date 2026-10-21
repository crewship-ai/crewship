import { afterEach, expect, it, vi } from "vitest"
import { DocumentStorageError, STORAGE_OPEN_TIMEOUT_MS, openDocumentDrafts } from "./document-queue"

afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })

it("closes a connection that arrives after the open timeout and rejects", async () => {
  vi.useFakeTimers()
  const request: Record<string, unknown> = {}
  const db = { close: vi.fn() }
  Object.defineProperty(request, "result", { get: () => db })
  vi.stubGlobal("indexedDB", { open: () => request })
  const opened = openDocumentDrafts("h", "w", "p")
  const failure = expect(opened).rejects.toBeInstanceOf(DocumentStorageError)
  await vi.advanceTimersByTimeAsync(STORAGE_OPEN_TIMEOUT_MS + 1)
  await failure
  ;(request.onsuccess as () => void)()
  expect(db.close).toHaveBeenCalledTimes(1)
})

it("bounds load() with the same timeout when the transaction never completes", async () => {
  vi.useFakeTimers()
  const request: Record<string, unknown> = {}
  const db = { close: vi.fn(), onversionchange: null, transaction: () => ({ objectStore: () => ({ getAll: () => ({}) }) }) }
  Object.defineProperty(request, "result", { get: () => db })
  vi.stubGlobal("indexedDB", { open: () => { setTimeout(() => (request.onsuccess as () => void)(), 0); return request } })
  vi.stubGlobal("IDBKeyRange", { bound: () => ({}) })
  const store = await (async () => { const p = openDocumentDrafts("h", "w", "p"); await vi.advanceTimersByTimeAsync(1); return p })()
  const loaded = expect(store.load()).rejects.toBeInstanceOf(DocumentStorageError)
  await vi.advanceTimersByTimeAsync(STORAGE_OPEN_TIMEOUT_MS + 1)
  await loaded
  store.close()
  expect(db.close).toHaveBeenCalledTimes(1)
})
