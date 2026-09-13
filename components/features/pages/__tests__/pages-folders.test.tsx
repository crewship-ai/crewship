/**
 * Folders on the /pages shell — the flows that cross the rail, the dialogs
 * and the wire (#2527, collections analysis §8 U2, U3).
 *
 * The rail's own tests prove the menu opens and calls back; these prove what
 * the shell does with the call: which request leaves, with which versions in
 * it, and what the person sees when the server says no. The wire is the
 * contract in the issue, mocked here field for field, because the backend
 * is landing separately and this is what the UI will be held to.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, fireEvent, cleanup, waitFor, within, act } from "@testing-library/react"

const push = vi.fn()
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push, replace: vi.fn(), prefetch: vi.fn(), back: vi.fn() }),
  usePathname: () => "/pages",
  useSearchParams: () => new URLSearchParams(),
  useParams: () => ({}),
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/components/features/pages/editor/section-content", () => ({
  EditorContentSection: () => <div data-testid="section-content">Content</div>,
}))
vi.mock("@/components/features/pages/editor/section-data-actions", () => ({
  EditorDataActionsSection: () => <div data-testid="section-data">Data & actions</div>,
}))
vi.mock("@/components/features/pages/editor/section-access", () => ({
  EditorAccessSection: () => <div data-testid="section-access">Access</div>,
}))
vi.mock("@/components/features/pages/editor/section-history", () => ({
  EditorHistorySection: () => <div data-testid="section-history">History</div>,
}))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock("sonner", () => ({ toast }))

import { PagesLayout } from "@/components/features/pages/pages-layout"
import type { WirePage } from "@/hooks/use-pages"
import type { WirePageFolder } from "@/hooks/use-page-folders"

const NOW = new Date("2026-09-13T12:00:00Z")
const WS = "workspace_id=ws-1"

const OPS_REF = { slug: "ops", name: "Ops", icon: "rocket", color: "amber" }

const FLEET: WirePage = { id: "p1", slug: "fleet-201", name: "Flotila .201", owner: "crew/lookout", folder: OPS_REF, pages_version: 3, panels: [] }
const NOTES: WirePage = { id: "p2", slug: "my-notes", name: "My notes", owner: "user/u1", folder: null, pages_version: 0, panels: [] }

const OPS: WirePageFolder = { id: "f1", slug: "ops", name: "Ops", icon: "rocket", color: "amber", owner: "crew/lookout", owner_crew_name: "Lookout", page_count: 1, grants_version: 5 }
const ARCHIVE: WirePageFolder = { id: "f2", slug: "archive", name: "Archive", icon: null, color: null, owner: "crew/finance", owner_crew_name: "Finance", page_count: 0, grants_version: 1 }
const BUSY: WirePageFolder = { id: "f3", slug: "busy", name: "Busy", icon: null, color: null, owner: "crew/finance", owner_crew_name: "Finance", page_count: 2, grants_version: 1 }

function json(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: { get: () => null },
    json: async () => body,
    text: async () => (body === null ? "" : JSON.stringify(body)),
  } as unknown as Response
}

interface Call {
  method: string
  url: string
  body: unknown
}

interface Harness {
  pages?: () => WirePage[]
  folders?: () => WirePageFolder[]
  /** Answers for a folder write, in order; the last one repeats. */
  answer?: (call: Call) => Response | null
}

function mount(harness: Harness = {}, slug?: string) {
  const calls: Call[] = []
  const pages = harness.pages ?? (() => [FLEET, NOTES])
  const folders = harness.folders ?? (() => [OPS, ARCHIVE, BUSY])
  const mockFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = (init?.method ?? "GET").toUpperCase()
    const call: Call = { method, url, body: init?.body ? JSON.parse(String(init.body)) : null }
    calls.push(call)
    const answered = harness.answer?.(call)
    if (answered) return answered
    if (method === "GET" && url.startsWith("/api/v1/pages/folders?")) return json(200, { folders: folders() })
    if (method === "GET" && url.startsWith("/api/v1/crews?"))
      return json(200, { data: [{ id: "c1", slug: "lookout", name: "Lookout" }, { id: "c2", slug: "finance", name: "Finance" }] })
    if (method === "GET" && url.startsWith("/api/v1/pages?")) return json(200, pages())
    if (method === "GET" && url.startsWith("/api/v1/pages/")) {
      const wanted = decodeURIComponent(url.split("/api/v1/pages/")[1].split("?")[0])
      return json(200, pages().find((p) => p.slug === wanted) ?? null)
    }
    return json(404, { error: `unrouted ${method} ${url}` })
  })
  vi.stubGlobal("fetch", mockFetch)
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <PagesLayout workspaceId="ws-1" slug={slug} now={NOW} />
    </QueryClientProvider>,
  )
  return { calls, writes: () => calls.filter((c) => c.method !== "GET") }
}

