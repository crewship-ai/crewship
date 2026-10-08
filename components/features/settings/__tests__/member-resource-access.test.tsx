import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { MemberResourceAccess } from "../member-resource-access"
import { PageSaveBar, PageSaveProvider } from "@/components/ui/page-save-bar"

const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
const toastError = vi.hoisted(() => vi.fn())
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: toastError } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
const policy = { membership_id: "membership-1", revision: 7, mode: "restricted", rights: [{ kind: "agent", id: "agent-1", operation: "chat" }] }
function response(body: unknown, status = 200) { return { ok: status < 400, status, json: async () => body } }
function show(role = "MEMBER") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  // Inside the page's save bar, as Settings › Members renders it.
  render(<QueryClientProvider client={client}><PageSaveProvider>
    <MemberResourceAccess workspaceId="workspace-1" memberId="membership-1" label="Client One" role={role} />
    <PageSaveBar />
  </PageSaveProvider></QueryClientProvider>)
}
const bar = () => screen.queryByRole("region", { name: "Unsaved changes" })
const saveButton = () => screen.getByRole("button", { name: "Save" })
function mockPolicy() {
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === "PUT") return response({ ...JSON.parse(String(init.body)), revision: 8 })
    if (url.includes("/members/")) return response(policy)
    return response([{ id: "agent-1", name: "Shared agent" }, { id: "agent-2", name: "Other agent" }])
  })
}
afterEach(() => { cleanup(); fetchMock.mockReset(); toastError.mockReset() })
describe("member resource access", () => {
  it("loads only the selected membership after opening and saves explicit deny-all with the original revision", async () => {
    mockPolicy(); show()
    expect(fetchMock).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Edit resource access" }))
    fireEvent.click(await screen.findByRole("button", { name: "Remove agent agent-1 chat" }))
    // The card has no Save of its own: the page bar counts the removed grant.
    expect(bar()).toHaveTextContent("1 unsaved change")
    fireEvent.click(saveButton())
    await waitFor(() => expect(fetchMock.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(true))
    const [url, init] = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT")!
    expect(url).toContain("/members/membership-1/access?workspace_id=workspace-1")
    expect(JSON.parse(init.body)).toEqual({ ...policy, rights: [] })
    // The saved revision is the new baseline: nothing left pending.
    await waitFor(() => expect(bar()).not.toHaveTextContent("unsaved"))
  })
  it("shows no Save while nothing has been edited, and Discard restores the saved grants", async () => {
    mockPolicy(); show(); fireEvent.click(screen.getByRole("button", { name: "Edit resource access" }))
    fireEvent.click(await screen.findByRole("button", { name: "Remove agent agent-1 chat" }))
    expect(bar()).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Discard" }))
    expect(bar()).toBeNull()
    expect(screen.getByRole("button", { name: "Remove agent agent-1 chat" })).toBeTruthy()
  })
  it("a failed write is a corner toast and keeps the draft", async () => {
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "PUT") return response({}, 500)
      return response(url.includes("/members/") ? policy : [{ id: "agent-1", name: "Shared agent" }])
    })
    show(); fireEvent.click(screen.getByRole("button", { name: "Edit resource access" }))
    fireEvent.click(await screen.findByRole("button", { name: "Remove agent agent-1 chat" }))
    fireEvent.click(saveButton())
    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastError.mock.calls[0][0]).toBe("Couldn’t save Resource access for Client One")
    expect(toastError.mock.calls[0][1]).toMatchObject({ description: "Resource access was not saved Your edits are kept." })
    expect(bar()).toHaveTextContent("1 unsaved change")
  })
  it("adds only an exact selected operation and prevents duplicate grants", async () => {
    mockPolicy(); show(); fireEvent.click(screen.getByRole("button", { name: "Edit resource access" }))
    await screen.findByRole("option", { name: "Other agent" })
    fireEvent.change(screen.getByLabelText("Resource"), { target: { value: "agent-2" } })
    fireEvent.change(screen.getByLabelText("Resource operation"), { target: { value: "run" } })
    fireEvent.click(screen.getByRole("button", { name: "Add grant" })); fireEvent.click(screen.getByRole("button", { name: "Add grant" }))
    expect(bar()).toHaveTextContent("1 unsaved change")
    fireEvent.click(saveButton())
    await waitFor(() => expect(fetchMock.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(true))
    const [, init] = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT")!
    expect(JSON.parse(init.body).rights).toEqual([...policy.rights, { kind: "agent", id: "agent-2", operation: "run" }])
  })
  it("keeps a rejected draft and requires explicit reload after a conflict without retrying the write", async () => {
    let current = policy
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "PUT") { current = { ...policy, revision: 9, rights: [] }; return response({}, 409) }
      return response(url.includes("/members/") ? current : [{ id: "agent-1", name: "Shared agent" }])
    })
    show(); fireEvent.click(screen.getByRole("button", { name: "Edit resource access" }))
    await screen.findByRole("option", { name: "Shared agent" })
    fireEvent.change(screen.getByLabelText("Resource operation"), { target: { value: "run" } })
    fireEvent.change(screen.getByLabelText("Resource"), { target: { value: "agent-1" } })
    fireEvent.click(screen.getByRole("button", { name: "Add grant" }))
    fireEvent.click(saveButton())
    await screen.findByText("Policy changed. Reload to discard this draft and review the current grants.")
    await waitFor(() => expect(saveButton()).toBeDisabled())
    expect(screen.getByRole("button", { name: "Remove agent agent-1 chat" })).toBeTruthy()
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "PUT")).toHaveLength(1)
    fireEvent.click(screen.getByRole("button", { name: "Reload current policy" }))
    await screen.findByText("No resource operations are granted.")
    // The reloaded policy is the new baseline; the stale draft is gone.
    await waitFor(() => expect(bar()?.textContent ?? "").not.toContain("unsaved"))
  })
  it("clears resource grants when explicitly restoring trusted access", async () => {
    mockPolicy(); show(); fireEvent.click(screen.getByRole("button", { name: "Edit resource access" }))
    await screen.findByLabelText("Access mode")
    fireEvent.change(screen.getByLabelText("Access mode"), { target: { value: "trusted" } })
    fireEvent.click(saveButton())
    await waitFor(() => expect(fetchMock.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(true))
    const [, init] = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT")!
    expect(JSON.parse(init.body)).toEqual({ ...policy, mode: "trusted", rights: [] })
  })
  it("shows names for agent and project grants whichever resource kind is selected (#2863)", async () => {
    const mixed = { ...policy, rights: [{ kind: "agent", id: "agent-1", operation: "chat" }, { kind: "project", id: "project-1", operation: "read" }] }
    fetchMock.mockImplementation(async (url: string) => {
      if (url.includes("/members/")) return response(mixed)
      if (url.startsWith("/api/v1/projects")) return response([{ id: "project-1", name: "Roadmap project" }])
      return response([{ id: "agent-1", name: "Shared agent" }])
    })
    show(); fireEvent.click(screen.getByRole("button", { name: "Edit resource access" }))
    const grants = await screen.findByRole("list", { name: "Resource grants" })
    await waitFor(() => expect(grants.textContent).toContain("agent: Shared agent · chat"))
    await waitFor(() => expect(grants.textContent).toContain("project: Roadmap project · read"))
    fireEvent.change(screen.getByLabelText("Resource kind"), { target: { value: "project" } })
    await screen.findByRole("option", { name: "Roadmap project" })
    expect(grants.textContent).toContain("agent: Shared agent · chat")
    expect(grants.textContent).toContain("project: Roadmap project · read")
  })
  it("rejects a replacement membership and provides no save control", async () => {
    fetchMock.mockResolvedValue(response({ ...policy, membership_id: "new-membership" }))
    show(); fireEvent.click(screen.getByRole("button", { name: "Edit resource access" }))
    await screen.findByRole("alert")
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull()
  })
})
