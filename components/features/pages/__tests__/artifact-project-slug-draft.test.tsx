// G4 B16 (#4827): drafts a build before Page-ID keys (#4628) kept under the Page
// slug. The record carries no Page identity and a slug can be reused (G4 B23b):
// it is offered as an older draft, adopted under the Page ID only on an explicit
// choice, and never applied or deleted silently.
vi.mock("@/lib/local-draft-seal", () => ({
 createLocalDraftCipher: async () => ({ seal: async (text:string) => `sealed:${text}`, open: async (text:string) => text.replace(/^sealed:/,""), close() {} }),
 forgetLocalDraftPage() {}, isSealedLocalDraft: (text:string) => text.startsWith("sealed:"), LocalDraftReadRevoked: class extends Error {},
}))
import React from "react"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { setArtifactDraftStore, suspendOtherArtifactDrafts } from "@/lib/artifact-drafts"
const state = vi.hoisted(() => ({ user: "owner", data: { page_id:"page-123", can_write: true, can_comment: false, can_share: true, can_restore: true, can_send_to_agent: true, latest_version: 2, shared_version: 1, manifest: { version: 2, entrypoint: "index.html", files: [{ path: "index.html", content: "<h1>Hello</h1>", media_type: "text/html" }] }, versions: [{ version: 2 }, { version: 1 }], quota: { used_bytes: 10, limit_bytes: 100, warning: false }, comments: [] } }))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({ useArtifactAgentDispatch: () => ({ dispatch: vi.fn(), ready: false }) }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: state.user } } }) }))
vi.mock("@/hooks/use-artifact-project", () => ({ useArtifactProject: () => ({ query: { data: state.data }, restore: { mutate: vi.fn() }, share: { mutate: vi.fn() }, comment: { mutate: vi.fn() }, save: { mutate: vi.fn() } }) }))
import { ArtifactProject } from "../artifact-project"

const disk = new Map<string,string>()
const local = new Map<string,string>()
const pageRow = (human:string, kind:string, page="page-123") => JSON.stringify([human, kind, "w", page])
const oldSource = (content:string) => JSON.stringify({path:"index.html",body:{base_version:1,entrypoint:"index.html",files:[{path:"index.html",media_type:"text/html",content}]}})
let failPut = false
beforeEach(() => {
 disk.clear(); local.clear(); failPut = false
 setArtifactDraftStore({
  capture: async () => [], removeCaptured: async rows => { for (const row of rows) disk.delete(JSON.stringify(row.row)) },
  get: async key => disk.get(JSON.stringify(key)),
  put: async (key, text) => { if (failPut) throw new Error("quota"); disk.set(JSON.stringify(key), text) },
  delete: async key => { disk.delete(JSON.stringify(key)) },
  purge: async () => { disk.clear() },
 })
 vi.mocked(localStorage.getItem).mockImplementation(key => local.get(key) ?? null)
 vi.mocked(localStorage.setItem).mockImplementation((key, value) => { if (failPut) throw new Error("quota"); local.set(key, String(value)) })
 vi.mocked(localStorage.removeItem).mockImplementation(key => { local.delete(key) })
})
afterEach(() => { cleanup(); state.user = "owner"; state.data.page_id = "page-123"; setArtifactDraftStore(undefined); vi.mocked(localStorage.getItem).mockReset().mockReturnValue(null); vi.mocked(localStorage.setItem).mockReset(); vi.mocked(localStorage.removeItem).mockReset() })

it("offers a slug-keyed source draft as an older draft and adopts it under the Page ID only when chosen", async () => {
 local.set("artifact-source:owner:w:demo", oldSource("Old slug draft"))
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 const offer = await screen.findByRole("region", { name: "Older draft" })
 expect(offer).toHaveTextContent("may belong to another Page")
 // Offered, not applied: nothing is written under the Page ID and the copy stays.
 expect(disk.has(pageRow("owner","artifact-source"))).toBe(false)
 expect(local.get("artifact-source:owner:w:demo")).toBe(oldSource("Old slug draft"))
 expect(screen.queryByLabelText("Source file")).toBeNull()
 fireEvent.click(screen.getByRole("button", { name: "Use old source draft here" }))
 await waitFor(() => expect(screen.getByLabelText("Source file")).toHaveValue("Old slug draft"))
 await waitFor(() => expect(disk.get(pageRow("owner","artifact-source"))).toMatch(/^sealed:/))
 expect(JSON.parse(disk.get(pageRow("owner","artifact-source"))!.slice("sealed:".length))).toEqual(JSON.parse(oldSource("Old slug draft")))
 // The slug copy goes only after the sealed Page-ID copy is durable.
 await waitFor(() => expect(local.has("artifact-source:owner:w:demo")).toBe(false))
 expect(screen.queryByRole("region", { name: "Older draft" })).toBeNull()
})