const rowOf = (name: string) => screen.getByText(name).closest('[role="button"]') as HTMLElement
const groupHeaders = () => screen.getAllByRole("button").filter((b) => b.hasAttribute("data-rail-header"))

/** Shift+F10 on the focused row, then Enter on the named item — no pointer. */
function chooseFromRowMenu(row: HTMLElement, item: RegExp) {
  act(() => row.focus())
  fireEvent.keyDown(row, { key: "F10", shiftKey: true })
  const entry = screen.getByRole("menuitem", { name: item })
  fireEvent.keyDown(entry, { key: "Enter" })
}

beforeEach(() => {
  cleanup()
  push.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  window.history.replaceState(null, "", "/pages")
})
afterEach(() => vi.unstubAllGlobals())

describe("the rail is grouped by folder from the folder route", () => {
  it("draws every readable folder with its count, then Unfiled", async () => {
    mount()
    await waitFor(() => expect(screen.getByText("Flotila .201")).toBeTruthy())
    await waitFor(() => expect(groupHeaders().map((h) => h.textContent)).toEqual(["Archive0", "Busy2", "Ops1", "Unfiled1"]))
  })

  it("groups by owner and offers no folder control when the folder route answers 404", async () => {
    mount({ answer: (c) => (c.method === "GET" && c.url.startsWith("/api/v1/pages/folders?") ? json(404, { error: "not found" }) : null) })
    await waitFor(() => expect(screen.getByText("Flotila .201")).toBeTruthy())
    await waitFor(() => expect(groupHeaders().map((h) => h.textContent)).toEqual(["lookout1", "Owned by others1"]))
    expect(screen.queryByRole("button", { name: "New folder" })).toBeNull()
  })
})

