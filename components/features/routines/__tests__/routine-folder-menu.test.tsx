// #3768: a routine folder's own menu in the Routines panel (wireframe v67
// `foldermenu`): Rename…, Icon and colour…, New subfolder, Move to…, Delete
// folder…. Share is not offered: routine folders grant no access.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, within, waitFor } from "@testing-library/react"

import { RoutinesExplorer } from "../routines-explorer"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { RoutineFilterState } from "@/lib/routine-filters"

vi.mock("@/hooks/use-active-routine-runs", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/hooks/use-active-routine-runs")>()
  return { ...mod, useActiveRoutineRuns: () => ({ bySlug: new Map(), runs: [], activeCount: 0 }) }
})
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async () => ({ ok: true, status: 200, json: async () => ({}) })),
  broadcastSessionExpired: vi.fn(),
}))

const pipeline = (slug: string, name: string, folder_id?: string) =>
  ({
    id: slug, slug, name, dsl_version: "1.0", definition_hash: "h", ephemeral: false, workspace_visible: true,
    invocation_count: 1, last_invocation_status: "completed", authored_via: "user_api",
    created_at: "2026-10-01T00:00:00Z", updated_at: "2026-10-01T00:00:00Z", status: "active", folder_id,
  }) as Pipeline
const filters: RoutineFilterState = { status: "all", invocations: "all", authorAgentId: null, showEphemeral: false }
const list = [
  { id: "f_ops", name: "Ops", icon: "rocket", color: "blue" },
  { id: "f_daily", name: "Daily", parent_id: "f_ops" },
  { id: "f_night", name: "Nightly", parent_id: "f_daily" },
  { id: "f_arch", name: "Archive" },
]
const routines = [pipeline("standup", "Standup", "f_daily"), pipeline("loose", "Loose")]

type FolderProps = NonNullable<React.ComponentProps<typeof RoutinesExplorer>["folders"]>
let cb: { onCreate: ReturnType<typeof vi.fn>; onMove: ReturnType<typeof vi.fn>; onUpdate: ReturnType<typeof vi.fn>; onDelete: ReturnType<typeof vi.fn> }
beforeEach(() => {
  cb = {
    onCreate: vi.fn(async () => null),
    onMove: vi.fn(async () => null),
    onUpdate: vi.fn(async () => null),
    onDelete: vi.fn(async () => null),
  }
})

function renderPanel(folders: Partial<FolderProps> = cb) {
  return render(
    <RoutinesExplorer
      routines={routines}
      search=""
      onSearchChange={vi.fn()}
      selectedSlug={null}
      onSelectRoutine={vi.fn()}
      filters={filters}
      onChange={vi.fn()}
      view="overview"
      onViewChange={vi.fn()}
      counts={{ needs: null, live: null, drafts: null }}
      crews={new Map()}
      folders={{ list, ...folders } as FolderProps}
    />,
  )
}

function openMenu(folder: string) {
  fireEvent.pointerDown(screen.getByRole("button", { name: `Options for folder ${folder}` }), { button: 0, ctrlKey: false })
}

describe("routine folder menu (#3768)", () => {
  it("has the wireframe's items in order, the destructive one last, and no Share", async () => {
    renderPanel()
    openMenu("Ops")
    const menu = await screen.findByRole("menu")
    expect(within(menu).getAllByRole("menuitem").map((i) => i.textContent)).toEqual([
      "Rename…",
      "Icon and colour…",
      "New subfolder",
      "Move to…",
      "Delete folder…",
    ])
    expect(within(menu).getByText("Ops")).toBeInTheDocument()
    expect(within(menu).queryByText(/Share/)).toBeNull()
  })

  it("nests subfolders under their parent, indented, and offers no New subfolder at the deepest level", async () => {
    renderPanel()
    const labels = screen.getAllByRole("group").map((g) => g.getAttribute("aria-label")).slice(1)
    expect(labels).toEqual(["Archive", "Ops", "Daily", "Nightly", "No folder"])
    expect(document.querySelector('[data-folder-depth="2"]')).not.toBeNull()
    openMenu("Nightly")
    const menu = await screen.findByRole("menu")
    expect(within(menu).queryByRole("menuitem", { name: "New subfolder" })).toBeNull()
  })

  it("has no menu on No folder, and none without the right to change folders", () => {
    renderPanel({ onCreate: undefined, onMove: undefined, onUpdate: undefined, onDelete: undefined })
    expect(screen.queryByRole("button", { name: /Options for folder/ })).toBeNull()
    renderPanel()
    expect(screen.queryByRole("button", { name: "Options for folder No folder" })).toBeNull()
  })

  it("Rename… saves the new name and shows a refusal in the dialog", async () => {
    cb.onUpdate.mockResolvedValueOnce('a folder named "Archive" already exists').mockResolvedValueOnce(null)
    renderPanel()
    openMenu("Ops")
    fireEvent.click(await screen.findByRole("menuitem", { name: "Rename…" }))
    const name = await screen.findByRole("textbox", { name: /Name/ })
    expect(name).toHaveValue("Ops")
    fireEvent.change(name, { target: { value: "Archive" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    expect(await screen.findByText('a folder named "Archive" already exists')).toBeInTheDocument()
    fireEvent.change(screen.getByRole("textbox", { name: /Name/ }), { target: { value: "Operations" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(cb.onUpdate).toHaveBeenLastCalledWith("f_ops", { name: "Operations" }))
  })

  it("New subfolder creates the folder inside this one", async () => {
    renderPanel()
    openMenu("Ops")
    fireEvent.click(await screen.findByRole("menuitem", { name: "New subfolder" }))
    fireEvent.change(await screen.findByRole("textbox", { name: /Name/ }), { target: { value: "Weekly" } })
    fireEvent.click(screen.getByRole("button", { name: "Create folder" }))
    await waitFor(() => expect(cb.onCreate).toHaveBeenCalledWith("Weekly", "f_ops"))
  })

  it("Move to… lists only places the folder can go and moves it", async () => {
    renderPanel()
    openMenu("Daily")
    fireEvent.click(await screen.findByRole("menuitem", { name: "Move to…" }))
    const group = await screen.findByRole("radiogroup", { name: "Move to" })
    expect(within(group).getAllByRole("radio").map((r) => r.getAttribute("data-target"))).toEqual(["top", "f_arch", "f_ops"])
    expect(within(group).getByRole("radio", { name: /Ops/ })).toHaveAttribute("aria-checked", "true")
    fireEvent.click(within(group).getByRole("radio", { name: /Top level/ }))
    fireEvent.click(screen.getByRole("button", { name: "Move" }))
    await waitFor(() => expect(cb.onUpdate).toHaveBeenCalledWith("f_daily", { parent_id: null }))
  })

  it("Delete folder… asks first, says what is kept, then deletes", async () => {
    renderPanel()
    openMenu("Daily")
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete folder…" }))
    const dialog = await screen.findByRole("alertdialog")
    expect(within(dialog).getByText(/routines are kept/i)).toBeInTheDocument()
    expect(within(dialog).getByText(/subfolders move up/i)).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete folder" }))
    await waitFor(() => expect(cb.onDelete).toHaveBeenCalledWith("f_daily"))
  })
})
