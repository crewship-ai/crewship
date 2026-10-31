import { afterEach, beforeEach, expect, it, vi } from "vitest"

// Lane/storage tests use an identity cipher; actual cryptography and admission
// are exercised in local-draft-seal and the real-browser retention tests.
vi.mock("./local-draft-seal", () => ({
 // The real cipher asks /pages/@<page>/draft-key, which accepts only this Page identity.
 createLocalDraftCipher: async (scope: { page: string }) => { if (!/^[A-Za-z0-9_-]{1,128}$/.test(scope.page)) throw new Error("draft-key 400: immutable Page reference required"); return { seal: async (text: string) => text, open: async (text: string) => text, close() {} } },
 forgetLocalDraftPage() {}, isSealedLocalDraft: () => true,
 LocalDraftReadRevoked: class extends Error {},
}))
type Drafts = typeof import("./artifact-drafts")
type Row = [string, string, string, string]

/** Durable map that only changes when a write is explicitly flushed, like a disk. */
function durableStore() {
  const disk = new Map<string, string>()
  const pending: (() => void)[] = []
  const store = {
    disk,
    capture: vi.fn(async () => []),
    removeCaptured: vi.fn(async () => {}),
    auto: true,
    puts: [] as string[],
    flush() { for (const done of pending.splice(0)) done() },
    get: vi.fn(async (row: Row) => disk.get(JSON.stringify(row))),
    put: vi.fn((row: Row, text: string) => {
      store.puts.push(text)
      return new Promise<void>(resolve => {
        const done = () => { disk.set(JSON.stringify(row), text); resolve() }
        if (store.auto) done(); else pending.push(done)
      })
    }),
    delete: vi.fn(async (row: Row) => { disk.delete(JSON.stringify(row)) }),
    purge: vi.fn(async (keep: string | null) => { for (const key of [...disk.keys()]) if (keep === null || JSON.parse(key)[0] !== keep) disk.delete(key) }),
  }
  return store
}

const local = new Map<string, string>()
async function load(): Promise<Drafts> { vi.resetModules(); return import("./artifact-drafts") }
beforeEach(() => {
  local.clear()
  vi.mocked(localStorage.getItem).mockImplementation(key => local.get(key) ?? null)
  vi.mocked(localStorage.setItem).mockImplementation((key, value) => { local.set(key, value) })
  vi.mocked(localStorage.removeItem).mockImplementation(key => { local.delete(key) })
})
afterEach(() => { vi.unstubAllGlobals(); vi.mocked(localStorage.getItem).mockReset().mockReturnValue(null) })

it("resolves a write only once the durable store has it, so a crash after the await keeps the draft", async () => {
  const store = durableStore(); store.auto = false
  const drafts = await load(); drafts.setArtifactDraftStore(store)
  const key = drafts.artifactDraftKey("artifact-source", "alice", "w", "demo")!
  let done = false
  const written = drafts.writeArtifactDraft(key, "typed").then(() => { done = true })
  await vi.waitFor(() => expect(store.put).toHaveBeenCalled())
  expect(done).toBe(false)
  expect(store.disk.size).toBe(0)
  store.flush(); await written
  // Crash: a fresh module instance has only what reached the durable store.
  const reloaded = await load(); reloaded.setArtifactDraftStore(store)
  await expect(reloaded.readArtifactDraft(key)).resolves.toBe("typed")
  expect(local.size).toBe(0)
})

it("coalesces typing behind the running write and keeps the latest text", async () => {
  const store = durableStore(); store.auto = false
  const drafts = await load(); drafts.setArtifactDraftStore(store)
  const key = drafts.artifactDraftKey("artifact-comment", "alice", "w", "demo")!
  const first = drafts.writeArtifactDraft(key, "a")
  await vi.waitFor(() => expect(store.put).toHaveBeenCalledTimes(1))
  const later = [drafts.writeArtifactDraft(key, "ab"), drafts.writeArtifactDraft(key, "abc")]
  store.flush(); await first
  await vi.waitFor(() => expect(store.put).toHaveBeenCalledTimes(2))
  store.flush(); await Promise.all(later)
  expect(store.puts).toEqual(["a", "abc"])
  await expect(drafts.readArtifactDraft(key)).resolves.toBe("abc")
  await drafts.writeArtifactDraft(key, "")
  expect(store.disk.size).toBe(0)
})