describe("moving a page (U2)", () => {
  it("moves a page into a folder from the keyboard alone, sending the page's and the folder's versions", async () => {
    const { writes } = mount({
      answer: (c) => (c.method === "POST" && c.url.startsWith("/api/v1/pages/folders/ops/pages") ? json(200, { page: { ...NOTES, folder: OPS_REF, pages_version: 1 } }) : null),
    })
    await waitFor(() => expect(screen.getByText("My notes")).toBeTruthy())
    await waitFor(() => expect(groupHeaders().length).toBe(4))

    chooseFromRowMenu(rowOf("My notes"), /move to folder/i)

    const dialog = await screen.findByRole("dialog")
    expect(within(dialog).getByText("Move to folder")).toBeTruthy()
    const targets = await within(dialog).findAllByRole("radio")
    expect(targets.map((t) => t.getAttribute("data-folder-option"))).toEqual(["archive", "busy", "ops", "unfiled"])
    // Where it is now is marked, and the primary is idle until a target differs.
    expect(within(dialog).getByRole("radio", { name: /unfiled/i }).getAttribute("aria-checked")).toBe("true")
    const move = within(dialog).getByRole("button", { name: "Move" })
    expect(move).toBeDisabled()

    fireEvent.click(within(dialog).getByRole("radio", { name: /^Ops/ }))
    expect(move).not.toBeDisabled()
    fireEvent.click(move)

    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0]).toEqual({
      method: "POST",
      url: `/api/v1/pages/folders/ops/pages?${WS}`,
      body: { page: "my-notes", pages_version: 0, grants_version: 5 },
    })
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  })

  it("on a 409 re-reads both lists, says so, and sends the fresh versions on the next confirm", async () => {
    let attempts = 0
    let version = 0
    const { calls, writes } = mount({
      pages: () => [FLEET, { ...NOTES, pages_version: version }],
      answer: (c) => {
        if (c.method === "POST" && c.url.startsWith("/api/v1/pages/folders/ops/pages")) {
          attempts += 1
          if (attempts === 1) {
            // Somebody else moved the page meanwhile: the list is now behind.
            version = 4
            return json(409, { error: "pages_version is stale", conflict: "pages_version", pages_version: 4, grants_version: 5 })
          }
          return json(200, { page: { ...NOTES, folder: OPS_REF, pages_version: 5 } })
        }
        return null
      },
    })
    await waitFor(() => expect(screen.getByText("My notes")).toBeTruthy())
    await waitFor(() => expect(groupHeaders().length).toBe(4))
    const folderReadsBefore = calls.filter((c) => c.method === "GET" && c.url.startsWith("/api/v1/pages/folders?")).length
    const listReadsBefore = calls.filter((c) => c.method === "GET" && c.url.startsWith("/api/v1/pages?")).length

    chooseFromRowMenu(rowOf("My notes"), /move to folder/i)
    const dialog = await screen.findByRole("dialog")
    fireEvent.click(await within(dialog).findByRole("radio", { name: /^Ops/ }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Move" }))

    // The sentence, with the server's own words under it; the dialog stays.
    await waitFor(() => expect(within(dialog).getByText("The folder or the page changed; try again.")).toBeTruthy())
    expect(within(dialog).getByText("pages_version is stale")).toBeTruthy()
    // Both lists were read again — not just the one that tripped.
    expect(calls.filter((c) => c.method === "GET" && c.url.startsWith("/api/v1/pages/folders?")).length).toBeGreaterThan(folderReadsBefore)
    expect(calls.filter((c) => c.method === "GET" && c.url.startsWith("/api/v1/pages?")).length).toBeGreaterThan(listReadsBefore)

    // Nothing was retried on the person's behalf.
    expect(writes()).toHaveLength(1)
    fireEvent.click(within(dialog).getByRole("button", { name: "Move" }))
    await waitFor(() => expect(writes()).toHaveLength(2))
    expect(writes()[1].body).toEqual({ page: "my-notes", pages_version: 4, grants_version: 5 })
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  })

  it("shows a refusal in the dialog, in the server's words, and keeps the choice", async () => {
    const { writes } = mount({
      answer: (c) =>
        c.method === "POST" && c.url.startsWith("/api/v1/pages/folders/busy/pages")
          ? json(403, { error: "Only a manager of Finance or a workspace admin can file pages into Busy." })
          : null,
    })
    await waitFor(() => expect(screen.getByText("My notes")).toBeTruthy())
    await waitFor(() => expect(groupHeaders().length).toBe(4))

    chooseFromRowMenu(rowOf("My notes"), /move to folder/i)
    const dialog = await screen.findByRole("dialog")
    fireEvent.click(await within(dialog).findByRole("radio", { name: /^Busy/ }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Move" }))

    await waitFor(() =>
      expect(within(dialog).getByRole("alert").textContent).toContain("Only a manager of Finance or a workspace admin can file pages into Busy."),
    )
    expect(within(dialog).getByRole("radio", { name: /^Busy/ }).getAttribute("aria-checked")).toBe("true")
    expect(writes()).toHaveLength(1)
  })
})

describe("removing a page from its folder (U3)", () => {
  it("sends the page's pages_version with the DELETE", async () => {
    const { writes } = mount({
      answer: (c) => (c.method === "DELETE" && c.url.startsWith("/api/v1/pages/folders/ops/pages/fleet-201") ? json(204, null) : null),
    })
    await waitFor(() => expect(screen.getByText("Flotila .201")).toBeTruthy())
    await waitFor(() => expect(groupHeaders().length).toBe(4))

    chooseFromRowMenu(rowOf("Flotila .201"), /remove from folder/i)

    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0]).toEqual({
      method: "DELETE",
      url: `/api/v1/pages/folders/ops/pages/fleet-201?${WS}`,
      body: { pages_version: 3 },
    })
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Flotila .201 is no longer in Ops."))
  })

  it("says the server's refusal where the action was", async () => {
    mount({
      answer: (c) => (c.method === "DELETE" ? json(403, { error: "Only the page's owner or a workspace admin can remove it from a folder." }) : null),
    })
    await waitFor(() => expect(screen.getByText("Flotila .201")).toBeTruthy())
    await waitFor(() => expect(groupHeaders().length).toBe(4))
    chooseFromRowMenu(rowOf("Flotila .201"), /remove from folder/i)
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith("Only the page's owner or a workspace admin can remove it from a folder."),
    )
  })
})

