import { afterEach, beforeEach, expect, it, vi } from "vitest"

// #4823: an edit typed right before the page unloads. The strict IndexedDB
// write is asynchronous (seal, then transaction) and dies with the page, so
// the restored draft was the pre-edit text. States kept apart below:
//   typed     the editor called writeArtifactDraft (the lane holds the text in RAM),
//   captured  sealed synchronously into localStorage on pagehide/hidden/beforeunload,
//   durable   the strict IndexedDB transaction committed,
//   server-confirmed  the editor's save ACK cleared the draft (clearArtifactDraft).
const cipher = vi.hoisted(() => ({ instant: true }))
vi.mock("./local-draft-seal", () => ({
 createLocalDraftCipher: async () => ({
  seal: async (text: string) => `sealed:${text}`,
  open: async (text: string) => text.replace(/^(sealed|instant):/, ""),
  prepareInstant: async () => {},
  sealNow: (text: string) => cipher.instant ? `instant:${text}` : undefined,
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
 const store = {
  disk, auto: true,
  flush() { for (const done of pending.splice(0)) done() },
  capture: vi.fn(async () => []), removeCaptured: vi.fn(async () => {}),
  get: vi.fn(async (row: Row) => disk.get(JSON.stringify(row))),
  put: vi.fn((row: Row, text: string) => new Promise<void>(resolve => {
   const done = () => { disk.set(JSON.stringify(row), text); resolve() }
   if (store.auto) done(); else pending.push(done)
  })),
  delete: vi.fn(async (row: Row) => { disk.delete(JSON.stringify(row)) }),
  purge: vi.fn(async () => {}),
 }
 return store
}
const local = new Map<string, string>()
async function load(): Promise<Drafts> { vi.resetModules(); return import("./artifact-drafts") }
const unload = () => { window.dispatchEvent(new Event("beforeunload", { cancelable: true })); window.dispatchEvent(new Event("pagehide")) }
beforeEach(() => {
 local.clear(); cipher.instant = true
 vi.mocked(localStorage.getItem).mockImplementation(key => local.get(key) ?? null)
 vi.mocked(localStorage.setItem).mockImplementation((key, value) => { local.set(key, String(value)) })
 vi.mocked(localStorage.removeItem).mockImplementation(key => { local.delete(key) })
})
afterEach(() => { vi.mocked(localStorage.getItem).mockReset().mockReturnValue(null) })

it("keeps an edit typed right before the page unloads (#4823)", async () => {
 const store = durableStore()
 const page = await load(); page.setArtifactDraftStore(store)
 const key = page.artifactDraftKey("artifact-source", "alice", "w", "page-1")!
 await page.writeArtifactDraft(key, "before the edit")
 await expect(page.loadArtifactDraft(key)).resolves.toBe("before the edit")
 store.auto = false
 // Typed, then the page goes away in the same tick: the strict write never commits.
 void page.writeArtifactDraft(key, "before the edit and the last words").catch(() => {})
 unload()
 expect(store.disk.get(JSON.stringify(["alice", "artifact-source", "w", "page-1"]))).toBe("sealed:before the edit")

 const reopened = await load(); store.auto = true; reopened.setArtifactDraftStore(store)
 await expect(reopened.loadArtifactDraft(key)).resolves.toBe("before the edit and the last words")
 // Recovery re-seals it into the strict store and drops the unload capture.
 expect(store.disk.get(JSON.stringify(["alice", "artifact-source", "w", "page-1"]))).toBe("sealed:before the edit and the last words")
 expect(local.size).toBe(0)
})

it("never stores plaintext on unload and stores nothing under another account's key", async () => {
 const store = durableStore()
 const page = await load(); page.setArtifactDraftStore(store)
 const key = page.artifactDraftKey("artifact-comment", "alice", "w", "page-1")!
 await page.loadArtifactDraft(key)
 store.auto = false
 void page.writeArtifactDraft(key, "private words").catch(() => {})
 unload()
 expect([...local.values()].every(value => value.startsWith("instant:") || value.startsWith("sealed:"))).toBe(true)
 expect([...local.keys()].every(name => name.includes(":alice:"))).toBe(true)
 const reopened = await load(); reopened.setArtifactDraftStore(store)
 await expect(reopened.loadArtifactDraft(reopened.artifactDraftKey("artifact-comment", "bob", "w", "page-1")!)).resolves.toBe("")
})

it("reports typed, captured, durable apart, and drops a capture once a newer write is durable", async () => {
 const store = durableStore()
 const page = await load(); page.setArtifactDraftStore(store)
 const key = page.artifactDraftKey("artifact-comment", "alice", "w", "page-1")!
 await page.loadArtifactDraft(key)
 expect(page.artifactDraftStorageState(key)).toBe("none")
 store.auto = false
 const first = page.writeArtifactDraft(key, "one")
 expect(page.artifactDraftStorageState(key)).toBe("queued")
 await vi.waitFor(() => expect(store.put).toHaveBeenCalledTimes(1))
 // Hidden (tab switch, bfcache): captured, but the page lives on.
 Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" })
 try { document.dispatchEvent(new Event("visibilitychange")) } finally { Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" }) }
 expect(page.artifactDraftStorageState(key)).toBe("captured")
 expect(local.get("artifact-comment:alice:w:page-1")).toBe("instant:one")
 const second = page.writeArtifactDraft(key, "one two")
 expect(page.artifactDraftStorageState(key)).toBe("queued")
 // The captured write commits: its capture goes, the newer write is still queued.
 store.flush(); await first
 expect(local.size).toBe(0)
 await vi.waitFor(() => expect(store.put).toHaveBeenCalledTimes(2))
 store.flush(); await second
 expect(page.artifactDraftStorageState(key)).toBe("durable")
 expect(local.size).toBe(0)
})

it("keeps a newer capture when an older in-flight write commits after it", async () => {
 const store = durableStore()
 const page = await load(); page.setArtifactDraftStore(store)
 const key = page.artifactDraftKey("artifact-comment", "alice", "w", "page-1")!
 await page.loadArtifactDraft(key)
 store.auto = false
 const first = page.writeArtifactDraft(key, "old")
 await vi.waitFor(() => expect(store.put).toHaveBeenCalledTimes(1))
 void page.writeArtifactDraft(key, "old and new").catch(() => {})
 unload()
 // "old" commits after the unload capture of "old and new": the capture stays.
 store.flush(); await first
 expect(local.get("artifact-comment:alice:w:page-1")).toBe("instant:old and new")
 const reopened = await load(); store.auto = true; reopened.setArtifactDraftStore(store)
 await expect(reopened.loadArtifactDraft(key)).resolves.toBe("old and new")
})

it("asks before unload only when an edit could not be captured", async () => {
 const store = durableStore()
 const page = await load(); page.setArtifactDraftStore(store)
 const key = page.artifactDraftKey("artifact-comment", "alice", "w", "page-1")!
 await page.loadArtifactDraft(key)
 store.auto = false
 void page.writeArtifactDraft(key, "fits").catch(() => {})
 const captured = new Event("beforeunload", { cancelable: true })
 window.dispatchEvent(captured)
 expect(captured.defaultPrevented).toBe(false)
 cipher.instant = false
 void page.writeArtifactDraft(key, "fits, then does not").catch(() => {})
 const refused = new Event("beforeunload", { cancelable: true })
 window.dispatchEvent(refused)
 expect(refused.defaultPrevented).toBe(true)
 expect(page.artifactDraftStorageState(key)).toBe("queued")
})