it("moves a draft written by an older build out of localStorage once", async () => {
  const store = durableStore()
  const drafts = await load(); drafts.setArtifactDraftStore(store)
  local.set("artifact-source:alice:w:demo", "{\"legacy\":true}")
  const key = drafts.artifactDraftKey("artifact-source", "alice", "w", "demo")!
  await expect(drafts.readArtifactDraft(key)).resolves.toBe("{\"legacy\":true}")
  await vi.waitFor(() => expect(local.has("artifact-source:alice:w:demo")).toBe(false))
  expect(store.disk.get(JSON.stringify(["alice", "artifact-source", "w", "demo"]))).toBe("{\"legacy\":true}")
  await expect(drafts.readArtifactDraft(key)).resolves.toBe("{\"legacy\":true}")
})

it("never offers one person's draft to another", async () => {
  const store = durableStore()
  const drafts = await load(); drafts.setArtifactDraftStore(store)
  await drafts.writeArtifactDraft(drafts.artifactDraftKey("artifact-viewer-comment", "alice", "w", "p1")!, "Alice private")
  local.set("artifact-viewer-comment:alice:w:p1", "Alice legacy")
  await expect(drafts.readArtifactDraft(drafts.artifactDraftKey("artifact-viewer-comment", "bob", "w", "p1")!)).resolves.toBe("")
  expect(drafts.artifactDraftKey("artifact-source", undefined, "w", "demo")).toBeNull()
})

it("deletes every draft on sign-out and other people's drafts on an identity change, including localStorage", async () => {
  const store = durableStore()
  const drafts = await load(); drafts.setArtifactDraftStore(store)
  const alice = drafts.artifactDraftKey("artifact-source", "alice", "w", "demo")!
  const bob = drafts.artifactDraftKey("artifact-comment", "bob", "w", "demo")!
  await drafts.writeArtifactDraft(alice, "Alice source")
  await drafts.writeArtifactDraft(bob, "Bob comment")
  local.set("artifact-comment:bob:w:x", "Bob legacy"); local.set("artifact-source:alice:w:x", "Alice legacy"); local.set("unrelated", "kept")
  const localKeys = vi.spyOn(Object, "keys").mockImplementation(((target: object) => target === localStorage ? [...local.keys()] : Reflect.ownKeys(target).filter((k): k is string => typeof k === "string")) as typeof Object.keys)
  try {
    await drafts.purgeOtherArtifactDrafts("alice")
    await expect(drafts.readArtifactDraft(bob)).resolves.toBe("")
    await expect(drafts.readArtifactDraft(alice)).resolves.toBe("Alice source")
    expect([...local.keys()].sort()).toEqual(["artifact-source:alice:w:x", "unrelated"])
    await drafts.purgeOtherArtifactDrafts(null)
    await expect(drafts.readArtifactDraft(alice)).resolves.toBe("")
    expect([...local.keys()]).toEqual(["unrelated"])
  } finally { localKeys.mockRestore() }
})

it("waits for a pending write before purging so it cannot recreate the draft", async () => {
  const store = durableStore(); store.auto = false
  const drafts = await load(); drafts.setArtifactDraftStore(store)
  const key = drafts.artifactDraftKey("artifact-source", "alice", "w", "demo")!
  const written = drafts.writeArtifactDraft(key, "late")
  await vi.waitFor(() => expect(store.put).toHaveBeenCalled())
  const purged = drafts.purgeOtherArtifactDrafts(null)
  await Promise.resolve()
  expect(store.purge).not.toHaveBeenCalled()
  store.flush(); await written; await purged
  expect(store.disk.size).toBe(0)
})

it("falls back to localStorage when IndexedDB is unavailable, and reports a refused write", async () => {
  const drafts = await load()
  const key = drafts.artifactDraftKey("artifact-comment", "alice", "w", "demo")!
  await drafts.writeArtifactDraft(key, "fallback")
  expect(local.get("artifact-comment:alice:w:demo")).toBe("fallback")
  await expect(drafts.readArtifactDraft(key)).resolves.toBe("fallback")
  vi.mocked(localStorage.setItem).mockImplementation(() => { throw new Error("QuotaExceededError") })
  await expect(drafts.writeArtifactDraft(key, "too much")).rejects.toThrow()
})

