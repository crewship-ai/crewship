/**
 * Folder sharing on the /pages shell (#2533, spec §8 U1–U4).
 *
 * The wire is the contract in the issue, mocked here field for field. What
 * is pinned: which of two surfaces a reader gets is the SERVER's decision
 * (200 on the ACL read is the table, 403 is the marker), the control has
 * exactly two states because edit implies view, every write leaves with the
 * right body, the move dialog says who will see the page in the words the
 * reader is allowed, a 409 regenerates that block, and all of it is
 * reachable without a pointer.
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
import type { WirePageFolder, WireFolderAclEntry } from "@/hooks/use-page-folders"
import { FOLDER_ACL_SENTENCE } from "@/lib/pages/folder-sharing"

const NOW = new Date("2026-09-13T12:00:00Z")
const WS = "workspace_id=ws-1"

const OPS_REF = { slug: "ops", name: "Ops", icon: "rocket", color: "amber" }
const FLEET: WirePage = { id: "p1", slug: "fleet-201", name: "Flotila .201", owner: "crew/lookout", folder: OPS_REF, pages_version: 3, panels: [] }
const NOTES: WirePage = { id: "p2", slug: "my-notes", name: "My notes", owner: "user/u1", folder: null, pages_version: 0, panels: [] }
const DRAFT: WirePage = { id: "p3", slug: "draft", name: "Draft", owner: "user/u1", folder: null, pages_version: 2, panels: [] }

const OPS: WirePageFolder = { id: "f1", slug: "ops", name: "Ops", icon: "rocket", color: "amber", owner: "crew/lookout", owner_crew_name: "Lookout", page_count: 1, acl_version: 5, shared: "crew" }
const ARCHIVE: WirePageFolder = { id: "f2", slug: "archive", name: "Archive", icon: null, color: null, owner: "crew/finance", owner_crew_name: "Finance", page_count: 0, acl_version: 1, shared: "workspace" }

const LONG = "a-very-long-crew-display-name-that-goes-on-and-on-and-on-past-any-reasonable-column"
const SUPPORT: WireFolderAclEntry = { subject_type: "crew", subject_id: "c-support", label: "Support", can_read: true, can_write: false, set_by: "ada@example.com", set_at: "2026-09-13T10:00:00Z" }
const OPS_CREW: WireFolderAclEntry = { subject_type: "crew", subject_id: "c-ops", label: "Ops", can_read: true, can_write: true, set_by: "ada@example.com", set_at: "2026-09-13T10:00:00Z" }
const BOB: WireFolderAclEntry = { subject_type: "user", subject_id: "u-bob", label: LONG, can_read: true, can_write: false }

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
  /** The ACL read, per folder slug. Absent: a 403 in the product's words. */
  acl?: (slug: string) => { acl: WireFolderAclEntry[]; acl_version: number } | null
  me?: () => string[]
  answer?: (call: Call) => Response | null
}

const REFUSAL = "Only a manager of the owning crew or a workspace admin can see who a folder is shared with."

function mount(harness: Harness = {}, slug?: string) {
  const calls: Call[] = []
  const pages = harness.pages ?? (() => [FLEET, NOTES, DRAFT])
  const folders = harness.folders ?? (() => [OPS, ARCHIVE])
  const mockFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = (init?.method ?? "GET").toUpperCase()
    const call: Call = { method, url, body: init?.body ? JSON.parse(String(init.body)) : null }
    calls.push(call)
    const answered = harness.answer?.(call)
    if (answered) return answered
    const aclRead = url.match(/^\/api\/v1\/page-folders\/([^/]+)\/acl\?/)
    if (method === "GET" && aclRead) {
      const body = harness.acl?.(decodeURIComponent(aclRead[1])) ?? null
      return body ? json(200, body) : json(403, { error: REFUSAL })
    }
    if (method === "GET" && /^\/api\/v1\/pages\/[^/]+\/access\/me\?/.test(url)) return json(200, { paths: harness.me?.() ?? [] })
    if (method === "GET" && url.startsWith("/api/v1/page-folders?")) return json(200, { folders: folders() })
    if (method === "GET" && url.startsWith("/api/v1/crews?")) return json(200, { data: [{ id: "c1", slug: "lookout", name: "Lookout" }] })
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
  return { calls, writes: () => calls.filter((c) => c.method !== "GET"), reads: (prefix: string) => calls.filter((c) => c.method === "GET" && c.url.startsWith(prefix)) }
}

