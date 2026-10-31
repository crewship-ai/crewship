/** NoteCraft over the real Mooring SDK. Only the server and the journal are
 * in-memory doubles (./fake-server.ts) that apply the server's fences. */
import { webcrypto } from "node:crypto"
import { fireEvent, within } from "@testing-library/dom"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { RecoveryDraft } from "../../../lib/mooring/sdk"
import { createFakeCraftServer, memoryJournal } from "./fake-server"
import { mountNoteCraft } from "./notecraft"

beforeEach(() => { vi.stubGlobal("crypto", webcrypto); document.body.innerHTML = "" })
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })
const alice = { actor_id: "u-alice", display_name: "Alice", kind: "human" as const }
const bob = { actor_id: "u-bob", display_name: "Bob", kind: "human" as const }
let tabs = 0

function open(server: ReturnType<typeof createFakeCraftServer>, actor: typeof alice, rows = new Map<string, RecoveryDraft>(), mayEdit = true) {
  const root = document.createElement("div")
  document.body.append(root)
  const client = server.connect(actor)
  const app = mountNoteCraft(root, { scope: { workspaceId: "w", pageId: "p", sessionId: `tab-${++tabs}`, enginePin: "pin" }, mayEdit, document: client, versions: client.versions, openJournal: async () => memoryJournal(rows) })
  const q = within(root)
  const note = q.getByLabelText<HTMLTextAreaElement>("Poznámka")
  return {
    app, q, client, note,
    lease: () => q.getByRole("toolbar").querySelector("[data-lease]")!.textContent,
    status: () => q.getByRole("status").textContent,
    type(text: string) { note.value = text; fireEvent.input(note) },
    click: (name: string | RegExp) => fireEvent.click(q.getByRole("button", { name })),
  }
}
async function editing(tab: ReturnType<typeof open>, name = "Alice") {
  await tab.app.ready
  tab.click("Převzít úpravy")
  await vi.waitFor(() => { expect(tab.lease()).toBe(`Upravuje ${name}`); expect(tab.note.readOnly).toBe(false) })
}
async function saved(tab: ReturnType<typeof open>, text: string) {
  tab.type(text)
  await vi.waitFor(() => expect(tab.status()).toBe("Uloženo"))
}

