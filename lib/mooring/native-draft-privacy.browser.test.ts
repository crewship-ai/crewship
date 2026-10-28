// @vitest-environment node
import { existsSync } from "node:fs"
import { buildSync } from "esbuild"
import { chromium, type Page } from "@playwright/test"
import { expect, it } from "vitest"

const scope = { human: "alice", workspace: "w", page: "p", applicationId: "wordcraft" }
const fixture = { applicationId: "wordcraft", enginePin: "fixture", sessionId: "tab", generation: "1", baseVersion: "1", editorRevision: "opaque" }
type Journal = import("./native-draft").NativeDraftJournal
type NativeWindow = Window & { nativeDraft: typeof import("./native-draft"); legacyDraft: typeof import("../document-queue"); journal?: Journal; opening?: Promise<string>; status?: string; releasePurge?: () => void; purging?: boolean; revoking?: Promise<void> }
// Same contract as native-draft.browser.test.ts: runs where Playwright's Chromium is installed.
const browserIt = it.skipIf(!existsSync(chromium.executablePath()))
async function withBrowser(run: (a: Page, b: Page) => Promise<void>) {
  const browser = await chromium.launch({ headless: true })
  try {
    const context = await browser.newContext()
    await context.route("http://localhost/**", route => route.fulfill({ body: "<!doctype html><title>Native privacy barrier</title>", contentType: "text/html" }))
    const native = buildSync({ entryPoints: [`${process.cwd()}/lib/mooring/native-draft.ts`], bundle: true, write: false, format: "iife", globalName: "nativeDraft", platform: "browser" }).outputFiles[0].text
    const legacy = buildSync({ entryPoints: [`${process.cwd()}/lib/document-queue.ts`], bundle: true, write: false, format: "iife", globalName: "legacyDraft", platform: "browser" }).outputFiles[0].text
    const a = await context.newPage(), b = await context.newPage()
    for (const page of [a, b]) { await page.goto("http://localhost/privacy"); await page.addScriptTag({ content: native }); await page.addScriptTag({ content: legacy }) }
    await run(a, b)
  } finally { await browser.close() }
}
browserIt("fails native storage closed without Web Locks while preserving the legacy fallback", () => withBrowser(async a => {
  const result = await a.evaluate(async ({ scope, fixture }) => {
    const w = window as unknown as NativeWindow
    Object.defineProperty(navigator, "locks", { configurable: true, value: undefined })
    let first: Journal | undefined, peer: Journal | undefined, peerLateWrite = false
    try { first = await w.nativeDraft.openNativeDraftJournal(scope) } catch { /* Native-only fail closed. */ }
    try { peer = await w.nativeDraft.openNativeDraftJournal(scope) } catch { /* Native-only fail closed. */ }
    const nativeRefused = first === undefined && peer === undefined
    if (first && peer) {
      await first.persist({ ...fixture, bytes: new Uint8Array([1]) })
      await peer.persist({ ...fixture, bytes: new Uint8Array([2]) })
      await first.revokeRead()
      try { await peer.persist({ ...fixture, bytes: new Uint8Array([9]) }); peerLateWrite = true } catch { /* Retired producer. */ }
    }
    await first?.close(); await peer?.close()
    const legacy = await w.legacyDraft.openDocumentDrafts("alice", "w", "legacy")
    await legacy.put({ id: "legacy", schema: 1, epoch: "legacy", update: "", attempted: true, snapshot: "legacy fallback" })
    const legacyWorks = (await legacy.load()).length === 1
    legacy.close()
    return { nativeRefused, peerLateWrite, legacyWorks }
  }, { scope, fixture })
  expect(result).toEqual({ nativeRefused: true, peerLateWrite: false, legacyWorks: true })
}))
browserIt("rejects old queued native openers and holds the Page lock throughout privacy purge", () => withBrowser(async (a, b) => {
  await a.evaluate(async scope => { const w = window as unknown as NativeWindow; w.journal = await w.nativeDraft.openNativeDraftJournal(scope) }, scope)
  await b.evaluate(scope => {
    const w = window as unknown as NativeWindow
    w.status = "pending"
    w.opening = w.nativeDraft.openNativeDraftJournal(scope).then(journal => { w.journal = journal; return w.status = "opened" }, () => w.status = "refused")
  }, scope)
  await expect.poll(() => b.evaluate(async () => (window as unknown as NativeWindow).status === "refused" || (await navigator.locks.query()).pending!.length > 0)).toBe(true)
  await a.evaluate(() => {
    const w = window as unknown as NativeWindow, original = indexedDB.open.bind(indexedDB)
    indexedDB.open = (...args: Parameters<IDBFactory["open"]>) => {
      const request = original(...args)
      Object.defineProperty(request, "onsuccess", { set(callback: (event: Event) => void) {
        request.addEventListener("success", event => { w.purging = true; w.releasePurge = () => callback.call(request, event) }, { once: true })
      } })
      return request
    }
    w.revoking = w.journal!.revokeRead()
  })
  await a.waitForFunction(() => (window as unknown as NativeWindow).purging)
  const beforeRelease = await b.evaluate(async scope => {
    const w = window as unknown as NativeWindow
    const first = await w.opening
    let duringRefused = false
    if (first === "refused") {
      try { w.journal = await w.nativeDraft.openNativeDraftJournal(scope) } catch { duringRefused = true }
    }
    return { first, duringRefused }
  }, scope)
  await a.evaluate(async () => { const w = window as unknown as NativeWindow; w.releasePurge!(); await w.revoking })
  const lateWrite = await b.evaluate(async fixture => {
    const w = window as unknown as NativeWindow
    if (!w.journal) return false
    try { await w.journal.persist({ ...fixture, bytes: new Uint8Array([9]) }); return true } catch { return false } finally { await w.journal.close() }
  }, fixture)
  expect({ ...beforeRelease, lateWrite }).toEqual({ first: "refused", duringRefused: true, lateWrite: false })
}))
browserIt("rejects read revocation from a closed lifecycle and preserves a fresh authorized mount", () => withBrowser(async (a, b) => {
  await a.evaluate(async scope => { const w = window as unknown as NativeWindow; w.journal = await w.nativeDraft.openNativeDraftJournal(scope); await w.journal.close() }, scope)
  await b.evaluate(async ({ scope, fixture }) => { const w = window as unknown as NativeWindow; w.journal = await w.nativeDraft.openNativeDraftJournal(scope); await w.journal.persist({ ...fixture, bytes: new Uint8Array([7]) }) }, { scope, fixture })
  const retiredRejected = await a.evaluate(async () => { try { await (window as unknown as NativeWindow).journal!.revokeRead(); return false } catch { return true } })
  const bytes = await b.evaluate(async scope => {
    const w = window as unknown as NativeWindow; await w.journal!.close()
    const observer = await w.nativeDraft.openNativeDraftJournal(scope)
    const bytes = (await observer.recovery()).map(row => Array.from(row.draft.bytes)); await observer.close(); return bytes
  }, scope)
  expect({ retiredRejected, bytes }).toEqual({ retiredRejected: true, bytes: [[7]] })
}))
