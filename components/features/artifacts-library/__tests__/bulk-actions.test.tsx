import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { LibraryItem } from "../library-items"
import type { BulkRequest, SharingApi } from "../sharing/sharing-api"

const m = vi.hoisted(() => ({ dryRuns: [] as BulkRequest[] }))
vi.mock("@/lib/feature-flags", async importOriginal => ({ ...(await importOriginal<typeof import("@/lib/feature-flags")>()), artifactsSharingMock: () => true }))
vi.mock("@/hooks/use-issue-people", () => ({ useIssuePeople: () => ({ people: [{ id: "u2", name: "Ann Novak" }], error: false }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(() => { throw new Error("no HTTP in the mock") }), broadcastSessionExpired: vi.fn() }))
// The real mock implementation; the dialog's own reads are recorded on the way through.
vi.mock("../sharing/sharing-api", async importOriginal => {
  const real = await importOriginal<typeof import("../sharing/sharing-api")>()
  const { useMemo } = await import("react")
  return {
    ...real,
    useSharingApi: (workspaceId: string): SharingApi => useMemo(() => {
      const api = real.mockSharingApi(workspaceId)
      return { ...api, bulk: request => { m.dryRuns.push(request); return api.bulk(request) } }
    }, [workspaceId]),
  }
})

import { BulkBar, MoveToFolderDialog } from "../sharing/bulk-actions"
import { mockFolderOverride, mockSharingApi, resetSharingMock } from "../sharing/sharing-api"

const item = (slug: string, name: string, over: Partial<LibraryItem> = {}): LibraryItem => ({
  slug, name, kind: "word", family: "document", sharedVersion: null, native: true, projectBacked: false, empty: false,
  private: false, ownerLabel: "You", folder: null, updatedAt: null, ...over,
})
const FOLDERS = [{ slug: "design", name: "Design" }, { slug: "sales", name: "Sales" }]

beforeEach(() => { resetSharingMock(); localStorage.clear(); m.dryRuns = [] })
afterEach(cleanup)

const wrap = (ui: React.ReactNode) => <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{ui}</QueryClientProvider>
const renderBar = (selected: LibraryItem[], props: { archivedView?: boolean } = {}) => {
  const onDone = vi.fn()
  render(wrap(<BulkBar workspaceId="ws1" selected={selected} folders={FOLDERS} onClear={vi.fn()} onDone={onDone} {...props} />))
  return { onDone }
}
const impact = async () => within(await screen.findByRole("list", { name: "Impact for each artifact" })).getAllByRole("listitem")

it("hides with nothing selected and counts what is", () => {
  render(wrap(<BulkBar workspaceId="ws1" selected={[]} folders={FOLDERS} onClear={vi.fn()} onDone={vi.fn()} />))
  expect(screen.queryByRole("toolbar")).toBeNull()
  cleanup()
  renderBar([item("a", "Alpha"), item("b", "Beta")], { archivedView: true })
  expect(screen.getByRole("toolbar", { name: "Selection" }).textContent).toContain("2 selected")
  expect(screen.getByRole("button", { name: "Unarchive" })).toBeTruthy()
  expect(screen.queryByRole("button", { name: "Archive" })).toBeNull()
})

it("a move previews what each item gains from the folder, and changes nothing until Confirm", async () => {
  await mockSharingApi("ws1").saveFolderSettings("design", {
    defaultAudience: "folder",
    entries: [{ subjectType: "user", subjectId: "u2", label: "Ann Novak", level: "comment", via: "folder" }],
  })
  const { onDone } = renderBar([item("a", "Alpha"), item("b", "Beta", { folder: { slug: "design", name: "Design" } as LibraryItem["folder"] })])
  fireEvent.click(screen.getByRole("button", { name: "Move…" }))
  expect(m.dryRuns).toEqual([])
  fireEvent.change(screen.getByRole("combobox", { name: "Folder" }), { target: { value: "design" } })

  const rows = await impact()
  expect(rows[0].textContent).toContain("Gains: Ann Novak (Comment).")
  expect(rows[1].textContent).toContain("Already there")
  expect(within(rows[1]).getByText("Skipped")).toBeTruthy()
  expect(screen.getByText("1 will move, 1 skipped")).toBeTruthy()
  expect(m.dryRuns).toHaveLength(1)
  expect(m.dryRuns[0]).toMatchObject({ action: "move", dryRun: true, folder: { slug: "design" } })
  expect(mockFolderOverride("ws1", "a")).toBeUndefined()

  fireEvent.click(screen.getByRole("button", { name: "Move 1" }))
  await vi.waitFor(() => expect(onDone).toHaveBeenCalled())
  expect(onDone.mock.calls[0][0]).toMatchObject({ dryRun: false, items: [{ slug: "a", ok: true }] })
  expect(mockFolderOverride("ws1", "a")).toEqual({ slug: "design", name: "Design" })
  expect(mockFolderOverride("ws1", "b")).toBeUndefined()
})

it("moving out of a shared folder to Unfiled says who loses access", async () => {
  await mockSharingApi("ws1").saveFolderSettings("sales", {
    defaultAudience: "folder",
    entries: [{ subjectType: "user", subjectId: "u2", label: "Ann Novak", level: "write", via: "folder" }],
  })
  const onDone = vi.fn()
  render(wrap(<MoveToFolderDialog workspaceId="ws1" item={item("a", "Alpha", { private: true, folder: { slug: "sales", name: "Sales" } as LibraryItem["folder"] })} folders={FOLDERS} open onOpenChange={vi.fn()} onDone={onDone} />))
  expect(screen.getByRole("heading", { name: "Move Alpha" })).toBeTruthy()
  fireEvent.change(screen.getByRole("combobox", { name: "Folder" }), { target: { value: "__unfiled" } })
  expect((await impact())[0].textContent).toContain("Loses: Ann Novak (Edit).")
  expect(m.dryRuns[0]).toMatchObject({ folder: null, items: [{ slug: "a" }] })
})

it("archive says not deleted, shows refusals with their reason, and archives only the rest", async () => {
  const { onDone } = renderBar([item("a", "Alpha"), item("c", "Crew notes", { ownerLabel: "Sales crew" })])
  fireEvent.click(screen.getByRole("button", { name: "Archive" }))
  const dialog = screen.getByRole("dialog")
  expect(dialog.textContent).toContain("hidden, not deleted")
  expect(dialog.textContent).toContain("restore them from Archive")
  const rows = await impact()
  expect(rows[1].textContent).toContain("Only its owner can archive it")
  expect(screen.getByText("1 will be archived, 1 skipped")).toBeTruthy()
  expect(mockSharingApi("ws1").libraryState([]).archived.size).toBe(0)

  fireEvent.click(screen.getByRole("button", { name: "Archive 1" }))
  await vi.waitFor(() => expect(onDone).toHaveBeenCalled())
  expect([...mockSharingApi("ws1").libraryState([]).archived]).toEqual(["a"])
})

it("share previews the subject and level per item and leaves grants alone until Confirm", async () => {
  renderBar([item("a", "Alpha", { private: true })])
  fireEvent.click(screen.getByRole("button", { name: "Share…" }))
  fireEvent.change(screen.getByRole("combobox", { name: "Level" }), { target: { value: "comment" } })
  fireEvent.change(screen.getByRole("combobox", { name: "Share with" }), { target: { value: "user:u2" } })
  expect((await impact())[0].textContent).toContain("Gains: Ann Novak (Comment).")
  expect((await mockSharingApi("ws1").access(item("a", "Alpha", { private: true }))).map(e => e.label)).toEqual(["You"])
})

it("refuses more than the limit without asking the server", async () => {
  const many = Array.from({ length: 201 }, (_, i) => item(`p${i}`, `Item ${i}`))
  renderBar(many)
  fireEvent.click(screen.getByRole("button", { name: "Archive" }))
  expect(screen.getByRole("alert").textContent).toContain("At most 200")
  await new Promise(resolve => setTimeout(resolve, 200))
  expect(m.dryRuns).toEqual([])
  expect(screen.queryByRole("list", { name: "Impact for each artifact" })).toBeNull()
  expect(screen.queryByRole("button", { name: /^Archive \d/ })).toBeNull()
})
