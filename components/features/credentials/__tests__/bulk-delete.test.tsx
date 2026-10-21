import { describe, expect, it, vi, beforeEach } from "vitest"
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

import { BulkDeleteDialog, BulkTierDialog } from "../bulk-actions"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body }) as unknown as Response
const fail = (status: number) => ({ ok: false, status, json: async () => ({}) }) as unknown as Response

const TARGETS = [
  { id: "a", name: "ALPHA", agent_names: ["Scout"] },
  { id: "b", name: "BETA", agent_names: ["Scout", "Writer"] },
  { id: "c", name: "GAMMA", agent_names: [] },
]

beforeEach(() => h.apiFetch.mockReset())

describe("bulk delete", () => {
  it("count only visible selection, dialog lists dependents, stays open on 4xx", async () => {
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "DELETE") return String(url).includes("/credentials/b?") ? fail(403) : ok({})
      if (String(url).includes("/a/dependents")) return ok({ routines: [{ slug: "nightly", name: "Nightly backlog triage" }] })
      return ok({ routines: [] })
    })
    const onDone = vi.fn()
    const onOpenChange = vi.fn()
    // The page passes only the visible, ticked rows: three here.
    render(<BulkDeleteDialog open onOpenChange={onOpenChange} workspaceId="ws1" targets={TARGETS} onDone={onDone} />)
    const dialog = screen.getByRole("alertdialog")
    expect(within(dialog).getByText("Delete 3 secrets?")).toBeInTheDocument()
    expect(within(dialog).getByText(/2 agents lose access at the next run: Scout, Writer/)).toBeInTheDocument()
    expect(await within(dialog).findByText(/1 routine reference them and may fail: Nightly backlog triage/)).toBeInTheDocument()

    await act(async () => fireEvent.click(within(dialog).getByRole("button", { name: "Delete 3" })))
    await waitFor(() => expect(onDone).toHaveBeenCalledWith(["b"]))
    // A refusal keeps the dialog: it never closes over a secret still there.
    expect(onOpenChange).not.toHaveBeenCalledWith(false)
    expect(screen.getByRole("alertdialog")).toBeInTheDocument()
  })

  it("treats a 404 as already deleted and closes", async () => {
    h.apiFetch.mockImplementation(async (_url: string, init?: RequestInit) => (init?.method === "DELETE" ? fail(404) : ok({ routines: [] })))
    const onDone = vi.fn()
    const onOpenChange = vi.fn()
    render(<BulkDeleteDialog open onOpenChange={onOpenChange} workspaceId="ws1" targets={[TARGETS[2]]} onDone={onDone} />)
    expect(await screen.findByText("No visible routine references them.")).toBeInTheDocument()
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Delete" })))
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
    expect(onDone).toHaveBeenCalledWith([])
  })

  it("says when routine use could not be checked instead of claiming none", async () => {
    h.apiFetch.mockResolvedValue(fail(500))
    render(<BulkDeleteDialog open onOpenChange={() => {}} workspaceId="ws1" targets={[TARGETS[0]]} onDone={() => {}} />)
    expect(await screen.findByText(/could not be checked for 1 of them/)).toBeInTheDocument()
    expect(screen.queryByText("No visible routine references them.")).not.toBeInTheDocument()
  })
})

describe("bulk tier", () => {
  it("PATCHes security_level for each selected secret", async () => {
    h.apiFetch.mockResolvedValue(ok({}))
    const onDone = vi.fn()
    render(<BulkTierDialog open onOpenChange={() => {}} workspaceId="ws1" targets={TARGETS.slice(0, 2)} onDone={onDone} />)
    fireEvent.click(screen.getByRole("radio", { name: "L3" }))
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Set L3" })))
    await waitFor(() => expect(onDone).toHaveBeenCalledWith([]))
    const patches = h.apiFetch.mock.calls.filter(([, o]) => (o as RequestInit).method === "PATCH")
    expect(patches.map(([u]) => String(u))).toEqual(["/api/v1/credentials/a?workspace_id=ws1", "/api/v1/credentials/b?workspace_id=ws1"])
    expect(JSON.parse(String((patches[0][1] as RequestInit).body))).toEqual({ security_level: 3 })
  })
})
