import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { RoutineActionsMenu, duplicateDraftDocument } from "../routine-actions-menu"
import type { RoutineDetail } from "../routines-detail-panel"

const h = vi.hoisted(() => ({ fetcher: vi.fn(), role: "ADMIN" }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetcher }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: h.role, capabilities: [] }) }))

const routine = {
  id: "p1",
  slug: "weekly-scan",
  name: "Weekly scan",
  description: "Scans",
  definition: { name: "weekly-scan", display_name: "Weekly scan", steps: [{ id: "a", type: "agent_run" }] },
  definition_hash: "h",
  dsl_version: "1.0",
  ephemeral: false,
  workspace_visible: true,
  invocation_count: 0,
  authored_via: "ui",
  created_at: "",
  updated_at: "",
  head_version: 3,
  author_crew_id: "crew_1",
} as unknown as RoutineDetail

const json = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body, clone() { return this }, text: async () => JSON.stringify(body) })

function openMenu() {
  fireEvent.pointerDown(screen.getByRole("button", { name: "Routine options" }), { button: 0, ctrlKey: false })
}

beforeEach(() => {
  h.fetcher.mockReset()
  h.role = "ADMIN"
})

describe("duplicateDraftDocument", () => {
  it("renames the definition to the new ID and keeps the crew", () => {
    expect(duplicateDraftDocument(routine, { slug: "weekly-scan-copy", name: "Weekly scan (copy)" })).toEqual({
      slug: "weekly-scan-copy",
      name: "Weekly scan (copy)",
      description: "Scans",
      definition: { name: "weekly-scan-copy", display_name: "Weekly scan (copy)", steps: [{ id: "a", type: "agent_run" }] },
      author_crew_id: "crew_1",
    })
  })
})

describe("<RoutineActionsMenu>", () => {
  it("Duplicate as draft saves a draft, never a live routine", async () => {
    h.fetcher.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.endsWith("/pipelines/drafts") && init?.method === "POST") return json({ ...JSON.parse(String(init.body)), id: "d1", revision: 1 })
      throw new Error(`unexpected ${url}`)
    })
    const open = vi.fn()
    render(<RoutineActionsMenu routine={routine} workspaceId="ws" onChanged={vi.fn()} onOpenRoutine={open} />)
    openMenu()
    fireEvent.click(await screen.findByRole("menuitem", { name: "Duplicate as draft" }))
    expect(screen.getByLabelText("ID", { exact: false })).toHaveValue("weekly-scan-copy")
    fireEvent.click(screen.getByRole("button", { name: "Save as draft" }))
    await waitFor(() => expect(open).toHaveBeenCalledWith("weekly-scan-copy"))
    const urls = h.fetcher.mock.calls.map(([u]) => String(u))
    expect(urls.some((u) => u.endsWith("/pipelines/save"))).toBe(false)
    const body = JSON.parse(String(h.fetcher.mock.calls[0][1].body))
    expect(body).toMatchObject({ id: "", revision: 0, slug: "weekly-scan-copy", document: { slug: "weekly-scan-copy", author_crew_id: "crew_1" } })
  })

  it("a name clash is explained in words", async () => {
    h.fetcher.mockResolvedValue(json({ error: "exists" }, 409))
    render(<RoutineActionsMenu routine={routine} workspaceId="ws" onChanged={vi.fn()} />)
    openMenu()
    fireEvent.click(await screen.findByRole("menuitem", { name: "Duplicate as draft" }))
    fireEvent.click(screen.getByRole("button", { name: "Save as draft" }))
    expect(await screen.findByText(/A routine or draft called “weekly-scan-copy” already exists/)).toBeInTheDocument()
  })

  it("Delete routine asks ConfirmDialog and calls DELETE (Admin)", async () => {
    h.fetcher.mockResolvedValue(json({}))
    const deleted = vi.fn()
    render(<RoutineActionsMenu routine={routine} workspaceId="ws" onChanged={vi.fn()} onDeleted={deleted} />)
    openMenu()
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete routine…" }))
    const dialog = await screen.findByTestId("confirm-dialog")
    expect(dialog).toHaveTextContent("Runs already recorded stay readable in Activity.")
    const confirm = screen.getByRole("button", { name: "Delete routine" })
    expect(confirm).toBeDisabled()
    fireEvent.change(screen.getByLabelText(/to confirm/), { target: { value: "weekly-scan" } })
    fireEvent.click(confirm)
    await waitFor(() => expect(deleted).toHaveBeenCalled())
    expect(h.fetcher).toHaveBeenCalledWith("/api/v1/workspaces/ws/pipelines/weekly-scan", { method: "DELETE" })
  })

  it("hides Turn off and Delete for a Manager", async () => {
    h.role = "MANAGER"
    render(<RoutineActionsMenu routine={routine} workspaceId="ws" onChanged={vi.fn()} onGovernance={vi.fn()} />)
    openMenu()
    expect(await screen.findByRole("menuitem", { name: "Duplicate as draft" })).toBeInTheDocument()
    expect(screen.queryByRole("menuitem", { name: "Delete routine…" })).toBeNull()
    expect(screen.queryByRole("menuitem", { name: "Turn off" })).toBeNull()
  })

  it("a Member gets no Duplicate; everyone can copy the CLI command", async () => {
    h.role = "MEMBER"
    render(<RoutineActionsMenu routine={routine} workspaceId="ws" onChanged={vi.fn()} />)
    openMenu()
    expect(await screen.findByRole("menuitem", { name: "Copy CLI command" })).toBeInTheDocument()
    expect(screen.queryByRole("menuitem", { name: "Duplicate as draft" })).toBeNull()
  })
})