/** Minimal IndexedDB: transactions complete only when flushed, and record their options. */
function fakeIndexedDB() {
  const rows = new Map<string, { key: Row; text: string }>()
  const transactions: { mode: string; options: unknown; complete: () => void }[] = []
  const request = <T>(result?: T) => ({ result } as { result: T | undefined; onsuccess?: () => void })
  const db = {
    createObjectStore: vi.fn(),
    close: vi.fn(),
    transaction(_name: string, mode: string, options?: unknown) {
      const staged: (() => void)[] = []
      const tx = {
        oncomplete: null as null | (() => void), onabort: null as null | (() => void), onerror: null as null | (() => void),
        abort: vi.fn(),
        objectStore: () => ({
          get: (key: Row) => request(rows.get(JSON.stringify(key))),
          put: (row: { key: Row; text: string }) => { staged.push(() => rows.set(JSON.stringify(row.key), row)); return request() },
          delete: (key: Row) => { staged.push(() => rows.delete(JSON.stringify(key))); return request() },
          clear: () => { staged.push(() => rows.clear()); return request() },
          openCursor: () => request(),
        }),
      }
      const entry = { mode, options, complete: () => { for (const apply of staged) apply(); tx.oncomplete?.() } }
      transactions.push(entry)
      if (mode === "readonly") queueMicrotask(entry.complete)
      return tx
    },
  }
  const factory = {
    open: vi.fn(() => {
      const opening = { result: db, onupgradeneeded: null as null | (() => void), onsuccess: null as null | (() => void), onerror: null, onblocked: null }
      queueMicrotask(() => { opening.onupgradeneeded?.(); opening.onsuccess?.() })
      return opening
    }),
  }
  return { factory, rows, transactions }
}

it("uses strict-durability IndexedDB transactions and resolves only on their completion", async () => {
  const idb = fakeIndexedDB()
  vi.stubGlobal("indexedDB", idb.factory)
  const drafts = await load()
  const key = drafts.artifactDraftKey("artifact-source", "alice", "w", "demo")!
  let done = false
  const written = drafts.writeArtifactDraft(key, "strict").then(() => { done = true })
  await vi.waitFor(() => expect(idb.transactions.some(t => t.mode === "readwrite")).toBe(true))
  const write = idb.transactions.find(t => t.mode === "readwrite")!
  expect(write.options).toEqual({ durability: "strict" })
  await Promise.resolve()
  expect(done).toBe(false)
  write.complete(); await written
  expect(idb.rows.get(JSON.stringify(["alice", "artifact-source", "w", "demo"]))?.text).toBe("strict")
  expect(idb.factory.open).toHaveBeenCalledWith("crewship-artifact-drafts", 1)
})

it("rejects a failed durable read instead of reporting an empty draft, and keeps the stored copy", async () => {
  const store = durableStore()
  const drafts = await load(); drafts.setArtifactDraftStore(store)
  const key = drafts.artifactDraftKey("artifact-comment", "alice", "w", "demo")!
  await drafts.writeArtifactDraft(key, "Unsent comment")
  store.get.mockRejectedValueOnce(new Error("open timed out"))
  await expect(drafts.loadArtifactDraft(key)).rejects.toThrow()
  expect(store.disk.get(JSON.stringify(["alice", "artifact-comment", "w", "demo"]))).toBe("Unsent comment")
  await expect(drafts.loadArtifactDraft(key)).resolves.toBe("Unsent comment")
})

it("drains accepted queued writes on expiry and refuses later writes while retaining recovery rows", async () => {
  const store = durableStore(); store.auto = false
  const drafts = await load(); drafts.setArtifactDraftStore(store)
  const key = drafts.artifactDraftKey("artifact-source", "alice", "w", "demo")!
  const first = drafts.writeArtifactDraft(key, "a")
  await vi.waitFor(() => expect(store.put).toHaveBeenCalledTimes(1))
  // Queued behind the running write when sign-out starts.
  const queued = drafts.writeArtifactDraft(key, "ab")
  const purged = drafts.suspendOtherArtifactDrafts(null)
  store.flush(); await first
  await vi.waitFor(() => expect(store.put).toHaveBeenCalledTimes(2))
  store.flush(); await queued; await purged
  expect(store.puts).toEqual(["a", "ab"])
  // A keystroke that arrives after sign-out (another tab, a stale effect).
  await expect(drafts.writeArtifactDraft(key, "late")).rejects.toThrow()
  expect([...store.disk.values()]).toEqual(["ab"])
  await expect(drafts.loadArtifactDraft(key)).resolves.toBe("")
  // The next person to sign in writes normally; the previous one stays retired.
  await drafts.suspendOtherArtifactDrafts("bob")
  store.auto = true
  await drafts.writeArtifactDraft(drafts.artifactDraftKey("artifact-source", "bob", "w", "demo")!, "Bob")
  await expect(drafts.writeArtifactDraft(key, "Alice again")).rejects.toThrow()
  expect([...store.disk.values()]).toEqual(["ab", "Bob"])
})

