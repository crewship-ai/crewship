import { afterEach, expect, it, vi } from "vitest"
import { DocumentStorageBusy, openDocumentDrafts } from "./document-queue"

afterEach(() => vi.unstubAllGlobals())

function fakeBrowser() {
  const held = new Set<string>()
  vi.stubGlobal("navigator", { locks: { request: (name: string, _o: unknown, cb: (lock: unknown) => Promise<void> | void) => {
    if (held.has(name)) return Promise.resolve(cb(null))
    held.add(name)
    return Promise.resolve(cb({ name })).finally(() => held.delete(name))
  } } })
  const dbs: { close: ReturnType<typeof vi.fn> }[] = []
  vi.stubGlobal("indexedDB", { open: () => {
    const request: Record<string, unknown> = {}
    const db = { close: vi.fn(), onversionchange: null }
    dbs.push(db)
    Object.defineProperty(request, "result", { get: () => db })
    setTimeout(() => (request.onsuccess as () => void)(), 0)
    return request
  } })
  return dbs
}

it("lets only one tab own a Page's journal, so another tab never reads live batches as recovery", async () => {
  const dbs = fakeBrowser()
  const a = await openDocumentDrafts("h", "w", "p")
  await expect(openDocumentDrafts("h", "w", "p")).rejects.toBeInstanceOf(DocumentStorageBusy)
  expect(dbs).toHaveLength(1) // the busy tab never opened the database
  // Another Page is independent.
  const other = await openDocumentDrafts("h", "w", "q")
  a.close(); other.close()
  await new Promise(resolve => setTimeout(resolve, 0))
  const b = await openDocumentDrafts("h", "w", "p")
  b.close()
})
