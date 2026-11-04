// @vitest-environment node
// UX walkthrough RC-4, R05: the browser went offline BEFORE an autosave left
// it, so nothing reached the server. The save ended "uncertain"; "Ověřit
// výsledek" asked the server, which answered 409 (no commit of those bytes as
// base+1). The coordinator then fenced every later save as a conflict: further
// typing was never saved and the server stayed on its version.
//
// Real client, coordinator and draft journal; only fetch and the IndexedDB
// layer under the journal are faked. The fake server mirrors
// internal/mooring/native_save_receipt.go: a save commits base+1 only while
// head == base (else 409); reconcile returns the receipt only for exactly
// those bytes at base+1, else ErrNativeUnconfirmed → 409.
import { Buffer } from "node:buffer"
import { webcrypto } from "node:crypto"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { DocumentDraft } from "../document-queue"
import { createNativeDocumentClient } from "./native-document-client"
import { openNativeDraftJournal } from "./native-draft"
import { createSingleEditorSave, type NativeSaveState } from "./single-editor-save"

const store = vi.hoisted(() => ({ rows: new Map<string, unknown>() }))
const storage = () => ({
  exclusive: true,
  load: vi.fn(async () => [...store.rows.values()]),
  put: vi.fn(async (row: { id: string }) => { store.rows.set(row.id, row) }),
  remove: vi.fn(async (id: string) => { store.rows.delete(id) }),
  close: vi.fn(),
})
vi.mock("../document-queue", async importOriginal => ({
  ...await importOriginal<typeof import("../document-queue")>(),
  openDocumentDrafts: vi.fn(async () => storage()),
  purgeDocumentDrafts: vi.fn(async () => { store.rows.clear() }),
  removeDocumentDraftRow: vi.fn(async (_h: string, _w: string, _p: string, row: DocumentDraft) => { store.rows.delete(row.id) }),
  registerDocumentDraftWriter: () => () => {},
}))
vi.mock("../protected-document-drafts", () => ({ openProtectedDocumentDrafts: vi.fn(async () => storage()) }))

beforeEach(() => { vi.stubGlobal("crypto", webcrypto); store.rows.clear() })
afterEach(() => { vi.unstubAllGlobals() })

const sha = async (bytes: Uint8Array) => "sha256:" + Buffer.from(await webcrypto.subtle.digest("SHA-256", bytes)).toString("hex")
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })

function server(initial: string) {
  const versions = new Map<number, { text: string; hash: string; editorRevision: string }>([[2, { text: initial, hash: "", editorRevision: "seed" }]])
  let head = 2, online = true
  const calls: string[] = []
  const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input), path = url.split("?")[0]
    // context.setOffline: nothing leaves the browser.
    if (!online) throw new TypeError("Failed to fetch")
    if (path.endsWith("/craft/sessions")) return json({ session_token: "b".repeat(64), session_id: "tab-a", application_id: "wordcraft", page_id: "page" })
    const op = path.endsWith("/document/save") ? "save" : path.endsWith("/document/reconcile") ? "reconcile" : undefined
    if (!op) return json({}, 404)
    calls.push(op)
    const body = JSON.parse(String(init?.body)) as Record<string, string>
    const bytes = new Uint8Array(Buffer.from(body.bytes, "base64")), base = Number(body.baseVersion)
    const receipt = (version: number) => ({ ...body, bytes: undefined, version: String(version), durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: body.contentHash })
    if (op === "save") {
      if (await sha(bytes) !== body.contentHash) return json({ error: "invalid" }, 422)
      if (base !== head) return json({ error: "native document or lease changed; keep the local draft" }, 409)
      versions.set(++head, { text: new TextDecoder().decode(bytes), hash: body.contentHash, editorRevision: body.editorRevision })
      return json(receipt(head))
    }
    const landed = versions.get(base + 1)
    if (!landed || landed.hash !== body.contentHash || landed.editorRevision !== body.editorRevision) return json({ error: "native document or lease changed; keep the local draft" }, 409)
    return json(receipt(base + 1))
  })
  const commitOther = async (text: string) => { versions.set(++head, { text, hash: await sha(new TextEncoder().encode(text)), editorRevision: "other" }) }
  return { fetch, calls, commitOther, setOffline: (value: boolean) => { online = !value }, head: () => ({ version: head, text: versions.get(head)!.text }) }
}

