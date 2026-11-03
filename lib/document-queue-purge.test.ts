// @vitest-environment node
// Real IndexedDB in Chromium, no storage mock. Skipped where Chromium is not provisioned.
import { existsSync } from "node:fs"
import { buildSync } from "esbuild"
import { chromium } from "@playwright/test"
import { expect, it } from "vitest"

it.skipIf(!existsSync(chromium.executablePath()))("purges a Page's rows, then other users' rows, from IndexedDB itself", async () => {
  const browser = await chromium.launch({ headless: true })
  try {
    const page = await browser.newPage()
    await page.route("https://document-queue.test/**", route => route.fulfill({ body: "<!doctype html><title>Document journal</title>", contentType: "text/html" }))
    await page.goto("https://document-queue.test")
    const { outputFiles } = buildSync({ entryPoints: [`${process.cwd()}/lib/document-queue.ts`], bundle: true, write: false, platform: "browser", format: "iife", globalName: "documentQueue" })
    await page.addScriptTag({ content: outputFiles[0].text })
    const result = await page.evaluate(async () => {
      const q = (window as unknown as { documentQueue: typeof import("./document-queue") }).documentQueue
      const draft = (id: string) => ({ id, update: "u", attempted: false, schema: 3, epoch: "e", snapshot: "private text" })
      const seed = async (human: string, workspace: string, pageId: string, id: string) => { const store = await q.openDocumentDrafts(human, workspace, pageId); await store.put(draft(id)); store.close() }
      const ids = async (human: string, workspace: string, pageId: string) => { const store = await q.openDocumentDrafts(human, workspace, pageId); try { return (await store.load()).map(row => row.id) } finally { store.close() } }
      await seed("alice", "w", "p1", "a-p1"); await seed("alice", "w", "p2", "a-p2"); await seed("alice", "w2", "p1", "a-w2"); await seed("bob", "w", "p1", "b-p1")
      const before = { a1: await ids("alice", "w", "p1"), a2: await ids("alice", "w", "p2"), b: await ids("bob", "w", "p1") }
      await q.purgeDocumentDrafts("alice", "w", "p1")
      const afterPage = { a1: await ids("alice", "w", "p1"), a2: await ids("alice", "w", "p2"), aw2: await ids("alice", "w2", "p1"), b: await ids("bob", "w", "p1") }
      await q.purgeOtherDocumentDrafts("alice")
      const afterOthers = { a2: await ids("alice", "w", "p2"), aw2: await ids("alice", "w2", "p1"), b: await ids("bob", "w", "p1") }
      await q.purgeOtherDocumentDrafts(null)
      const afterSignOut = { a2: await ids("alice", "w", "p2"), aw2: await ids("alice", "w2", "p1"), b: await ids("bob", "w", "p1") }
      return { before, afterPage, afterOthers, afterSignOut }
    })
    expect(result.before).toEqual({ a1: ["a-p1"], a2: ["a-p2"], b: ["b-p1"] })
    expect(result.afterPage).toEqual({ a1: [], a2: ["a-p2"], aw2: ["a-w2"], b: ["b-p1"] })
    expect(result.afterOthers).toEqual({ a2: ["a-p2"], aw2: ["a-w2"], b: [] })
    expect(result.afterSignOut).toEqual({ a2: [], aw2: [], b: [] })
  } finally { await browser.close() }
})

it.skipIf(!existsSync(chromium.executablePath()))("removes exactly one row by its full key, without the Page lock (#4348)", async () => {
  const browser = await chromium.launch({ headless: true })
  try {
    const page = await browser.newPage()
    await page.route("https://document-queue.test/**", route => route.fulfill({ body: "<!doctype html><title>Document journal</title>", contentType: "text/html" }))
    await page.goto("https://document-queue.test")
    const { outputFiles } = buildSync({ entryPoints: [`${process.cwd()}/lib/document-queue.ts`], bundle: true, write: false, platform: "browser", format: "iife", globalName: "documentQueue" })
    await page.addScriptTag({ content: outputFiles[0].text })
    const result = await page.evaluate(async () => {
      const q = (window as unknown as { documentQueue: typeof import("./document-queue") }).documentQueue
      const draft = (id: string) => ({ id, update: "", attempted: true, schema: 1, epoch: "native-v1", snapshot: "private text" })
      const store = await q.openDocumentDrafts("alice", "w", "p1")
      await store.put(draft("native:old")); await store.put(draft("native:fresh"))
      const other = await q.openDocumentDrafts("alice", "w", "p2"); await other.put(draft("native:old")); other.close()
      // The fresh mount still holds its store (and the Page lock) open.
      await q.removeDocumentDraftRow("alice", "w", "p1", { schema: 1, epoch: "native-v1", id: "native:old" })
      const ids = async (pageId: string) => { const s = await q.openDocumentDrafts("alice", "w", pageId); try { return (await s.load()).map(row => row.id) } finally { s.close() } }
      store.close()
      return { p1: await ids("p1"), p2: await ids("p2") }
    })
    expect(result).toEqual({ p1: ["native:fresh"], p2: ["native:old"] })
  } finally { await browser.close() }
})

it("closes an IndexedDB connection that opens after the purge deadline", async () => {
  const { vi } = await import("vitest")
  const { purgeDocumentDrafts, STORAGE_OPEN_TIMEOUT_MS } = await import("./document-queue")
  const close = vi.fn()
  const request = { result: { close } } as unknown as IDBOpenDBRequest
  vi.useFakeTimers()
  vi.stubGlobal("indexedDB", { open: () => request })
  try {
    const purge = purgeDocumentDrafts("alice", "w", "p")
    await vi.advanceTimersByTimeAsync(STORAGE_OPEN_TIMEOUT_MS)
    await purge
    request.onsuccess?.call(request, new Event("success"))
    expect(close).toHaveBeenCalledOnce()
  } finally { vi.useRealTimers(); vi.unstubAllGlobals() }
})

it("aborts a stuck purge transaction and closes its connection at the deadline", async () => {
  const { vi } = await import("vitest")
  const { purgeDocumentDrafts, STORAGE_OPEN_TIMEOUT_MS } = await import("./document-queue")
  const abort = vi.fn(), close = vi.fn()
  const transaction = { abort, objectStore: () => ({ openCursor: () => ({}) }) }
  const request = { result: { close, transaction: () => transaction } } as unknown as IDBOpenDBRequest
  vi.useFakeTimers()
  vi.stubGlobal("indexedDB", { open: () => request })
  vi.stubGlobal("IDBKeyRange", { bound: () => undefined })
  try {
    let settled = false
    void purgeDocumentDrafts("alice", "w", "p").then(() => { settled = true })
    request.onsuccess?.call(request, new Event("success"))
    await vi.advanceTimersByTimeAsync(STORAGE_OPEN_TIMEOUT_MS)
    expect(settled).toBe(true)
    expect(abort).toHaveBeenCalledOnce()
    expect(close).toHaveBeenCalledOnce()
  } finally { vi.useRealTimers(); vi.unstubAllGlobals() }
})
