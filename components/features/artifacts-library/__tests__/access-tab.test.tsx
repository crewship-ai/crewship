import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { LibraryItem } from "../library-items"

const flags = vi.hoisted(() => ({ mock: true, fetch: vi.fn(), human: "u1" as string | null }))
vi.mock("@/lib/feature-flags", async importOriginal => ({ ...(await importOriginal<typeof import("@/lib/feature-flags")>()), artifactsSharingMock: () => flags.mock }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: flags.fetch }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => flags.human ? { data: { user: { id: flags.human } }, status: "authenticated" } : { data: null, status: "unauthenticated" } }))

import { mockSharingApi, resetSharingMock } from "../sharing/sharing-api"
import { AccessTab } from "../sharing/access-tab"

const folder = { slug: "fin", name: "Finance", icon: null, color: null }
const item = (over: Partial<LibraryItem> = {}): LibraryItem => ({
  slug: "brief", name: "Partner brief", kind: "word", family: "document", sharedVersion: 1, native: true, projectBacked: false,
  empty: false, private: false, ownerLabel: "You", folder: null, updatedAt: null, ...over,
})

beforeEach(() => { resetSharingMock(); localStorage.clear(); flags.mock = true; flags.human = "u1"; flags.fetch.mockReset() })
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

/** GET …/access and GET …/grants as #4444 serves them. */
const serve = (subjects: unknown[], grants: unknown[]) => flags.fetch.mockImplementation(async (url: string) =>
  new Response(JSON.stringify(url.includes("/grants") ? { page: "brief", grants } : { page: "brief", page_id: "p1", pages_version: 3, subjects }), { status: 200 }))
const perms = (read: boolean, comment = false, edit = false) => ({ read, comment, edit, produce: false })

it("shows an empty state when the server lists nobody", async () => {
  flags.mock = false
  serve([], [])
  renderTab()
  expect(await screen.findByText("Nobody else can open it yet.")).toBeTruthy()
})

it("shows a real server answer with a non-owner grant, never as private", async () => {
  flags.mock = false
  serve(
    [{ subject_type: "user", subject_id: "u1", label: "me@example.test", paths: ["owner"], effective_permissions: perms(true, true, true), sources: [{ path: "owner", inherited: false, permissions: perms(true, true, true) }] },
      { subject_type: "user", subject_id: "u2", label: "ann@example.test", paths: ["grant:page:comment"], effective_permissions: perms(true, true), sources: [{ path: "grant:page:comment", inherited: false, permissions: perms(true, true) }] }],
    [{ subject_type: "user", subject: "ann@example.test", subject_id: "u2", level: "comment", live: true }],
  )
  renderTab()
  const groups = await screen.findAllByRole("group")
  expect(groups.map(g => g.getAttribute("aria-label"))).toEqual(["Edit access", "Comment access"])
  expect(within(groups[1]).getByText("ann@example.test")).toBeTruthy()
  expect(within(groups[1]).getByText("Shared directly")).toBeTruthy()
  expect(screen.queryByText("Nobody else can open it yet.")).toBeNull()
})

it("an envelope it does not recognise is an error, not an empty list", async () => {
  flags.mock = false
  flags.fetch.mockResolvedValue(new Response(JSON.stringify({ entries: [] }), { status: 200 }))
  renderTab()
  expect((await screen.findByRole("alert")).textContent).toMatch(/could not be read/)
  expect(screen.queryByText("Nobody else can open it yet.")).toBeNull()
})

it("a new session in the same workspace never sees the previous one's list", async () => {
  flags.mock = false
  serve([], [{ subject_type: "user", subject: "ann@example.test", subject_id: "u2", level: "read", live: true }])
  const client = new QueryClient()
  const view = render(<QueryClientProvider client={client}><AccessTab workspaceId="ws1" item={item()} onShare={vi.fn()} /></QueryClientProvider>)
  await screen.findByText("ann@example.test")
  // Signed out, then another human: the old answer is gone at once, and no request runs without a human.
  flags.human = null
  flags.fetch.mockClear()
  view.rerender(<QueryClientProvider client={client}><AccessTab workspaceId="ws1" item={item()} onShare={vi.fn()} /></QueryClientProvider>)
  expect(screen.queryByText("ann@example.test")).toBeNull()
  expect(flags.fetch).not.toHaveBeenCalled()
  flags.human = "u7"
  serve([], [])
  view.rerender(<QueryClientProvider client={client}><AccessTab workspaceId="ws1" item={item()} onShare={vi.fn()} /></QueryClientProvider>)
  expect(screen.queryByText("ann@example.test")).toBeNull()
  expect(await screen.findByText("Nobody else can open it yet.")).toBeTruthy()
})