describe("NoteCraft reference application", () => {
  it("shows who edits and reports Saved only after the server confirmed", async () => {
    const server = createFakeCraftServer("ahoj")
    const a = open(server, alice)
    await a.app.ready
    expect(a.lease()).toBe("Volné")
    expect(a.note).toMatchObject({ value: "ahoj", readOnly: true })
    expect(a.status()).toBe("Pouze čtení")
    await editing(a)
    a.type("ahoj světe")
    expect(a.status()).toBe("Neuložený koncept")
    await vi.waitFor(() => expect(a.status()).toBe("Uloženo"))
    expect(server.text()).toBe("ahoj světe")
    expect(server.head).toBe("2")
  })

  it("requests a hand-off, saves the holder's last words, then moves custody", async () => {
    const server = createFakeCraftServer("")
    const a = open(server, alice)
    await editing(a)
    a.type("Alice final")
    const b = open(server, bob)
    await b.app.ready
    expect(b.lease()).toBe("Upravuje Alice")
    expect(b.q.queryByRole("button", { name: "Převzít úpravy" })).toBeNull()
    b.click("Požádat o předání")
    await vi.waitFor(async () => { await a.app.refresh(); expect(a.q.getByRole("button", { name: "Předat uživateli Bob" })).toBeTruthy() })
    expect(server.generation).toBe("1")
    a.click("Předat uživateli Bob")
    await vi.waitFor(() => expect(a.lease()).toBe("Upravuje Bob"))
    expect(server.text()).toBe("Alice final")
    expect(server.generation).toBe("2")
    expect(a.note.readOnly).toBe(true)
    expect(a.status()).toBe("Pouze čtení")
    await b.app.refresh()
    await vi.waitFor(() => expect(b.note).toMatchObject({ value: "Alice final", readOnly: false }))
    await saved(b, "Bob now")
    expect(server.text()).toBe("Bob now")
  })

  it("refuses a write at a stale generation and keeps the draft for compare/copy only", async () => {
    const server = createFakeCraftServer("start")
    const a = open(server, alice)
    await editing(a)
    server.takeOver(bob) // Alice's lease expired and Bob took custody
    a.type("Alice late")
    await vi.waitFor(() => expect(a.status()).toBe("Koncept zůstává v tomto editoru"))
    expect(server.head).toBe("1")
    await a.app.refresh()
    await vi.waitFor(() => expect(a.lease()).toBe("Upravuje Bob"))
    expect(a.note).toMatchObject({ value: "start", readOnly: true })
    const recovery = within(a.q.getByRole("heading", { name: "Místní koncepty" }).parentElement!)
    expect(recovery.getByText(/Váš neuložený koncept nad verzí 1/)).toBeTruthy()
    expect(recovery.queryByRole("button", { name: /uložit|obnovit/i })).toBeNull()
    fireEvent.click(recovery.getByRole("button", { name: "Porovnat" }))
    await vi.waitFor(() => expect(a.q.getByText("Liší se od serverové verze 1")).toBeTruthy())
    fireEvent.click(recovery.getByRole("button", { name: "Kopírovat" }))
    expect(a.q.getByLabelText<HTMLTextAreaElement>("Kopie konceptu").value).toBe("Alice late")
    await new Promise(resolve => setTimeout(resolve, 700))
    expect(server.head).toBe("1")
    expect(server.text()).toBe("start")
  })

  it("offers a crashed tab's unconfirmed draft after reload and never saves it", async () => {
    const server = createFakeCraftServer("server text"), rows = new Map<string, RecoveryDraft>()
    const crashed = open(server, alice, rows)
    await editing(crashed)
    vi.spyOn(crashed.client, "save").mockRejectedValue(new Error("network"))
    crashed.type("unsaved words")
    await vi.waitFor(() => expect(crashed.status()).toBe("Výsledek nepotvrzen"))
    expect(crashed.q.getByRole("button", { name: "Ověřit výsledek" }).hidden).toBe(false)
    // The tab dies without closing; a new tab of the same person opens.
    const reopened = open(server, alice, rows)
    await reopened.app.ready
    await vi.waitFor(() => expect(reopened.note.readOnly).toBe(false))
    expect(reopened.note.value).toBe("server text")
    expect(reopened.q.getByText(/Místní koncept nad verzí 1/)).toBeTruthy()
    reopened.click("Porovnat")
    await vi.waitFor(() => expect(reopened.q.getByText("Liší se od serverové verze 1")).toBeTruthy())
    reopened.click("Kopírovat")
    expect(reopened.q.getByLabelText<HTMLTextAreaElement>("Kopie konceptu").value).toBe("unsaved words")
    await new Promise(resolve => setTimeout(resolve, 700))
    expect(server.head).toBe("1")
  })

  it("lists versions and restores as a new version", async () => {
    const server = createFakeCraftServer("first")
    const a = open(server, alice)
    await editing(a)
    await saved(a, "second")
    await saved(a, "third")
    await a.app.refresh()
    await vi.waitFor(() => expect(a.q.getByText("Verze 3 · aktuální")).toBeTruthy())
    a.click("Obnovit verzi 1")
    await vi.waitFor(() => expect(a.q.getByText("Verze 4 · aktuální · obnoveno z 1")).toBeTruthy())
    expect(server.text()).toBe("first")
    expect(server.text("3")).toBe("third")
    expect(a.note).toMatchObject({ value: "first", readOnly: false })
    await saved(a, "after restore")
    expect(server.head).toBe("5")
  })

  it("stays read-only without an edit right", async () => {
    const server = createFakeCraftServer("x")
    const viewer = open(server, bob, new Map(), false)
    await viewer.app.ready
    expect(viewer.lease()).toBe("Volné · Pouze čtení")
    expect(viewer.q.queryAllByRole("button", { name: /Převzít|Požádat|Uvolnit|Předat|Obnovit/ })).toHaveLength(0)
    expect(viewer.note.readOnly).toBe(true)
  })

  it("never loses text typed while a restore is pending", async () => {
    const server = createFakeCraftServer("first")
    const a = open(server, alice)
    await editing(a)
    await saved(a, "second")
    await a.app.refresh()
    await vi.waitFor(() => expect(a.q.getByRole("button", { name: "Obnovit verzi 1" })).toBeTruthy())
    let release!: () => void
    const gate = new Promise<void>(resolve => { release = resolve })
    const restore = a.client.versions.restore
    const delayed = vi.spyOn(a.client.versions, "restore").mockImplementation(async (...args) => { await gate; return restore(...args) })
    a.click("Obnovit verzi 1")
    await vi.waitFor(() => expect(delayed).toHaveBeenCalled())
    expect(a.note.readOnly).toBe(true)
    a.type("typed during restore") // e.g. a programmatic write past readonly
    release()
    await vi.waitFor(() => expect(a.q.getByText("Verze 3 · aktuální · obnoveno z 1")).toBeTruthy())
    expect(a.q.getByRole("alert").hidden).toBe(false)
    a.click("Kopírovat")
    expect(a.q.getByLabelText<HTMLTextAreaElement>("Kopie konceptu").value).toBe("typed during restore")
    expect(server.text()).toBe("first")
  })

  it("restores a working editor when a hand-off fails after local revocation", async () => {
    const server = createFakeCraftServer("")
    const a = open(server, alice)
    await editing(a)
    const b = open(server, bob)
    await b.app.ready
    b.click("Požádat o předání")
    await vi.waitFor(async () => { await a.app.refresh(); expect(a.q.getByRole("button", { name: "Předat uživateli Bob" })).toBeTruthy() })
    vi.spyOn(a.client, "leaseAction").mockRejectedValueOnce(new Error("network"))
    a.click("Předat uživateli Bob")
    await vi.waitFor(() => expect(a.q.getByRole("alert").hidden).toBe(false))
    expect(server.generation).toBe("1")
    expect(a.lease()).toBe("Upravuje Alice")
    await vi.waitFor(() => expect(a.note.readOnly).toBe(false))
    await saved(a, "after failure")
    expect(server.text()).toBe("after failure")
  })

  it("keeps an offered draft from lost custody after reacquiring and saving other text", async () => {
    const server = createFakeCraftServer("start"), rows = new Map<string, RecoveryDraft>()
    const a = open(server, alice, rows)
    await editing(a)
    server.takeOver(bob)
    a.type("Alice late")
    await vi.waitFor(() => expect(a.status()).toBe("Koncept zůstává v tomto editoru"))
    await a.app.refresh()
    await vi.waitFor(() => expect(a.lease()).toBe("Upravuje Bob"))
    await server.connect(bob).leaseAction("release", { generation: server.generation }, new AbortController().signal)
    await a.app.refresh()
    await vi.waitFor(() => expect(a.lease()).toBe("Volné"))
    await editing(a)
    await saved(a, "Alice again")
    expect(server.text()).toBe("Alice again")
    const reloaded = open(server, alice, rows)
    await reloaded.app.ready
    await vi.waitFor(() => expect(reloaded.q.getByText(/Místní koncept nad verzí 1/)).toBeTruthy())
    reloaded.click("Kopírovat")
    expect(reloaded.q.getByLabelText<HTMLTextAreaElement>("Kopie konceptu").value).toBe("Alice late")
  })
})