const rowOf = (name: string) => screen.getByText(name).closest('[role="button"]') as HTMLElement

/** Keyboard only: ArrowDown opens the Radix menu on its trigger, Enter picks. */
async function openSharing(folderName: string) {
  const trigger = await screen.findByRole("button", { name: `Actions for folder ${folderName}` })
  act(() => trigger.focus())
  fireEvent.keyDown(trigger, { key: "ArrowDown" })
  const item = await screen.findByRole("menuitem", { name: /sharing/i })
  fireEvent.keyDown(item, { key: "Enter" })
  return screen.findByRole("dialog")
}

function chooseFromRowMenu(row: HTMLElement, item: RegExp) {
  act(() => row.focus())
  fireEvent.keyDown(row, { key: "F10", shiftKey: true })
  fireEvent.keyDown(screen.getByRole("menuitem", { name: item }), { key: "Enter" })
}

beforeEach(() => {
  cleanup()
  push.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  window.history.replaceState(null, "", "/pages")
})
afterEach(() => vi.unstubAllGlobals())

describe("Folder → Sharing, for a manager (U1)", () => {
  it("draws the sentence, the owning crew fixed at Can edit, each entry with a two-state control, and Everyone off", async () => {
    mount({ acl: () => ({ acl: [SUPPORT, OPS_CREW], acl_version: 5 }) })
    const dialog = await openSharing("Ops")
    await waitFor(() => expect(within(dialog).getByRole("table", { name: "Who reaches this folder" })).toBeTruthy())

    expect(within(dialog).getByText(FOLDER_ACL_SENTENCE)).toBeTruthy()
    const owner = dialog.querySelector('[data-slot="acl-owner"]') as HTMLElement
    expect(owner.textContent).toContain("Lookout")
    expect(owner.textContent).toContain("Can edit")
    expect(within(owner).queryByRole("radio")).toBeNull()

    // Edit implies view: two states, never "edit without view".
    const support = within(dialog).getByRole("radiogroup", { name: "Permission for Support" })
    expect(within(support).getAllByRole("radio").map((r) => r.textContent)).toEqual(["Can view", "Can edit"])
    expect(within(support).getByRole("radio", { name: "Can view" }).getAttribute("aria-checked")).toBe("true")
    const ops = within(dialog).getByRole("radiogroup", { name: "Permission for Ops" })
    expect(within(ops).getByRole("radio", { name: "Can edit" }).getAttribute("aria-checked")).toBe("true")

    const everyone = within(dialog).getByRole("switch", { name: "Everyone in this workspace" })
    expect(everyone.getAttribute("aria-checked")).toBe("false")
    expect(within(dialog).queryByRole("radiogroup", { name: "Permission for Everyone in this workspace" })).toBeNull()
  })

  it("sends can_write on the control, the workspace row on the switch, and the subject on Add and Remove", async () => {
    let entries = [SUPPORT]
    const { writes } = mount({
      acl: () => ({ acl: entries, acl_version: 5 }),
      answer: (c) => {
        if (c.method === "PUT" && c.url.startsWith("/api/v1/page-folders/ops/acl?")) {
          const b = c.body as { subject_type: string; subject_id?: string; can_write: boolean }
          if (b.subject_type === "workspace") entries = [...entries, { subject_type: "workspace", subject_id: "", can_read: true, can_write: b.can_write }]
          else if (b.subject_id === "c-support") entries = entries.map((e) => (e.subject_id === "c-support" ? { ...e, can_write: b.can_write } : e))
          else entries = [...entries, { subject_type: b.subject_type, subject_id: `id-${b.subject_id}`, label: b.subject_id, can_read: true, can_write: b.can_write }]
          return json(200, { subject_type: b.subject_type, subject_id: b.subject_id ?? "", can_read: true, can_write: b.can_write })
        }
        if (c.method === "DELETE" && c.url.startsWith("/api/v1/page-folders/ops/acl/")) {
          const [type, id = ""] = c.url.slice("/api/v1/page-folders/ops/acl/".length).split("?")[0].split("/")
          entries = entries.filter((e) => !(e.subject_type === type && (e.subject_id ?? "") === id))
          return json(204, null)
        }
        return null
      },
    })
    const dialog = await openSharing("Ops")
    await waitFor(() => expect(within(dialog).getByRole("radiogroup", { name: "Permission for Support" })).toBeTruthy())

    fireEvent.click(within(within(dialog).getByRole("radiogroup", { name: "Permission for Support" })).getByRole("radio", { name: "Can edit" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0]).toEqual({ method: "PUT", url: `/api/v1/page-folders/ops/acl?${WS}`, body: { subject_type: "crew", subject_id: "c-support", can_write: true } })
    await waitFor(() =>
      expect(within(within(dialog).getByRole("radiogroup", { name: "Permission for Support" })).getByRole("radio", { name: "Can edit" }).getAttribute("aria-checked")).toBe("true"),
    )

    fireEvent.click(within(dialog).getByRole("switch", { name: "Everyone in this workspace" }))
    await waitFor(() => expect(writes()).toHaveLength(2))
    expect(writes()[1].body).toEqual({ subject_type: "workspace", can_write: false })
    await waitFor(() => expect(within(dialog).getByRole("radiogroup", { name: "Permission for Everyone in this workspace" })).toBeTruthy())

    fireEvent.change(within(dialog).getByLabelText("Subject kind"), { target: { value: "crew" } })
    fireEvent.change(within(dialog).getByLabelText("Crew slug"), { target: { value: "finance" } })
    fireEvent.click(within(within(dialog).getByRole("radiogroup", { name: "Permission for the new entry" })).getByRole("radio", { name: "Can edit" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }))
    await waitFor(() => expect(writes()).toHaveLength(3))
    expect(writes()[2].body).toEqual({ subject_type: "crew", subject_id: "finance", can_write: true })
    await waitFor(() => expect((within(dialog).getByLabelText("Crew slug") as HTMLInputElement).value).toBe(""))

    fireEvent.click(within(dialog).getByRole("button", { name: "Remove Support" }))
    await waitFor(() => expect(writes()).toHaveLength(4))
    expect(writes()[3]).toEqual({ method: "DELETE", url: `/api/v1/page-folders/ops/acl/crew/c-support?${WS}`, body: null })
    await waitFor(() => expect(within(dialog).queryByRole("radiogroup", { name: "Permission for Support" })).toBeNull())

    fireEvent.click(within(dialog).getByRole("switch", { name: "Everyone in this workspace" }))
    await waitFor(() => expect(writes()).toHaveLength(5))
    expect(writes()[4]).toEqual({ method: "DELETE", url: `/api/v1/page-folders/ops/acl/workspace?${WS}`, body: null })
  })

  it("renders a refused write in the dialog, in the server's words, and keeps what was typed", async () => {
    mount({
      acl: () => ({ acl: [], acl_version: 5 }),
      answer: (c) => (c.method === "PUT" ? json(400, { error: "An agent cannot be given folder permissions." }) : null),
    })
    const dialog = await openSharing("Ops")
    const ref = await within(dialog).findByLabelText("Email")
    fireEvent.change(ref, { target: { value: "watcher" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }))
    await waitFor(() => expect(within(dialog).getByRole("alert").textContent).toContain("An agent cannot be given folder permissions."))
    expect((within(dialog).getByLabelText("Email") as HTMLInputElement).value).toBe("watcher")
  })

  it("truncates a long label and keeps the whole of it in title (U4)", async () => {
    mount({ acl: () => ({ acl: [BOB], acl_version: 5 }) })
    const dialog = await openSharing("Ops")
    await waitFor(() => expect(dialog.querySelector('[data-subject="user:u-bob"]')).toBeTruthy())
    const row = dialog.querySelector('[data-subject="user:u-bob"]') as HTMLElement
    const label = within(row).getByTitle(LONG)
    expect(label.className).toContain("truncate")
    expect(label.textContent).toBe(LONG)
  })
})

describe("Folder → Sharing, for everyone else (U1)", () => {
  it("shows the marker, the product's refusal, and the reader's own paths to the open page", async () => {
    mount({ me: () => ["crew:lookout", "folder:ops"] }, "fleet-201")
    await waitFor(() => expect(screen.getByText("Flotila .201")).toBeTruthy())
    const dialog = await openSharing("Ops")
    await waitFor(() => expect(dialog.querySelector('[data-slot="folder-sharing-marker"]')).toBeTruthy())
    expect(within(dialog).getByText("Shared with a crew")).toBeTruthy()
    expect(within(dialog).getByText(REFUSAL)).toBeTruthy()
    expect(within(dialog).queryByRole("table")).toBeNull()
    await waitFor(() => expect(dialog.querySelector('[data-slot="own-paths"]')?.textContent).toContain("You reach it through your crew lookout and the folder ops."))
  })

  it("says Shared with everyone in this workspace for a workspace-shared folder, with no paths when no page is open", async () => {
    mount()
    const dialog = await openSharing("Archive")
    await waitFor(() => expect(within(dialog).getByText("Shared with everyone in this workspace")).toBeTruthy())
    expect(dialog.querySelector('[data-slot="own-paths"]')).toBeNull()
  })
})

describe("the sidebar marker", () => {
  it("marks a folder shared with the whole workspace, and only that one", async () => {
    mount()
    const marker = await screen.findByRole("img", { name: "Shared with everyone in this workspace" })
    expect(marker.getAttribute("title")).toBe("Shared with everyone in this workspace")
    expect(marker.closest("button")?.textContent).toContain("Archive")
    expect(screen.getAllByRole("img", { name: "Shared with everyone in this workspace" })).toHaveLength(1)
  })
})

describe("the move dialog says who will see the page (U2)", () => {
  it("names them for a manager of the target, from the target's ACL", async () => {
    mount({ acl: (slug) => (slug === "ops" ? { acl: [SUPPORT, { subject_type: "workspace", subject_id: "", can_read: true, can_write: false }, OPS_CREW], acl_version: 5 } : null) })
    await waitFor(() => expect(screen.getByText("My notes")).toBeTruthy())
    chooseFromRowMenu(rowOf("My notes"), /move to folder/i)
    const dialog = await screen.findByRole("dialog")
    // Nothing chosen yet: it is Unfiled now, and the block says so.
    await waitFor(() => expect(dialog.querySelector('[data-slot="move-impact"]')?.textContent).toContain("No folder permissions apply"))
    fireEvent.click(await within(dialog).findByRole("radio", { name: /^Ops/ }))
    await waitFor(() =>
      expect(dialog.querySelector('[data-slot="move-impact"]')?.textContent).toContain(
        "Visible to crew Support and everyone in this workspace; crew Ops can edit.",
      ),
    )
    expect(within(dialog).queryByText(/restore sharing/i)).toBeNull()
  })

  it("says it in general for anyone else, from the folder's marker", async () => {
    mount()
    await waitFor(() => expect(screen.getByText("My notes")).toBeTruthy())
    chooseFromRowMenu(rowOf("My notes"), /move to folder/i)
    const dialog = await screen.findByRole("dialog")
    fireEvent.click(await within(dialog).findByRole("radio", { name: /^Archive/ }))
    await waitFor(() => expect(dialog.querySelector('[data-slot="move-impact"]')?.textContent).toContain("Visible to everyone in this workspace."))
    fireEvent.click(within(dialog).getByRole("radio", { name: /^Ops/ }))
    await waitFor(() => expect(dialog.querySelector('[data-slot="move-impact"]')?.textContent).toContain("Visible to members of the shared crews."))
  })

  it("sends acl_version, and on a 409 re-reads the ACL, regenerates the block and sends the fresh version next", async () => {
    let attempts = 0
    let aclVersion = 5
    let entries: WireFolderAclEntry[] = [SUPPORT]
    const { writes, reads } = mount({
      folders: () => [{ ...OPS, acl_version: aclVersion }, ARCHIVE],
      acl: (slug) => (slug === "ops" ? { acl: entries, acl_version: aclVersion } : null),
      answer: (c) => {
        if (c.method === "POST" && c.url.startsWith("/api/v1/page-folders/ops/pages?")) {
          attempts += 1
          if (attempts === 1) {
            // A manager changed the sharing meanwhile: the block is behind.
            aclVersion = 6
            entries = [SUPPORT, { subject_type: "workspace", subject_id: "", can_read: true, can_write: false }]
            return json(409, { error: "acl_version is stale", conflict: "acl_version", pages_version: 0, acl_version: 6, acl: entries })
          }
          return json(200, { page: { ...NOTES, folder: OPS_REF, pages_version: 1 } })
        }
        return null
      },
    })
    await waitFor(() => expect(screen.getByText("My notes")).toBeTruthy())
    chooseFromRowMenu(rowOf("My notes"), /move to folder/i)
    const dialog = await screen.findByRole("dialog")
    fireEvent.click(await within(dialog).findByRole("radio", { name: /^Ops/ }))
    await waitFor(() => expect(dialog.querySelector('[data-slot="move-impact"]')?.textContent).toContain("Visible to crew Support."))
    const aclReadsBefore = reads("/api/v1/page-folders/ops/acl").length

    fireEvent.click(within(dialog).getByRole("button", { name: "Move" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0].body).toEqual({ page: "my-notes", pages_version: 0, acl_version: 5 })

    await waitFor(() => expect(within(dialog).getByText("The folder or the page changed; try again.")).toBeTruthy())
    expect(reads("/api/v1/page-folders/ops/acl").length).toBeGreaterThan(aclReadsBefore)
    await waitFor(() =>
      expect(dialog.querySelector('[data-slot="move-impact"]')?.textContent).toContain("Visible to crew Support and everyone in this workspace."),
    )

    fireEvent.click(within(dialog).getByRole("button", { name: "Move" }))
    await waitFor(() => expect(writes()).toHaveLength(2))
    expect(writes()[1].body).toEqual({ page: "my-notes", pages_version: 0, acl_version: 6 })
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  })
})

describe("moving several pages at once", () => {
  async function selectTwo() {
    await waitFor(() => expect(screen.getByText("My notes")).toBeTruthy())
    fireEvent.click(screen.getByRole("button", { name: "Select pages" }))
    // Space on the focused row toggles it; Shift+click takes a range.
    const notes = rowOf("My notes")
    act(() => notes.focus())
    fireEvent.keyDown(notes, { key: " " })
    fireEvent.click(rowOf("Draft"), { shiftKey: true })
    await waitFor(() => expect(screen.getByText("2 selected")).toBeTruthy())
    expect(rowOf("My notes").getAttribute("data-checked")).toBe("true")
    expect(rowOf("Draft").getAttribute("data-checked")).toBe("true")
    expect(rowOf("Flotila .201").getAttribute("data-checked")).toBe("false")
    fireEvent.click(screen.getByRole("button", { name: "Move to folder…" }))
    return screen.findByRole("dialog")
  }

  it("files them through the batch route, each with its own version, and the target's acl_version", async () => {
    const { writes } = mount({
      answer: (c) =>
        c.method === "POST" && c.url.startsWith("/api/v1/page-folders/ops/pages:batch")
          ? json(200, { pages: [{ ...NOTES, folder: OPS_REF, pages_version: 1 }, { ...DRAFT, folder: OPS_REF, pages_version: 3 }] })
          : null,
    })
    const dialog = await selectTwo()
    expect(within(dialog).getByText("2 pages")).toBeTruthy()
    fireEvent.click(await within(dialog).findByRole("radio", { name: /^Ops/ }))
    await waitFor(() => expect(dialog.querySelector('[data-slot="move-impact"]')?.textContent).toContain("For all 2 pages: Visible to members of the shared crews."))
    fireEvent.click(within(dialog).getByRole("button", { name: "Move" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0]).toEqual({
      method: "POST",
      url: `/api/v1/page-folders/ops/pages:batch?${WS}`,
      body: { pages: [{ page: "my-notes", pages_version: 0 }, { page: "draft", pages_version: 2 }], acl_version: 5 },
    })
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
    // Select mode is left behind with the move.
    expect(screen.queryByText(/selected$/)).toBeNull()
  })

  it("names the page a refusal is about, in the server's words, and writes nothing else", async () => {
    const { writes } = mount({
      answer: (c) =>
        c.method === "POST" && c.url.startsWith("/api/v1/page-folders/ops/pages:batch")
          ? json(403, { error: "You do not own this page, so you cannot file it.", page: "draft" })
          : null,
    })
    const dialog = await selectTwo()
    fireEvent.click(await within(dialog).findByRole("radio", { name: /^Ops/ }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Move" }))
    await waitFor(() => expect(within(dialog).getByRole("alert").textContent).toContain("Draft: You do not own this page, so you cannot file it."))
    expect(writes()).toHaveLength(1)
    expect(screen.getByRole("dialog")).toBeTruthy()
  })
})
