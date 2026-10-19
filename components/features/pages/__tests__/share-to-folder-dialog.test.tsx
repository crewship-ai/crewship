import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { ShareToFolderDialog } from "../share-to-folder-dialog"

const request = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeChannelSafe: vi.fn(), useRealtimeEventSafe: vi.fn(), useRealtimeStatusSafe: () => "disconnected" }))

const folder = { slug: "ops", name: "Ops", owner: "crew/ops", shared: "workspace", can_accept_pages: true, acl_version: 3, page_count: 1 }
const posts: string[] = []
let failing: Record<string, number> = {}
beforeEach(() => {
  posts.length = 0
  failing = {}
  request.mockReset()
  request.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      const page = JSON.parse(String(init.body)).page as string
      posts.push(page)
      return failing[page] ? Response.json({ error: "nope" }, { status: failing[page] }) : Response.json({ page: { slug: page } })
    }
    if (/\/acl/.test(url)) return Response.json({ error: "managers only" }, { status: 403 })
    return Response.json([folder])
  })
})
afterEach(cleanup)

function mount(subjects: { slug: string; name: string; pagesVersion: number | null; versions?: number | null }[]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}><ShareToFolderDialog workspaceId="w" open onOpenChange={() => {}} subjects={subjects} /></QueryClientProvider>)
}

it("shows who gains content and history, and the same ID, before confirming", async () => {
  mount([{ slug: "plan", name: "Plan", pagesVersion: 1, versions: 4 }])
  fireEvent.click(await screen.findByRole("radio", { name: /Ops/ }))
  const block = await waitFor(() => { const el = document.querySelector("[data-slot=share-consequence]"); expect(el?.textContent).toMatch(/everyone in this workspace/); return el as HTMLElement })
  expect(block.textContent).toMatch(/4 versions/)
  expect(block.textContent).toMatch(/same ID/)
  expect(block.textContent).toMatch(/Duplicate/)
  expect(posts).toEqual([])
  expect((screen.getByRole("button", { name: "Move and share" }) as HTMLButtonElement).disabled).toBe(false)
})

it("cannot confirm before a folder is chosen", async () => {
  mount([{ slug: "plan", name: "Plan", pagesVersion: 1 }])
  await screen.findByRole("radio", { name: /Ops/ })
  expect((screen.getByRole("button", { name: "Move and share" }) as HTMLButtonElement).disabled).toBe(true)
})

it("lists a result for every item in a bulk move", async () => {
  failing = { b: 403, c: 409, d: 404 }
  mount(["a", "b", "c", "d"].map(slug => ({ slug, name: slug.toUpperCase(), pagesVersion: 1 })))
  fireEvent.click(await screen.findByRole("radio", { name: /Ops/ }))
  await waitFor(() => expect(document.querySelector("[data-slot=share-consequence]")?.textContent).toMatch(/workspace/))
  fireEvent.click(screen.getByRole("button", { name: "Move and share" }))
  const list = await screen.findByRole("list", { name: "Result for each item" })
  const outcomes = within(list).getAllByRole("listitem").map(li => li.getAttribute("data-outcome"))
  expect(outcomes).toEqual(["success", "forbidden", "conflict", "not_found"])
  expect(posts).toEqual(["a", "b", "c", "d"])
})