describe("New folder", () => {
  it("posts the name, the owning crew, and the icon and colour picked through the shared picker", async () => {
    const { writes } = mount({
      answer: (c) =>
        c.method === "POST" && c.url.startsWith("/api/v1/pages/folders?")
          ? json(201, { id: "f9", slug: "runbooks", name: "Runbooks", icon: "rocket", color: "amber", owner: "crew/finance", page_count: 0, grants_version: 0 })
          : null,
    })
    await waitFor(() => expect(screen.getByRole("button", { name: "New folder" })).toBeTruthy())
    fireEvent.click(screen.getByRole("button", { name: "New folder" }))

    const dialog = await screen.findByRole("dialog")
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: "Runbooks" } })
    const owner = (await within(dialog).findByLabelText(/Owning crew/)) as HTMLSelectElement
    await waitFor(() => expect(owner.options.length).toBe(2))
    fireEvent.change(owner, { target: { value: "crew/finance" } })

    fireEvent.click(within(dialog).getByRole("button", { name: "Choose…" }))
    // The picker is the crew one, generalised: swatches and tiles are radios.
    fireEvent.click(await screen.findByRole("radio", { name: "amber" }))
    fireEvent.click(screen.getByRole("radio", { name: "Rocket" }))
    fireEvent.click(screen.getByRole("button", { name: /^Save$/ }))
    await waitFor(() => expect(screen.queryByRole("radio", { name: "amber" })).toBeNull())

    fireEvent.click(within(dialog).getByRole("button", { name: "Create folder" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0]).toEqual({
      method: "POST",
      url: `/api/v1/pages/folders?${WS}`,
      body: { name: "Runbooks", owner: "crew/finance", icon: "rocket", color: "amber" },
    })
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  })

  it("renders the server's 403 at the control and keeps what was typed", async () => {
    mount({
      answer: (c) =>
        c.method === "POST" && c.url.startsWith("/api/v1/pages/folders?")
          ? json(403, { error: "You are not a manager in Lookout; ask one, or a workspace admin." })
          : null,
    })
    await waitFor(() => expect(screen.getByRole("button", { name: "New folder" })).toBeTruthy())
    fireEvent.click(screen.getByRole("button", { name: "New folder" }))
    const dialog = await screen.findByRole("dialog")
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: "Runbooks" } })
    await waitFor(() => expect((within(dialog).getByLabelText(/Owning crew/) as HTMLSelectElement).options.length).toBe(2))
    fireEvent.click(within(dialog).getByRole("button", { name: "Create folder" }))
    await waitFor(() =>
      expect(within(dialog).getByRole("alert").textContent).toContain("You are not a manager in Lookout; ask one, or a workspace admin."),
    )
    expect((within(dialog).getByLabelText(/^Name/) as HTMLInputElement).value).toBe("Runbooks")
  })
})

describe("Rename and Delete", () => {
  it("renames through PATCH with only what changed", async () => {
    const { writes } = mount({
      answer: (c) => (c.method === "PATCH" ? json(200, { ...OPS, name: "Operations" }) : null),
    })
    await waitFor(() => expect(screen.getByRole("button", { name: "Actions for folder Ops" })).toBeTruthy())
    fireEvent.keyDown(screen.getByRole("button", { name: "Actions for folder Ops" }), { key: "ArrowDown" })
    fireEvent.click(screen.getByRole("menuitem", { name: /rename or change icon/i }))
    const dialog = await screen.findByRole("dialog")
    const name = within(dialog).getByLabelText(/^Name/) as HTMLInputElement
    expect(name.value).toBe("Ops")
    fireEvent.change(name, { target: { value: "Operations" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0]).toEqual({ method: "PATCH", url: `/api/v1/pages/folders/ops?${WS}`, body: { name: "Operations" } })
  })

  it("refuses to delete a folder with pages the caller can see, saying how many", async () => {
    mount()
    await waitFor(() => expect(screen.getByRole("button", { name: "Actions for folder Busy" })).toBeTruthy())
    fireEvent.keyDown(screen.getByRole("button", { name: "Actions for folder Busy" }), { key: "ArrowDown" })
    fireEvent.click(screen.getByRole("menuitem", { name: /delete folder/i }))
    const dialog = await screen.findByRole("alertdialog")
    expect(dialog.textContent).toContain("Move its 2 pages out first")
    expect(within(dialog).getByRole("button", { name: "Delete folder" })).toBeDisabled()
  })

  it("shows the server's not-empty sentence when it refuses a folder that looked empty", async () => {
    const { writes } = mount({
      answer: (c) =>
        c.method === "DELETE" && c.url.startsWith("/api/v1/pages/folders/archive?")
          ? json(409, { error: "Archive still holds pages you cannot see; ask their owners to move them out first." })
          : null,
    })
    await waitFor(() => expect(screen.getByRole("button", { name: "Actions for folder Archive" })).toBeTruthy())
    fireEvent.keyDown(screen.getByRole("button", { name: "Actions for folder Archive" }), { key: "ArrowDown" })
    fireEvent.click(screen.getByRole("menuitem", { name: /delete folder/i }))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete folder" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0]).toEqual({ method: "DELETE", url: `/api/v1/pages/folders/archive?${WS}`, body: null })
    await waitFor(() =>
      expect(within(dialog).getByRole("alert").textContent).toContain(
        "Archive still holds pages you cannot see; ask their owners to move them out first.",
      ),
    )
    // The dialog stays: the refusal is read there, not in a toast that scrolls away.
    expect(screen.getByRole("alertdialog")).toBeTruthy()
  })
})
