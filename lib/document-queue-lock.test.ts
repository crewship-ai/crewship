import { afterEach, expect, it, vi } from "vitest"
import { DocumentStorageBusy, openDocumentDrafts } from "./document-queue"

afterEach(() => vi.unstubAllGlobals())

type Cb = (lock: unknown) => Promise<void> | void
/** Web Locks semantics the queue relies on: exclusive, queued, abortable. */
function fakeBrowser(requestOverride?: () => Promise<never>) {
  const holders = new Map<string, boolean>(), waiting = new Map<string, (() => void)[]>()
  const grant = (name: string, cb: Cb) => { holders.set(name, true); return Promise.resolve(cb({ name })).finally(() => { holders.delete(name); waiting.get(name)?.shift()?.() }) }
  vi.stubGlobal("navigator", { locks: { request: (name: string, options: { signal?: AbortSignal }, cb: Cb) => {
    if (requestOverride) return requestOverride()
    if (!holders.has(name)) return grant(name, cb)
    return new Promise<void>((resolve, reject) => {
      const start = () => { resolve(); void grant(name, cb) }
      waiting.set(name, [...(waiting.get(name) ?? []), start])
      options.signal?.addEventListener("abort", () => { waiting.set(name, (waiting.get(name) ?? []).filter(item => item !== start)); reject(new DOMException("aborted", "AbortError")) })
    })
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
  expect(a.exclusive).toBe(true)
  await expect(openDocumentDrafts("h", "w", "p")).rejects.toBeInstanceOf(DocumentStorageBusy)
  expect(dbs).toHaveLength(1) // the busy tab never opened the database
  const other = await openDocumentDrafts("h", "w", "q") // another Page is independent
  a.close(); other.close()
})

it("waits briefly for the lock, so a same-tab remount takes over from the closing instance", async () => {
  fakeBrowser()
  const first = await openDocumentDrafts("h", "w", "p")
  const second = openDocumentDrafts("h", "w", "p")
  setTimeout(() => first.close(), 100)
  const store = await second
  expect(store.exclusive).toBe(true)
  store.close()
})

it("treats a failing lock request as no lock available, not as busy", async () => {
  fakeBrowser(() => Promise.reject(new DOMException("denied", "SecurityError")))
  const store = await openDocumentDrafts("h", "w", "p")
  expect(store.exclusive).toBe(false)
  store.close()
})

it("reports the journal as non-exclusive when Web Locks do not exist", async () => {
  fakeBrowser()
  vi.stubGlobal("navigator", {})
  const store = await openDocumentDrafts("h", "w", "p")
  expect(store.exclusive).toBe(false)
  store.close()
})

it("treats a synchronous throw from the lock request as no lock available", async () => {
  fakeBrowser()
  vi.stubGlobal("navigator", { locks: { request: () => { throw new TypeError("not allowed") } } })
  const store = await openDocumentDrafts("h", "w", "p")
  expect(store.exclusive).toBe(false)
  store.close()
})
