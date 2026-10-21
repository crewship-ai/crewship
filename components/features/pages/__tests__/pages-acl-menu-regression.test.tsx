import { beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { PagesRail } from "../pages-rail"
import { EMPTY_PAGE_FILTERS, toPageView } from "@/hooks/use-pages"
import { toPageFolderView } from "@/hooks/use-page-folders"

beforeEach(() => cleanup())

function show({ arrange = false, manage = false, move = false, remove = false, unknown = false } = {}) {
  const folder = toPageFolderView({ slug: "ops", name: "Ops", owner: "crew/ops", page_count: 1, acl_version: 1,
    ...(unknown ? {} : { can_rename: arrange, can_share: manage, can_delete: manage, can_accept_pages: arrange }) })!
  const page = toPageView({ slug: "report", name: "Report", owner: "user/owner", panels: [], folder: { slug: "ops", name: "Ops" }, pages_version: 1,
    ...(unknown ? {} : { can_move: move, can_remove_from_folder: remove }) })
  const callbacks = { onEditFolder: vi.fn(), onShareFolder: vi.fn(), onDeleteFolder: vi.fn(), onMovePage: vi.fn(), onRemoveFromFolder: vi.fn(), onMovePages: vi.fn() }
  render(<PagesRail pages={[page]} folders={[folder]} workspaceId="ws" currentUserId="reader" search="" onSearchChange={vi.fn()}
    filters={EMPTY_PAGE_FILTERS} onFiltersChange={vi.fn()} selectedSlug={null} onSelectPage={vi.fn()} {...callbacks} />)
  return callbacks
}

describe("Pages rail uses the server's object authority", () => {
  it.each([false, true])("offers no mutations for a reader (missing metadata: %s)", unknown => {
    show({ unknown })
    expect(screen.queryByRole("button", { name: "Actions for folder Ops" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Actions for Report" })).toBeNull()
    expect(screen.queryByRole("button", { name: /Select pages/ })).toBeNull()
    const row = screen.getByText("Report").closest('[role="button"]')!
    act(() => (row as HTMLElement).focus())
    fireEvent.keyDown(row, { key: "F10", shiftKey: true })
    expect(screen.queryByRole("menuitem")).toBeNull()
  })
  it("lets an ACL writer arrange without sharing or deleting the folder", () => {
    const callbacks = show({ arrange: true, remove: true })
    fireEvent.pointerDown(screen.getByRole("button", { name: "Actions for folder Ops" }), { button: 0, ctrlKey: false })
    expect(screen.getByRole("menuitem", { name: /Rename/ })).toBeTruthy()
    expect(screen.queryByRole("menuitem", { name: /Sharing/ })).toBeNull()
    expect(screen.queryByRole("menuitem", { name: /Delete/ })).toBeNull()
    fireEvent.click(screen.getByRole("menuitem", { name: /Rename/ }))
    expect(callbacks.onEditFolder).toHaveBeenCalledWith("ops")
    const row = screen.getByText("Report").closest('[role="button"]')!
    act(() => (row as HTMLElement).focus())
    fireEvent.keyDown(row, { key: "F10", shiftKey: true })
    expect(screen.queryByRole("menuitem", { name: /Move to folder/ })).toBeNull()
    fireEvent.click(screen.getByRole("menuitem", { name: /Remove from folder/ }))
    expect(callbacks.onRemoveFromFolder).toHaveBeenCalledOnce()
  })
  it("preserves page-owner move/remove without granting folder administration", () => {
    show({ move: true, remove: true })
    expect(screen.queryByRole("button", { name: "Actions for folder Ops" })).toBeNull()
    const row = screen.getByText("Report").closest('[role="button"]')!
    act(() => (row as HTMLElement).focus())
    fireEvent.keyDown(row, { key: "ContextMenu" })
    expect(screen.getByRole("menuitem", { name: /Move to folder/ })).toBeTruthy()
    expect(screen.getByRole("menuitem", { name: /Remove from folder/ })).toBeTruthy()
  })
})
