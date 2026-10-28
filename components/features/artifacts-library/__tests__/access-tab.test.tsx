import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { LibraryItem } from "../library-items"

const flags = vi.hoisted(() => ({ mock: true, fetch: vi.fn() }))
vi.mock("@/lib/feature-flags", async importOriginal => ({ ...(await importOriginal<typeof import("@/lib/feature-flags")>()), artifactsSharingMock: () => flags.mock }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: flags.fetch }))

import { mockSharingApi, resetSharingMock } from "../sharing/sharing-api"
import { AccessTab } from "../sharing/access-tab"

const folder = { slug: "fin", name: "Finance", icon: null, color: null }
const item = (over: Partial<LibraryItem> = {}): LibraryItem => ({
  slug: "brief", name: "Partner brief", kind: "word", family: "document", sharedVersion: 1, native: true, projectBacked: false,
  empty: false, private: false, ownerLabel: "You", folder: null, updatedAt: null, ...over,
})

beforeEach(() => { resetSharingMock(); localStorage.clear(); flags.mock = true; flags.fetch.mockReset() })
afterEach(cleanup)

const renderTab = (it: LibraryItem = item(), onShare = vi.fn()) => render(<QueryClientProvider client={new QueryClient()}>
  <AccessTab workspaceId="ws1" item={it} onShare={onShare} />
</QueryClientProvider>)

it("groups effective access by level, Edit first, with the reason and the counts", async () => {
  const api = mockSharingApi("ws1")
  await api.saveFolderSettings("fin", { defaultAudience: "folder", entries: [{ subjectType: "user", subjectId: "u3", label: "Alan Turing", level: "write", via: "folder" }] })
  await api.share(item(), { subjectType: "user", subjectId: "u2", label: "Ada Lovelace", level: "comment" })
  const onShare = vi.fn()
  renderTab(item({ folder }), onShare)
  expect(screen.getByRole("status").textContent).toMatch(/Loading/)
  const groups = await screen.findAllByRole("group")
  expect(groups.map(g => g.getAttribute("aria-label"))).toEqual(["Edit access", "Comment access", "Read access"])
  const edit = within(groups[0])
  expect(edit.getByText("You")).toBeTruthy()
  expect(edit.getByText("Owner")).toBeTruthy()
  expect(edit.getByText("Via folder Finance · Change in folder settings")).toBeTruthy()
  expect(edit.getByText("2")).toBeTruthy()
  expect(within(groups[1]).getByText("Ada Lovelace")).toBeTruthy()
  expect(within(groups[1]).getByText("Shared directly")).toBeTruthy()
  expect(within(groups[2]).getByText("Everyone in this workspace")).toBeTruthy()
  expect(screen.getByText(/4 people and groups · 2 Edit, 1 Comment, 1 Read/)).toBeTruthy()
  expect(screen.queryByRole("combobox")).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Share…" }))
  expect(onShare).toHaveBeenCalledTimes(1)
})

it("a private item shows only the owner", async () => {
  renderTab(item({ private: true }))
  const groups = await screen.findAllByRole("group")
  expect(groups).toHaveLength(1)
  expect(within(groups[0]).getByText("You")).toBeTruthy()
  expect(screen.getByText(/1 person or group/)).toBeTruthy()
})

it("says who may see the list when the server refuses", async () => {
  flags.mock = false
  flags.fetch.mockResolvedValue(new Response(JSON.stringify({ error: "forbidden" }), { status: 403 }))
  renderTab()
  expect((await screen.findByRole("alert")).textContent).toBe("Only people who can share this artifact see its access list.")
  expect(screen.getByRole("button", { name: "Share…" })).toBeTruthy()
})

it("shows any other error as the server wrote it", async () => {
  flags.mock = false
  flags.fetch.mockResolvedValue(new Response(JSON.stringify({ detail: "Access is temporarily unavailable." }), { status: 503 }))
  renderTab()
  expect((await screen.findByRole("alert")).textContent).toBe("Access is temporarily unavailable.")
})

it("shows an empty state when the server lists nobody", async () => {
  flags.mock = false
  flags.fetch.mockResolvedValue(new Response(JSON.stringify({ entries: [] }), { status: 200 }))
  renderTab()
  expect(await screen.findByText("Nobody else can open it yet.")).toBeTruthy()
})
