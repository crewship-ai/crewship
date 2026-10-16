import React from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import type { PageFolderView } from "@/hooks/use-page-folders"

const state = vi.hoisted(() => ({
  folders: [] as PageFolderView[],
  create: vi.fn(),
  reparent: vi.fn(),
}))
vi.mock("@/hooks/use-page-folders", async (original) => ({
  ...await original<typeof import("@/hooks/use-page-folders")>(),
  usePageFolders: () => ({ folders: state.folders, loading: false }),
  useFolderOwnerChoices: () => ({ data: [{ slug: "ops", name: "Ops" }], isPending: false }),
  usePageFolderMutations: () => ({
    create: { mutateAsync: state.create, isPending: false },
    update: { isPending: false },
    reparent: { mutateAsync: state.reparent, isPending: false },
  }),
}))
import { FolderEditDialog, ReparentFolderDialog } from "@/components/features/pages/folder-dialogs"

function folder(id: string): PageFolderView {
  return {
    id, slug: id, name: id, icon: null, color: null, ownerRef: "crew/ops", ownerLabel: "Ops",
    pageCount: 0, createdAt: null, updatedAt: null, aclVersion: 2, treeVersion: 3, canReparent: true, canAcceptFolders: true,
    shared: "none", canRename: true, canShare: true, canDelete: true, canAcceptPages: true,
  }
}

beforeEach(() => {
  state.folders = [folder("source"), folder("target")]
  state.create.mockReset().mockResolvedValue(folder("created"))
  state.reparent.mockReset().mockResolvedValue({ tree_version: 5 })
})
afterEach(cleanup)

describe("folder tree intent fences", () => {
  it("blocks a reparent after background tree/ACL changes until a separate review and confirm", () => {
    const close = vi.fn()
    const view = render(<ReparentFolderDialog workspaceId="ws" folder={state.folders[0]} onOpenChange={close} />)
    fireEvent.change(screen.getByLabelText("New parent"), { target: { value: "target" } })
    state.folders = state.folders.map((item) => ({ ...item, treeVersion: 4, aclVersion: 7 }))
    view.rerender(<ReparentFolderDialog workspaceId="ws" folder={state.folders[0]} onOpenChange={close} />)
    const confirm = screen.getByRole("button", { name: "Move folder", exact: true })
    expect((confirm as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(confirm)
    expect(state.reparent).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Review updated folders" }))
    expect(state.reparent).not.toHaveBeenCalled()
    fireEvent.click(confirm)
    expect(state.reparent).toHaveBeenCalledWith({ slug: "source", parent: "target", treeVersion: 4, aclVersion: 7 })
  })

  it("does not silently replace a nested-create parent identity or fences", () => {
    const props = { workspaceId: "ws", open: true, folder: null, onOpenChange: vi.fn(), defaultOwner: "crew/ops" }
    const view = render(<FolderEditDialog {...props} />)
    fireEvent.change(screen.getByRole("textbox", { name: /^Name/ }), { target: { value: "Child" } })
    fireEvent.change(screen.getByLabelText("Parent folder"), { target: { value: "target" } })
    state.folders = state.folders.map((item) => item.slug === "target"
      ? { ...item, id: "replacement", treeVersion: 4, aclVersion: 9 } : item)
    view.rerender(<FolderEditDialog {...props} />)
    const confirm = screen.getByRole("button", { name: "Create folder", exact: true })
    expect((confirm as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(confirm)
    expect(state.create).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Review updated folders" }))
    expect(state.create).not.toHaveBeenCalled()
    fireEvent.click(confirm)
    expect(state.create).toHaveBeenCalledWith({ name: "Child", owner: "crew/ops", icon: null, color: null,
      parent: "target", treeVersion: 4, aclVersion: 9 })
  })

  it("blocks a selected target that disappears without disclosing its former authority", () => {
    const props = { workspaceId: "ws", folder: state.folders[0], onOpenChange: vi.fn() }
    const view = render(<ReparentFolderDialog {...props} />)
    fireEvent.change(screen.getByLabelText("New parent"), { target: { value: "target" } })
    state.folders = [state.folders[0]]
    view.rerender(<ReparentFolderDialog {...props} />)
    expect((screen.getByRole("button", { name: "Move folder", exact: true }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole("button", { name: "Review updated folders" }) as HTMLButtonElement).disabled).toBe(true)
    expect(state.reparent).not.toHaveBeenCalled()
  })
})
