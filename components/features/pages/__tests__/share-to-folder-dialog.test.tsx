import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { ShareToFolderDialog, type ShareSubject } from "../share-to-folder-dialog"

const request = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeChannelSafe: vi.fn(), useRealtimeEventSafe: vi.fn(), useRealtimeStatusSafe: () => "disconnected" }))

const baseFolder = { slug: "ops", name: "Ops", owner: "crew/ops", shared: "workspace", can_accept_pages: true, acl_version: 3, page_count: 1 }
let folder = baseFolder
let acl: Response | (() => Response | Promise<Response>) = () => Response.json({ error: "managers only" }, { status: 403 })
const moves: { id: string; body: Record<string, unknown> }[] = []
let failing: Record<string, number> = {}
let hold: Promise<void> | null = null
beforeEach(() => {
  moves.length = 0
  failing = {}
  hold = null
  folder = baseFolder
  acl = () => Response.json({ error: "managers only" }, { status: 403 })
  request.mockReset()
  request.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      const id = decodeURIComponent(url.split("/pages/")[1].split("/move")[0])
      moves.push({ id, body: JSON.parse(String(init.body)) })
      if (hold) await hold
      return failing[id] ? Response.json({ error: "nope" }, { status: failing[id] }) : Response.json({ page: { id } })
    }
    if (/\/acl/.test(url)) return typeof acl === "function" ? acl() : acl
    return Response.json([folder])
  })
})
afterEach(cleanup)

const subject = (id: string, extra: Partial<ShareSubject> = {}): ShareSubject => ({ id, slug: `s-${id}`, name: id.toUpperCase(), pagesVersion: 1, ...extra })
function mount(subjects: ShareSubject[], onOpenChange = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const ui = () => <QueryClientProvider client={client}><ShareToFolderDialog workspaceId="w" open onOpenChange={onOpenChange} subjects={subjects} /></QueryClientProvider>
  return { ...render(ui()), onOpenChange }
}
const block = () => document.querySelector("[data-slot=share-consequence]") as HTMLElement | null
const confirmButton = () => screen.getByRole("button", { name: "Move and share" }) as HTMLButtonElement
async function choose() {
  fireEvent.click(await screen.findByRole("radio", { name: /Ops/ }))
}

it("shows who gains content and history, and the same ID, before anything is sent", async () => {
  mount([subject("plan", { versions: 4 })])
  await choose()
  await waitFor(() => expect(block()?.textContent).toMatch(/everyone in this workspace/))
  expect(block()!.textContent).toMatch(/4 versions/)
  expect(block()!.textContent).toMatch(/same ID/)
  expect(block()!.textContent).toMatch(/Duplicate/)
  expect(moves).toEqual([])
})

it("keeps confirm disabled before a folder is chosen and while the ACL is loading", async () => {
  acl = () => new Promise<Response>(() => {})
  mount([subject("plan")])
  await screen.findByRole("radio", { name: /Ops/ })
  expect(confirmButton().disabled).toBe(true)
  fireEvent.click(screen.getByRole("radio", { name: /Ops/ }))
  expect(await screen.findByText("Reading who will see it…")).toBeTruthy()
  expect(confirmButton().disabled).toBe(true)
})

it("names the real viewers when the ACL is readable and sends that ACL version", async () => {
  acl = () => Response.json({ acl: [
    { subject_type: "crew", subject_id: "support", label: "Support", can_write: false },
    { subject_type: "user", subject_id: "u1", label: "ada@example.com", can_write: true },
  ], acl_version: 3 })
  mount([subject("plan")])
  await choose()
  await waitFor(() => expect(block()?.textContent).toMatch(/crew Support/))
  expect(block()!.textContent).toMatch(/ada@example.com can edit/)
  fireEvent.click(confirmButton())
  await waitFor(() => expect(moves).toHaveLength(1))
  expect(moves[0]).toEqual({ id: "plan", body: { target_folder: "ops", pages_version: 1, acl_version: 3 } })
})

it("blocks confirm while the ACL version read differs from the folder list's", async () => {
  acl = () => Response.json({ acl: [{ subject_type: "workspace", subject_id: "", label: "x", can_write: false }], acl_version: 4 })
  mount([subject("plan")])
  await choose()
  await screen.findByText(/permissions changed/)
  expect(confirmButton().disabled).toBe(true)
  expect(moves).toEqual([])
})

it("says nobody gains access when the folder adds no sharing", async () => {
  folder = { ...baseFolder, shared: "none" }
  mount([subject("plan")])
  await choose()
  await waitFor(() => expect(block()?.textContent).toMatch(/Nobody gains access/))
  expect(block()!.textContent).not.toMatch(/sees the current content/)
})

it("lists a result with the reason for every item in a bulk move", async () => {
  failing = { b: 403, c: 409, d: 404, e: 500 }
  mount(["a", "b", "c", "d", "e"].map(id => subject(id)))
  await choose()
  await waitFor(() => expect(block()?.textContent).toMatch(/For each item/))
  fireEvent.click(confirmButton())
  const list = await screen.findByRole("list", { name: "Result for each item" })
  const items = within(list).getAllByRole("listitem")
  expect(items.map(li => li.getAttribute("data-outcome"))).toEqual(["success", "forbidden", "conflict", "not_found", "failed"])
  expect(items[4].textContent).toMatch(/Failed: nope/)
  expect(moves.map(m => m.id)).toEqual(["a", "b", "c", "d", "e"])
})

it("cannot be closed while items are still moving, and keeps the results", async () => {
  let release!: () => void
  hold = new Promise(resolve => { release = resolve })
  const view = mount([subject("a"), subject("b")])
  await choose()
  await waitFor(() => expect(block()).toBeTruthy())
  await waitFor(() => expect(block()?.textContent).toMatch(/workspace/))
  fireEvent.click(confirmButton())
  await waitFor(() => expect(moves).toHaveLength(1))
  fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" })
  fireEvent.click(screen.getByRole("button", { name: /cancel/i }))
  expect(view.onOpenChange).not.toHaveBeenCalledWith(false)
  release()
  expect(await screen.findByRole("list", { name: "Result for each item" })).toBeTruthy()
})