it("adopts a slug-keyed comment draft into the empty comment only when chosen", async () => {
 local.set("artifact-comment:owner:w:demo", "Old slug comment")
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 await screen.findByRole("region", { name: "Older draft" })
 await waitFor(() => expect(screen.getByLabelText("Comment for agent")).not.toBeDisabled())
 expect(screen.getByLabelText("Comment for agent")).toHaveValue("")
 fireEvent.click(screen.getByRole("button", { name: "Use old comment here" }))
 await waitFor(() => expect(screen.getByLabelText("Comment for agent")).toHaveValue("Old slug comment"))
 await waitFor(() => expect(disk.get(pageRow("owner","artifact-comment"))).toBe("sealed:Old slug comment"))
 await waitFor(() => expect(local.has("artifact-comment:owner:w:demo")).toBe(false))
})

it("never restores a slug draft into a different Page that reuses the slug, and never deletes it unasked (G4 B23b)", async () => {
 local.set("artifact-source:owner:w:demo", oldSource("Draft of the renamed Page"))
 state.data.page_id = "other-page"
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 await screen.findByRole("region", { name: "Older draft" })
 await waitFor(() => expect(screen.getByRole("button",{name:"Edit source"})).toBeEnabled())
 expect(screen.queryByRole("button",{name:"Restore source draft"})).toBeNull()
 expect(screen.queryByLabelText("Source file")).toBeNull()
 expect(disk.has(pageRow("owner","artifact-source","other-page"))).toBe(false)
 expect(local.get("artifact-source:owner:w:demo")).toBe(oldSource("Draft of the renamed Page"))
 // An explicit discard removes exactly the copy that was shown.
 fireEvent.click(screen.getByRole("button", { name: "Discard old source draft" }))
 expect(local.has("artifact-source:owner:w:demo")).toBe(false)
 expect(disk.size).toBe(0)
})

it("keeps the slug copy when the sealed Page-ID write fails", async () => {
 local.set("artifact-comment:owner:w:demo", "Old slug comment")
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 await screen.findByRole("region", { name: "Older draft" })
 await waitFor(() => expect(screen.getByLabelText("Comment for agent")).not.toBeDisabled())
 failPut = true
 fireEvent.click(screen.getByRole("button", { name: "Use old comment here" }))
 await waitFor(() => expect(screen.getByText("Browser storage unavailable. Copy your draft before leaving.")).toBeTruthy())
 expect(local.get("artifact-comment:owner:w:demo")).toBe("Old slug comment")
})

it("never shows another account's slug draft", async () => {
 local.set("artifact-source:bob:w:demo", oldSource("Bob private draft"))
 local.set("artifact-comment:bob:w:demo", "Bob private comment")
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 await waitFor(() => expect(screen.getByRole("button",{name:"Edit source"})).toBeEnabled())
 expect(screen.queryByRole("region", { name: "Older draft" })).toBeNull()
 expect(document.body.textContent).not.toContain("Bob private")
})

it("does not offer a sealed value or the Page-ID key itself as a slug draft", async () => {
 local.set("artifact-comment:owner:w:demo", "sealed:fallback copy")
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 await waitFor(() => expect(screen.getByRole("button",{name:"Edit source"})).toBeEnabled())
 expect(screen.queryByRole("region", { name: "Older draft" })).toBeNull()
 cleanup()
 state.data.page_id = "demo"
 local.set("artifact-comment:owner:w:demo", "Plaintext under the Page ID")
 render(<ArtifactProject workspaceId="w" slug="demo" />)
 await waitFor(() => expect(screen.getByRole("button",{name:"Edit source"})).toBeEnabled())
 expect(screen.queryByRole("region", { name: "Older draft" })).toBeNull()
})

it("removes another account's slug draft on identity change (privacy rules of #4611 stay intact)", async () => {
 local.set("artifact-source:bob:w:demo", oldSource("Bob private draft"))
 local.set("artifact-source:owner:w:demo", oldSource("Owner draft"))
 const keys = vi.spyOn(Object, "keys").mockImplementation(((target: object) => target === localStorage ? [...local.keys()] : Reflect.ownKeys(target).filter((k): k is string => typeof k === "string")) as typeof Object.keys)
 try { await suspendOtherArtifactDrafts("owner") } finally { keys.mockRestore() }
 expect(local.has("artifact-source:bob:w:demo")).toBe(false)
 expect(local.get("artifact-source:owner:w:demo")).toBe(oldSource("Owner draft"))
})