it("serialises identity purges so an older sign-out cannot delete the next person's draft", async () => {
  const store = durableStore()
  let finishClear!: () => void
  const clearing = new Promise<void>(resolve => { finishClear = resolve })
  store.purge.mockImplementationOnce(async () => { await clearing; store.disk.clear() })
  const drafts = await load(); drafts.setArtifactDraftStore(store)
  const signedOut = drafts.purgeOtherArtifactDrafts(null)
  const signedIn = drafts.purgeOtherArtifactDrafts("bob")
  const bob = drafts.artifactDraftKey("artifact-comment", "bob", "w", "demo")!
  const written = drafts.writeArtifactDraft(bob, "Bob comment")
  finishClear(); await signedOut; await signedIn; await written
  await expect(drafts.loadArtifactDraft(bob)).resolves.toBe("Bob comment")
})

it("also deletes Design canvas and Design comment drafts from localStorage on sign-out and identity change", async () => {
  const drafts = await load(); drafts.setArtifactDraftStore(durableStore())
  for (const name of ["crewship-design:v2:alice:w:p", "design-comment:v2:alice:w:p", "crewship-design:alice:w:slug", "crewship-design:v2:bob:w:p", "design-comment:v2:bob:w:p", "unrelated"]) local.set(name, "x")
  const localKeys = vi.spyOn(Object, "keys").mockImplementation(((target: object) => target === localStorage ? [...local.keys()] : Reflect.ownKeys(target).filter((k): k is string => typeof k === "string")) as typeof Object.keys)
  try {
    await drafts.purgeOtherArtifactDrafts("bob")
    expect([...local.keys()].sort()).toEqual(["crewship-design:v2:bob:w:p", "design-comment:v2:bob:w:p", "unrelated"])
    await drafts.purgeOtherArtifactDrafts(null)
    expect([...local.keys()]).toEqual(["unrelated"])
  } finally { localKeys.mockRestore() }
})

it("tolerates unreachable localStorage: sign-out and settle still complete, capture reports nothing", async () => {
  const store = durableStore()
  const drafts = await load(); drafts.setArtifactDraftStore(store)
  const blocked = vi.spyOn(Object, "keys").mockImplementation(((target: object) => {
    if (target === localStorage) throw new DOMException("blocked site storage", "SecurityError")
    return Reflect.ownKeys(target).filter((k): k is string => typeof k === "string")
  }) as typeof Object.keys)
  try {
    expect(drafts.captureLegacyLocalDrafts()).toEqual([])
    await expect(drafts.suspendOtherArtifactDrafts(null)).resolves.toBeUndefined()
    await expect(drafts.suspendOtherArtifactDrafts("bob")).resolves.toBeUndefined()
    expect(() => drafts.removeCapturedLegacyLocalDrafts([{ key: "crewship-design:alice", text: "x" }])).not.toThrow()
  } finally { blocked.mockRestore() }
})

it("capture tolerates a localStorage getter that throws", async () => {
  const drafts = await load()
  const original = Object.getOwnPropertyDescriptor(globalThis, "localStorage")
  Object.defineProperty(globalThis, "localStorage", { configurable: true, get() { throw new DOMException("blocked site storage", "SecurityError") } })
  try {
    expect(drafts.captureLegacyLocalDrafts()).toEqual([])
    expect(() => drafts.removeCapturedLegacyLocalDrafts([{ key: "crewship-design:alice", text: "x" }])).not.toThrow()
  } finally { if (original) Object.defineProperty(globalThis, "localStorage", original) }
})
