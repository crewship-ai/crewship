import { afterEach, beforeEach, expect, it, vi } from "vitest"

// Codex #4831 round 2 B1: an ordinary write from another tab that lands while
// adoption is between reading the empty destination and writing it.
const gate = vi.hoisted(() => ({ waiting: undefined as undefined | Promise<void>, sealing: () => {} }))
vi.mock("./local-draft-seal", () => ({
 createLocalDraftCipher: async () => ({
  seal: async (text: string) => { if (text === "SLUG" && gate.waiting) { gate.sealing(); await gate.waiting } return `sealed:${text}` },
  open: async (text: string) => text.replace(/^sealed:/, ""),
  close() {},
 }),
 forgetLocalDraftPage() {}, isSealedLocalDraft: (text: string) => text.startsWith("sealed:"),
 LocalDraftReadRevoked: class extends Error {},
}))
type Row = [string, string, string, string]
const disk = new Map<string, { text: string; revision?: string }>()
const store = {
 capture: async () => [], removeCaptured: async () => {}, purge: async () => {},
 get: async (row: Row) => disk.get(JSON.stringify(row))?.text,
 read: async (row: Row) => disk.get(JSON.stringify(row)),
 put: async (row: Row, text: string, revision?: string) => { disk.set(JSON.stringify(row), { text, revision }) },
 // Like the IndexedDB transaction: check and put without an await between them.
 putIfAbsent: async (row: Row, text: string, revision: string) => { if (disk.has(JSON.stringify(row))) return false; disk.set(JSON.stringify(row), { text, revision }); return true },
 delete: async (row: Row) => { disk.delete(JSON.stringify(row)) },
}
// Web Locks shared by both "tabs": one holder per name, FIFO.
const held = new Map<string, Promise<unknown>>()
const locks = { request: (name: string, task: () => Promise<unknown>) => {
 const previous = held.get(name) ?? Promise.resolve()
 const run = previous.catch(() => {}).then(task)
 held.set(name, run.catch(() => {}))
 return run
} }
const local = new Map<string, string>()
beforeEach(() => {
 disk.clear(); local.clear(); held.clear()
 vi.stubGlobal("navigator", { ...globalThis.navigator, locks })
 vi.mocked(localStorage.getItem).mockImplementation(key => local.get(key) ?? null)
 vi.mocked(localStorage.setItem).mockImplementation((key, value) => { local.set(key, String(value)) })
 vi.mocked(localStorage.removeItem).mockImplementation(key => { local.delete(key) })
})
afterEach(() => { vi.unstubAllGlobals(); vi.mocked(localStorage.getItem).mockReset().mockReturnValue(null) })
async function tab() { vi.resetModules(); const drafts = await import("./artifact-drafts"); drafts.setArtifactDraftStore(store); return drafts }

it("never lets adoption overwrite an ordinary write another tab made during it", async () => {
 local.set("artifact-comment:owner:w:demo", "SLUG")
 const a = await tab(), b = await tab()
 const key = a.artifactDraftKey("artifact-comment", "owner", "w", "page-1")!
 await a.loadArtifactDraft(key); await b.loadArtifactDraft(key)
 const found = a.findSlugArtifactDraft("artifact-comment", "owner", "w", "demo", "page-1")!
 let release!: () => void
 gate.waiting = new Promise<void>(resolve => { release = resolve })
 const sealing = new Promise<void>(resolve => { gate.sealing = resolve })
 // A has read the empty destination and is sealing the slug text.
 const adopted = a.adoptSlugArtifactDraft(found, key)
 await sealing
 const written = b.writeArtifactDraft(key, "B typed meanwhile")
 await new Promise(resolve => setTimeout(resolve, 20))
 release(); gate.waiting = undefined
 await Promise.all([adopted, written])
 await expect(adopted).resolves.toBe("kept-existing")
 expect(local.get("artifact-comment:owner:w:demo")).toBe("SLUG")
 expect(disk.get(JSON.stringify(["owner", "artifact-comment", "w", "page-1"]))?.text).toBe("sealed:B typed meanwhile")
})