async function editor() {
  const remote = server("Pred vypadkem")
  const client = createNativeDocumentClient({ workspaceId: "ws", pageId: "page", fetch: remote.fetch as unknown as typeof fetch })
  await client.open(new AbortController().signal)
  const journal = await openNativeDraftJournal({ human: "h", workspace: "ws", page: "page", applicationId: "wordcraft" })
  let text = "Pred vypadkem", revision = 0
  const states: NativeSaveState[] = []
  const controller = createSingleEditorSave({
    workspaceId: "ws", pageId: "page", applicationId: "wordcraft", enginePin: "e".repeat(64), baseVersion: "2", generation: "1", sessionId: "tab-a",
    loaded: { bytes: new TextEncoder().encode(text) }, journal, idleMs: 5, timeoutMs: 2_000,
    engine: { snapshot: async () => ({ bytes: new TextEncoder().encode(text), editorRevision: `doc:${revision}` }), validate: async () => {} },
    transport: client,
    onState: state => { states.push(state) },
  })
  const type = (more: string) => { text += more; revision++; controller.edited() }
  const last = () => states.at(-1)!
  return { remote, controller, journal, type, last, states }
}

it("resumes saving after an outage in which the uncertain save never reached the server", async () => {
  const e = await editor()
  e.remote.setOffline(true)
  e.type(" behem vypadku")
  await vi.waitFor(() => expect(e.last()).toMatchObject({ phase: "uncertain" }))
  expect(e.remote.calls).toEqual([])

  e.remote.setOffline(false)
  // "Ověřit výsledek": the server answers that nothing landed as version 3.
  await e.controller.reconcile()
  expect(e.remote.calls).toEqual(["reconcile"])
  // The pipeline resumes with a fresh capture of the current bytes.
  await vi.waitFor(() => expect(e.last()).toMatchObject({ phase: "saved", dirty: false }))
  expect(e.remote.head()).toEqual({ version: 3, text: "Pred vypadkem behem vypadku" })

  // Typing after the network returned is saved too, never silently dropped.
  e.type(" po navratu")
  await vi.waitFor(() => expect(e.remote.head()).toEqual({ version: 4, text: "Pred vypadkem behem vypadku po navratu" }))
  await vi.waitFor(() => expect(e.last()).toMatchObject({ phase: "saved", dirty: false }))
  expect(e.remote.calls).toEqual(["reconcile", "save", "save"])
  // The recovery row is retired only by the receipt.
  expect(store.rows.size).toBe(0)
  await e.controller.close()
})

it("never sends a save while the reconcile answer is still unknown, and stays uncertain on a failed check", async () => {
  const e = await editor()
  e.remote.setOffline(true)
  e.type(" behem vypadku")
  await vi.waitFor(() => expect(e.last()).toMatchObject({ phase: "uncertain" }))
  // Still offline: the check itself cannot reach the server.
  await e.controller.reconcile()
  expect(e.last()).toMatchObject({ phase: "uncertain" })
  e.type(" dal")
  await new Promise(resolve => setTimeout(resolve, 50))
  expect(e.last()).toMatchObject({ phase: "uncertain" })
  expect(e.remote.calls).toEqual([])
  e.remote.setOffline(false)
  await e.controller.reconcile()
  await vi.waitFor(() => expect(e.remote.head()).toEqual({ version: 3, text: "Pred vypadkem behem vypadku dal" }))
  expect(e.remote.calls).toEqual(["reconcile", "save"])
  await e.controller.close()
})

it("a save that landed while its reply was lost is proven by reconcile, not saved again", async () => {
  const e = await editor()
  const original = e.remote.fetch.getMockImplementation()!
  e.remote.fetch.mockImplementationOnce(async (input, init) => { await original(input, init); throw new TypeError("Failed to fetch") })
  e.type(" behem vypadku")
  await vi.waitFor(() => expect(e.last()).toMatchObject({ phase: "uncertain" }))
  await e.controller.reconcile()
  await vi.waitFor(() => expect(e.last()).toMatchObject({ phase: "saved", dirty: false }))
  expect(e.remote.head()).toEqual({ version: 3, text: "Pred vypadkem behem vypadku" })
  expect(e.remote.calls).toEqual(["save", "reconcile"])
  await e.controller.close()
})

it("a head that moved on is still a visible conflict after reconcile, with the draft kept", async () => {
  const e = await editor()
  e.remote.setOffline(true)
  e.type(" behem vypadku")
  await vi.waitFor(() => expect(e.last()).toMatchObject({ phase: "uncertain" }))
  e.remote.setOffline(false)
  // Someone else committed version 3 in the meantime (e.g. an accepted proposal).
  await e.remote.commitOther("jiny")
  await e.controller.reconcile()
  await vi.waitFor(() => expect(e.last()).toMatchObject({ phase: "draft", reason: "conflict" }))
  expect(e.remote.head()).toEqual({ version: 3, text: "jiny" })
  // The unsaved draft stays in the browser for compare/copy.
  expect(store.rows.size).toBe(1)
  await e.controller.close()
})
