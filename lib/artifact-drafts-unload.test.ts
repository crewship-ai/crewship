import { afterEach, beforeEach, expect, it, vi } from "vitest"

// #4823: an edit typed right before the page unloads. The strict IndexedDB
// write is asynchronous (seal, then transaction) and dies with the page, so
// the restored draft was the pre-edit text. No unauthenticated synchronous
// capture is made (Codex #4836 B1); the browser asks before leaving instead.
// States kept apart below:
//   typed / queued  the editor called writeArtifactDraft (the lane holds the text in RAM),
//   durable   the strict IndexedDB transaction committed,
//   server-confirmed  the editor's save ACK cleared the draft (clearArtifactDraft).
vi.mock("./local-draft-seal", () => ({
 createLocalDraftCipher: async () => ({
  seal: async (text: string) => `sealed:${text}`,
  open: async (text: string) => text.replace(/^(sealed|instant):/, ""),
  close() {},
 }),
 forgetLocalDraftPage() {}, isSealedLocalDraft: (text: string) => /^(sealed|instant):/.test(text),
 LocalDraftReadRevoked: class extends Error {},
}))
type Drafts = typeof import("./artifact-drafts")
type Row = [string, string, string, string]

/** A disk that changes only when a write is flushed; an unload never flushes it. */
function durableStore() {
 const disk = new Map<string, string>()
 const pending: (() => void)[] = []
 const failures: (() => void)[] = []
 const store = {
  disk, auto: true,
  flush() { for (const done of pending.splice(0)) done() },
  flushOne() { pending.shift()?.() },
  failOne() { failures.shift()?.() },
  capture: vi.fn(async () => []), removeCaptured: vi.fn(async () => {}),
  get: vi.fn(async (row: Row) => disk.get(JSON.stringify(row))),
  put: vi.fn((row: Row, text: string) => new Promise<void>((resolve, reject) => {
   const done = () => { const at = failures.indexOf(fail); if (at >= 0) failures.splice(at, 1); disk.set(JSON.stringify(row), text); resolve() }
   const fail = () => { const at = pending.indexOf(done); if (at >= 0) pending.splice(at, 1); reject(new Error("quota")) }
   if (store.auto) done(); else { pending.push(done); failures.push(fail) }
  })),
  delete: vi.fn(async (row: Row) => { disk.delete(JSON.stringify(row)) }),
  purge: vi.fn(async () => {}),
 }
 stores.push(store)
 return store
}
// Earlier tests' module instances keep their beforeunload listener: let their writes finish.
const stores: { auto: boolean; flush(): void }[] = []
const local = new Map<string, string>()
async function load(): Promise<Drafts> { vi.resetModules(); return import("./artifact-drafts") }
beforeEach(() => {
 local.clear()
 vi.mocked(localStorage.getItem).mockImplementation(key => local.get(key) ?? null)
 vi.mocked(localStorage.setItem).mockImplementation((key, value) => { local.set(key, String(value)) })
 vi.mocked(localStorage.removeItem).mockImplementation(key => { local.delete(key) })
})
afterEach(async () => {
 for (const store of stores.splice(0)) { store.auto = true; store.flush() }
 await new Promise(resolve => setTimeout(resolve, 20))
 for (const store of stores.splice(0)) store.flush()
 vi.mocked(localStorage.getItem).mockReset().mockReturnValue(null)
})

it("asks before leaving while an edit typed just before unload is not durable (#4823)", async () => {
 const store = durableStore()
 const page = await load(); page.setArtifactDraftStore(store)
 const key = page.artifactDraftKey("artifact-source", "alice", "w", "page-1")!
 await page.writeArtifactDraft(key, "before the edit")
 const settled = new Event("beforeunload", { cancelable: true })
 window.dispatchEvent(settled)
 expect(settled.defaultPrevented, "nothing to lose: no prompt").toBe(false)
 store.auto = false
 // Typed, then the page goes away in the same tick: the strict write never commits.
 void page.writeArtifactDraft(key, "before the edit and the last words").catch(() => {})
 const leave = new Event("beforeunload", { cancelable: true })
 window.dispatchEvent(leave)
 expect(leave.defaultPrevented, "the uncommitted edit is announced").toBe(true)
 window.dispatchEvent(new Event("pagehide"))
 const reopened = await load(); store.auto = true; reopened.setArtifactDraftStore(store)
 await expect(reopened.loadArtifactDraft(key)).resolves.toBe("before the edit")
})

