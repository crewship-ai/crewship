import { beforeEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ConversationExpansion } from "../conversation-expansion"
import type { WorkspaceConversation } from "@/hooks/use-workspace-conversations"

const fetcher = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetcher }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
vi.mock("../conversation-identity", () => ({ ConversationIdentity: () => <span /> }))
const direct: WorkspaceConversation = { id: "dm", title: "Private", created_by: "alice", kind: "group", is_direct: true, access_scope: "participants", last_sequence: 3, unread_count: 0 }
const target = { ...direct, id: "new", is_direct: false }
const people = ["alice", "bob", "carol"].map((id) => ({ user: { id, full_name: id, email: `${id}@example.test` } }))
const participants = ["alice", "bob"].map((user_id) => ({ user_id, name: user_id, role: "member" }))
function setup(kind: "group" | "channel" = "group", onCreated = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return { ...render(<QueryClientProvider client={client}><ConversationExpansion workspaceId="ws" userId="alice" conversation={direct} participants={participants} kind={kind} onCreated={onCreated} /></QueryClientProvider>), onCreated }
}
function writes() { return fetcher.mock.calls.filter(([, options]) => options?.method === "POST") }
beforeEach(() => {
  sessionStorage.clear(); fetcher.mockReset()
  fetcher.mockImplementation(async (url: string) => Response.json(url.includes("/members") ? people : url.includes("/agents?") ? [{ id: "agent", name: "Mařena", slug: "ma-ena" }] : target))
})
describe("continuing a private chat", () => {
  it("includes only additional people in one guarded request, without copying history or changing the DM", async () => {
    let finish!: (value: Response) => void
    fetcher.mockImplementation(async (url: string) => url.includes("/members") ? Response.json(people) : new Promise<Response>((resolve) => { finish = resolve }))
    const { onCreated } = setup()
    fireEvent.click(await screen.findByRole("checkbox", { name: "carol" }))
    expect(screen.queryByRole("checkbox", { name: "alice" })).not.toBeInTheDocument()
    expect(screen.queryByRole("checkbox", { name: "bob" })).not.toBeInTheDocument()
    const button = screen.getByRole("button", { name: "Create group" })
    fireEvent.click(button); fireEvent.click(button)
    expect(writes()).toHaveLength(1)
    expect(writes()[0][0]).toBe("/api/v1/conversations/dm/continue?workspace_id=ws")
    expect(JSON.parse(writes()[0][1].body)).toEqual({ client_id: expect.any(String), kind: "group", title: "Group chat", member_ids: ["carol"] })
    finish(Response.json(target))
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(target))
    expect(sessionStorage.length).toBe(0)
  })
  it("reuses the exact request after an ambiguous error even when the dialog is reopened", async () => {
    let count = 0
    fetcher.mockImplementation(async (url: string) => url.includes("/members") ? Response.json(people) : ++count === 1 ? new Response(null, { status: 503 }) : Response.json(target))
    const first = setup()
    fireEvent.click(await screen.findByRole("checkbox", { name: "carol" }))
    fireEvent.click(screen.getByRole("button", { name: "Create group" }))
    await screen.findByRole("alert")
    first.unmount()
    const second = setup()
    expect(screen.getByRole("textbox", { name: "Name" })).toBeDisabled()
    fireEvent.click(screen.getByRole("button", { name: "Retry creation" }))
    await waitFor(() => expect(second.onCreated).toHaveBeenCalledWith(target))
    expect(writes()).toHaveLength(2)
    expect(writes()[0][1].body).toBe(writes()[1][1].body)
  })
  it("states workspace visibility and invites an agent atomically only after an explicit submit", async () => {
    setup("channel")
    expect(screen.getByText(/visible to everyone in this workspace/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Create workspace channel" })).toBeDisabled()
    await screen.findByRole("option", { name: "Mařena" })
    expect(writes()).toHaveLength(0)
    fireEvent.change(screen.getByRole("combobox", { name: "Agent" }), { target: { value: "agent" } })
    fireEvent.click(screen.getByRole("button", { name: "Create workspace channel" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(JSON.parse(writes()[0][1].body)).toEqual({ client_id: expect.any(String), kind: "channel", title: "Team chat", member_ids: [], agent_id: "agent" })
  })
  it("does not navigate after closing an in-flight form and keeps its retry identity", async () => {
    let finish!: (value: Response) => void
    fetcher.mockImplementation(async (url: string) => url.includes("/members") ? Response.json(people) : new Promise<Response>((resolve) => { finish = resolve }))
    const { unmount, onCreated } = setup()
    fireEvent.click(await screen.findByRole("checkbox", { name: "carol" }))
    fireEvent.click(screen.getByRole("button", { name: "Create group" }))
    unmount(); finish(Response.json(target))
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(onCreated).not.toHaveBeenCalled()
    expect(sessionStorage.length).toBe(1)
  })
})
