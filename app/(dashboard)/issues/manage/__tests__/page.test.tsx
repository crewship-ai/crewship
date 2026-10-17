import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import Page from "../page"

const state = vi.hoisted(() => ({ params: "project=p1", user: "client-a", workspace: "w1", mode: "restricted", unavailable: false, fetch: vi.fn() }))
vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams(state.params), useRouter: () => ({ push: vi.fn() }) }))
vi.mock("@/hooks/use-auth", () => ({ useSession: () => ({ data: { user: { id: state.user } } }) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: state.workspace, workspaceUnavailable: state.unavailable }) }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => state.mode }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: state.fetch }))
vi.mock("@/components/features/issues/shared-project-milestones", () => ({ SharedProjectMilestones: ({ projectId }: { projectId: string }) => <div data-testid="milestones">{projectId}</div> }))
const detail = { id: "p1", name: "Client A project", description: "Private A brief", version: "a".repeat(64), can_manage: true }
const response = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body })
function show(client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 60_000 }, mutations: { retry: false } } })) {
  const view = render(<QueryClientProvider client={client}><Page /></QueryClientProvider>)
  return { client, rerender: () => view.rerender(<QueryClientProvider client={client}><Page /></QueryClientProvider>) }
}
beforeEach(() => { state.params = "project=p1"; state.user = "client-a"; state.workspace = "w1"; state.mode = "restricted"; state.unavailable = false; state.fetch.mockReset().mockResolvedValue(response(detail)) })
afterEach(cleanup)