it("writes nothing to localStorage on unload: no unauthenticated or plaintext record (Codex #4836 B1)", async () => {
 const store = durableStore()
 const page = await load(); page.setArtifactDraftStore(store)
 const key = page.artifactDraftKey("artifact-comment", "alice", "w", "page-1")!
 await page.loadArtifactDraft(key)
 store.auto = false
 void page.writeArtifactDraft(key, "private words").catch(() => {})
 window.dispatchEvent(new Event("beforeunload", { cancelable: true }))
 Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" })
 try { document.dispatchEvent(new Event("visibilitychange")) } finally { Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" }) }
 window.dispatchEvent(new Event("pagehide"))
 expect(local.size).toBe(0)
})

it("reports typed (queued) and durable apart, and stops asking once the newest write is durable", async () => {
 const store = durableStore()
 const page = await load(); page.setArtifactDraftStore(store)
 const key = page.artifactDraftKey("artifact-comment", "alice", "w", "page-1")!
 await page.loadArtifactDraft(key)
 expect(page.artifactDraftStorageState(key)).toBe("none")
 store.auto = false
 const first = page.writeArtifactDraft(key, "one")
 expect(page.artifactDraftStorageState(key)).toBe("queued")
 await vi.waitFor(() => expect(store.put).toHaveBeenCalledTimes(1))
 const second = page.writeArtifactDraft(key, "one two")
 store.flushOne(); await first
 // Write 1 is durable, write 2 is not: still queued, still asking.
 expect(page.artifactDraftStorageState(key)).toBe("queued")
 const pending = new Event("beforeunload", { cancelable: true })
 window.dispatchEvent(pending)
 expect(pending.defaultPrevented).toBe(true)
 await vi.waitFor(() => expect(store.put).toHaveBeenCalledTimes(2))
 store.flushOne(); await second
 expect(page.artifactDraftStorageState(key)).toBe("durable")
 const done = new Event("beforeunload", { cancelable: true })
 window.dispatchEvent(done)
 expect(done.defaultPrevented).toBe(false)
})

// Codex #4836 round 1 B2: another tab's older durable write must not leave this
// tab's newer edit unprotected: it is either restored or the leave prompt asks.
it("does not lose a pending edit when another tab's older write commits", async () => {
 const store = durableStore()
 const tabA = await load(); tabA.setArtifactDraftStore(store)
 const key = tabA.artifactDraftKey("artifact-comment", "alice", "w", "page-1")!
 await tabA.loadArtifactDraft(key)
 const tabB = await load(); tabB.setArtifactDraftStore(store)
 await tabB.loadArtifactDraft(key)
 store.auto = false
 const older = tabB.writeArtifactDraft(key, "B older")
 await vi.waitFor(() => expect(store.put).toHaveBeenCalledTimes(1))
 void tabA.writeArtifactDraft(key, "A newest").catch(() => {})
 await vi.waitFor(() => expect(store.put).toHaveBeenCalledTimes(2))
 // A is closed: beforeunload, then pagehide. Only B's older write commits
 // afterwards; A's own write never does.
 const leave = new Event("beforeunload", { cancelable: true })
 window.dispatchEvent(leave)
 window.dispatchEvent(new Event("pagehide"))
 store.flushOne()
 await older
 const reopened = await load(); store.auto = true; reopened.setArtifactDraftStore(store)
 const restored = await reopened.loadArtifactDraft(key)
 expect(restored === "A newest" || leave.defaultPrevented, `restored ${restored}, warned ${leave.defaultPrevented}`).toBe(true)
})

// Codex #4836 round 1 B3: write 1's storage failure must not replace write 2.
it("does not lose write 2 when write 1's strict write is refused", async () => {
 const store = durableStore()
 const page = await load(); page.setArtifactDraftStore(store)
 const key = page.artifactDraftKey("artifact-comment", "alice", "w", "page-1")!
 await page.loadArtifactDraft(key)
 store.auto = false
 const first = page.writeArtifactDraft(key, "write 1")
 await vi.waitFor(() => expect(store.put).toHaveBeenCalledTimes(1))
 void page.writeArtifactDraft(key, "write 1 and write 2").catch(() => {})
 const leave = new Event("beforeunload", { cancelable: true })
 window.dispatchEvent(leave)
 window.dispatchEvent(new Event("pagehide"))
 store.failOne()
 await first.catch(() => {})
 const reopened = await load(); store.auto = true; reopened.setArtifactDraftStore(store)
 const restored = await reopened.loadArtifactDraft(key)
 expect(restored === "write 1 and write 2" || leave.defaultPrevented, `restored ${restored}, warned ${leave.defaultPrevented}`).toBe(true)
})

// Codex #4836 round 1 B4: a merge's write number is fixed at admission.
it("still asks before leaving while a second queued merge has not committed", async () => {
 const store = durableStore()
 const page = await load(); page.setArtifactDraftStore(store)
 const key = page.artifactDraftKey("wordcraft-pending", "alice", "w", "page-1")!
 await page.loadArtifactDraft(key)
 store.auto = false
 const one = page.mergeArtifactDraft(key, existing => `${existing}[1]`)
 void page.mergeArtifactDraft(key, existing => `${existing}[2]`).catch(() => {})
 await vi.waitFor(() => expect(store.put).toHaveBeenCalledTimes(1))
 store.flushOne(); await one
 expect(page.artifactDraftStorageState(key)).toBe("queued")
 const leave = new Event("beforeunload", { cancelable: true })
 window.dispatchEvent(leave)
 expect(leave.defaultPrevented).toBe(true)
})