describe("known project management", () => {
  it("loads only the known project without a selector or issue discovery", async () => {
    show()
    expect(await screen.findByLabelText("Project name")).toHaveValue(detail.name)
    expect(screen.queryByLabelText("Shared project")).toBeNull()
    expect(screen.getByTestId("milestones")).toHaveTextContent("p1")
    expect(state.fetch.mock.calls.map(([path]) => path)).toEqual(["/api/v1/workspaces/w1/restricted-projects/p1"])
  })
  it.each(["", "project=", "project=.", "project=..", "project=p1&project=p2", "project=p1&workspace_id=w2", "project=p1&search=hidden", "project=p1&issue_id=", "project=p1&issue_id=..", "project=p1&issue_id=i1&issue_id=i2"])("rejects ambiguous or unsupported resource intent %s", async params => {
    state.params = params; show()
    expect(await screen.findByRole("alert")).toHaveTextContent("known project")
    expect(state.fetch).not.toHaveBeenCalled()
  })
  it("never renders an unconfirmed or foreign project response", async () => {
    state.fetch.mockResolvedValue(response({ ...detail, id: "p2", name: "Foreign secret" }))
    show(); await screen.findByRole("alert")
    expect(screen.queryByText("Foreign secret")).toBeNull()
    expect(screen.queryByLabelText("Project name")).toBeNull()
    expect(screen.queryByTestId("milestones")).toBeNull()
  })
  it("revalidates a warm cache before displaying protected fields or milestones", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 60_000 } } })
    client.setQueryData(["restricted-project-details", "client-a", "w1", "p1"], detail)
    let resolve!: (value: ReturnType<typeof response>) => void
    state.fetch.mockReturnValue(new Promise(done => { resolve = done }))
    show(client)
    expect(screen.queryByLabelText("Project name")).toBeNull()
    expect(screen.queryByTestId("milestones")).toBeNull()
    await waitFor(() => expect(state.fetch).toHaveBeenCalledOnce())
    await act(async () => resolve(response({}, 404)))
    await screen.findByRole("alert")
    expect(screen.queryByText(detail.description)).toBeNull()
  })
  it("keys protected drafts by user, workspace and project", async () => {
    const view = show(); const name = await screen.findByLabelText("Project name")
    fireEvent.change(name, { target: { value: "A unsaved draft" } })
    state.params = "project=p2"; state.fetch.mockResolvedValue(response({ ...detail, id: "p2", name: "B known project" })); view.rerender()
    expect(screen.queryByDisplayValue("A unsaved draft")).toBeNull()
    expect(await screen.findByLabelText("Project name")).toHaveValue("B known project")
    state.user = "client-b"; state.workspace = "w2"; state.fetch.mockResolvedValue(response({}, 404)); view.rerender()
    expect(screen.queryByDisplayValue("B known project")).toBeNull()
    await screen.findByRole("alert")
    expect(state.fetch.mock.calls.at(-1)?.[0]).toBe("/api/v1/workspaces/w2/restricted-projects/p2")
  })
  it("respects current credential hints without inventing management rights", async () => {
    state.fetch.mockResolvedValue(response({ ...detail, can_manage: false })); show()
    await screen.findByText(detail.description)
    expect(screen.queryByRole("button", { name: "Save project details" })).toBeNull()
  })
  it("does not mount protected data while workspace or account authority is unavailable", async () => {
    state.unavailable = true; show()
    await screen.findByRole("alert"); expect(state.fetch).not.toHaveBeenCalled()
  })
  it("preserves an existing draft through temporary workspace-directory loss", async () => {
    const view = show()
    fireEvent.change(await screen.findByLabelText("Project name"), { target: { value: "Unsubmitted local draft" } })
    state.unavailable = true; state.mode = "trusted"; view.rerender()
    state.unavailable = false; state.mode = "restricted"; view.rerender()
    expect(await screen.findByLabelText("Project name")).toHaveValue("Unsubmitted local draft")
  })
  it("opens only the explicit known issue deletion preview without List", async () => {
    state.params = "project=p1&issue_id=i1"
    state.fetch.mockImplementation(async (path: string) => response(path.endsWith("/deletion")
      ? { id: "i1", identifier: "CLIENT-1", title: "Known request", project_id: "p1", status: "BACKLOG", version: "b".repeat(64), can_delete: true }
      : detail))
    show()
    expect(await screen.findByRole("button", { name: "Delete issue" })).toBeEnabled()
    expect(state.fetch.mock.calls.map(([path]) => path)).toEqual([
      "/api/v1/workspaces/w1/restricted-projects/p1",
      "/api/v1/workspaces/w1/restricted-issues/i1/deletion",
    ])
  })
  it("deletes the reviewed known issue while retaining the project draft", async () => {
    state.params = "project=p1&issue_id=i1"
    const preview = { id: "i1", identifier: "CLIENT-1", title: "Known request", project_id: "p1", status: "BACKLOG", version: "b".repeat(64), can_delete: true }
    state.fetch.mockImplementation(async (path: string, init?: RequestInit) => response(init?.method === "DELETE" ? null : path.endsWith("/deletion") ? preview : detail, init?.method === "DELETE" ? 204 : 200))
    show()
    fireEvent.change(await screen.findByLabelText("Project name"), { target: { value: "Retained project draft" } })
    fireEvent.click(await screen.findByRole("button", { name: "Delete issue" }))
    await screen.findByRole("alertdialog", { name: "Delete CLIENT-1?" })
    fireEvent.click(screen.getByRole("button", { name: "Permanently delete issue" }))
    await screen.findByText("Known issue deleted.")
    expect(screen.getByLabelText("Project name")).toHaveValue("Retained project draft")
    expect(screen.queryByRole("button", { name: "Delete issue" })).toBeNull()
    const writes = state.fetch.mock.calls.filter(([, init]) => init?.method === "DELETE")
    expect(writes).toHaveLength(1)
    expect(writes[0][0]).toBe("/api/v1/workspaces/w1/restricted-issues/i1")
    expect(JSON.parse(writes[0][1].body)).toEqual({ project_id: "p1", version: preview.version, confirm: true })
  })
  it.each([401, 403, 404].flatMap(status => [429, 500].map(transient => ({ status, transient }))))("keeps acknowledged loss hidden through $status then $transient", async ({ status, transient }) => {
    state.params = "project=p1&issue_id=i1"
    const preview = { id: "i1", identifier: "CLIENT-1", title: "Known request", project_id: "p1", status: "BACKLOG", version: "b".repeat(64), can_delete: true }
    state.fetch.mockImplementation(async (path: string) => response(path.endsWith("/deletion") ? preview : detail))
    const view = show()
    await screen.findByLabelText("Project name")
    expect(await screen.findByRole("button", { name: "Delete issue" })).toBeEnabled()
    state.fetch.mockResolvedValue(response({}, status))
    await act(async () => { await view.client.invalidateQueries({ queryKey: ["restricted-project-details"] }) })
    await screen.findByRole("alert")
    for (const next of [transient]) {
      state.fetch.mockResolvedValue(response({}, next))
      await act(async () => { await view.client.invalidateQueries({ queryKey: ["restricted-project-details"] }) })
      await waitFor(() => expect(view.client.getQueryState(["restricted-project-details", "client-a", "w1", "p1"])?.error).toMatchObject({ status: next }))
      view.rerender() // Force the current error/cache snapshot, not the prior 404 frame.
      expect(screen.queryByLabelText("Project name")).toBeNull()
      expect(screen.queryByTestId("milestones")).toBeNull()
      expect(screen.queryByRole("button", { name: "Delete issue" })).toBeNull()
    }
    // Even a delayed mutation/cache success is not a fresh GET authority check.
    await act(async () => { view.client.setQueryData(["restricted-project-details", "client-a", "w1", "p1"], detail) })
    view.rerender()
    expect(screen.queryByLabelText("Project name")).toBeNull()
    expect(screen.queryByTestId("milestones")).toBeNull()
    expect(screen.queryByRole("button", { name: "Delete issue" })).toBeNull()
    state.fetch.mockImplementation(async (path: string) => response(path.endsWith("/deletion") ? preview : { ...detail, name: "Freshly checked project" }))
    await act(async () => { await view.client.invalidateQueries({ queryKey: ["restricted-project-details"] }) })
    expect(await screen.findByLabelText("Project name")).toHaveValue("Freshly checked project")
    expect(screen.getByTestId("milestones")).toHaveTextContent("p1")
    expect(await screen.findByRole("button", { name: "Delete issue" })).toBeEnabled()
  })
  it.each([200, 404])("ignores an aborted prior project response (%s)", async status => {
    let resolve!: (value: ReturnType<typeof response>) => void
    let signal!: AbortSignal
    state.fetch.mockImplementationOnce((_path: string, init: { signal: AbortSignal }) => { signal = init.signal; return new Promise(done => { resolve = done }) })
    const view = show()
    await waitFor(() => expect(state.fetch).toHaveBeenCalledOnce())
    state.params = "project=p2"
    state.fetch.mockResolvedValue(response({ ...detail, id: "p2", name: "Current project" }))
    view.rerender()
    expect(await screen.findByLabelText("Project name")).toHaveValue("Current project")
    expect(signal.aborted).toBe(true)
    await act(async () => resolve(response(detail, status)))
    view.rerender()
    expect(screen.getByLabelText("Project name")).toHaveValue("Current project")
    expect(screen.getByTestId("milestones")).toHaveTextContent("p2")
  })
  it("does not admit a new resource while the directory is unavailable", async () => {
    const view = show()
    await screen.findByLabelText("Project name")
    const previousCalls = state.fetch.mock.calls.length
    state.unavailable = true; state.params = "project=p2"; view.rerender()
    expect(screen.queryByLabelText("Project name")).toBeNull()
    expect(state.fetch).toHaveBeenCalledTimes(previousCalls)
  })
  it("does not trust a warm deletion preview before its current check", async () => {
    state.params = "project=p1&issue_id=i1"
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 60_000 } } })
    const path = "/api/v1/workspaces/w1/restricted-issues/i1"
    client.setQueryData(["restricted-issue-deletion", "client-a", path, "p1"], { id: "i1", identifier: "STALE-1", title: "Old cached issue", project_id: "p1", status: "BACKLOG", version: "b".repeat(64), can_delete: true })
    let resolve!: (value: ReturnType<typeof response>) => void
    state.fetch.mockImplementation(async (request: string) => request.endsWith("/deletion") ? new Promise(done => { resolve = done }) : response(detail))
    show(client)
    await waitFor(() => expect(state.fetch).toHaveBeenCalledTimes(2))
    expect(screen.queryByRole("button", { name: "Delete issue" })).toBeNull()
    await act(async () => resolve(response({}, 404)))
    expect(screen.queryByText("Old cached issue")).toBeNull()
    expect(screen.queryByRole("button", { name: "Delete issue" })).toBeNull()
  })
  it("rejects a known issue preview from another managed project", async () => {
    state.params = "project=p1&issue_id=i1"
    state.fetch.mockImplementation(async (path: string) => response(path.endsWith("/deletion") ? { id: "i1", identifier: "FOREIGN-1", title: "Foreign request", project_id: "p2", status: "BACKLOG", version: "b".repeat(64), can_delete: true } : detail))
    show()
    await screen.findByText(/issue project changed/)
    expect(screen.queryByText("Foreign request")).toBeNull()
    expect(screen.queryByRole("button", { name: "Delete issue" })).toBeNull()
  })
  it.each([false, true])("ignores a late save after known loss (fresh read: %s)", async fresh => {
    let resolveSave!: (value: ReturnType<typeof response>) => void
    state.fetch.mockImplementation(async (_path: string, init?: RequestInit) => init?.method === "PUT" ? new Promise(done => { resolveSave = done }) : response(detail))
    const view = show()
    fireEvent.change(await screen.findByLabelText("Project name"), { target: { value: "Pending client edit" } })
    await waitFor(() => expect(screen.getByRole("button", { name: "Save project details" })).toBeEnabled())
    fireEvent.click(screen.getByRole("button", { name: "Save project details" }))
    await waitFor(() => expect(resolveSave).toBeDefined())
    state.fetch.mockResolvedValue(response({}, 404))
    await act(async () => { await view.client.invalidateQueries({ queryKey: ["restricted-project-details"] }) })
    await waitFor(() => expect(screen.queryByLabelText("Project name")).toBeNull())
    if (fresh) {
      state.fetch.mockResolvedValue(response({ ...detail, name: "Fresh readonly project", can_manage: false, version: "c".repeat(64) }))
      await act(async () => { await view.client.invalidateQueries({ queryKey: ["restricted-project-details"] }) })
      await screen.findByText("Fresh readonly project")
    }
    await act(async () => resolveSave(response({ ...detail, name: "Late stale save", version: "b".repeat(64) })))
    view.rerender()
    expect(screen.queryByLabelText("Project name")).toBeNull()
    expect(screen.queryByDisplayValue("Late stale save")).toBeNull()
    if (fresh) expect(screen.getByText("Fresh readonly project")).toBeInTheDocument()
  })
})
