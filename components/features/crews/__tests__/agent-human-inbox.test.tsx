import { beforeEach, describe, expect, it, vi } from "vitest"
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { AgentHumanInbox } from "../agent-human-inbox"

const api = vi.fn()
const events = new Map<string, () => void>()
let accessMode = "trusted"
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => api(...args) }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "human" } } }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: (name: string, callback: () => void) => { events.set(name, callback) } }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => accessMode }))

function mount(workspaceId = "ws1") {
  const q = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const tree = (workspace: string) => <QueryClientProvider client={q}><AgentHumanInbox workspaceId={workspace} agentId="agent1" agentSlug="worker" crewId="crew1" /></QueryClientProvider>
  return { ...render(tree(workspaceId)), tree }
}
const ok = (value: unknown) => ({ ok: true, json: async () => value })
beforeEach(() => {
  api.mockReset(); events.clear(); accessMode = "trusted"
  api.mockImplementation(async (path: string) => path.includes("/conversations?") ? ok({ conversations: [{ id: "channel1", kind: "channel", title: "Existing channel" }, { id: "private", kind: "group", title: "Private human group" }] }) : ok({ items: [] }))
})

describe("agent human Inbox canonical sources", () => {
  it("hides the inbox and its composers in a workspace with the flag disabled", async () => {
    api.mockResolvedValue({ ok: false, status: 404 })
    mount()
    await waitFor(() => expect(api).toHaveBeenCalledTimes(1))
    expect(screen.queryByTestId("agent-human-inbox")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Assign task" })).not.toBeInTheDocument()
  })
  it("does not infer agent read from a running delivery or operator viewing it", async () => {
    api.mockResolvedValue(ok({ items: [{ id: "job1", kind: "channel_message", content: "A human asked this", href: "/chat?conversation=channel1", state: "RUNNING", read_at: null }] }))
    mount()
    expect(await screen.findByText("A human asked this")).toBeVisible()
    expect(screen.getByText(/Agent read not yet verified/)).toBeVisible()
    expect(api.mock.calls.every(([, options]) => !options?.method || options.method === "GET")).toBe(true)
    expect(screen.queryByText(/Agent read 20/)).not.toBeInTheDocument()
  })
  it("sends exactly one canonical message mention through the existing sender", async () => {
    mount()
    fireEvent.click(await screen.findByRole("button", { name: "Send message" }))
    await screen.findByRole("option", { name: "Existing channel" })
    expect(screen.queryByRole("option", { name: "Private human group" })).not.toBeInTheDocument()
    fireEvent.change(screen.getByRole("combobox", { name: "Workspace channel" }), { target: { value: "channel1" } })
    fireEvent.change(screen.getByRole("textbox", { name: "Message" }), { target: { value: "Please check the build" } })
    fireEvent.click(screen.getByRole("button", { name: /^Send$/ }))
    await waitFor(() => expect(api.mock.calls.filter(([, options]) => options?.method === "POST")).toHaveLength(1))
    const [path, options] = api.mock.calls.find(([, options]) => options?.method === "POST")!
    expect(path).toBe("/api/v1/conversations/channel1/messages?workspace_id=ws1")
    const body = JSON.parse(options.body)
    expect(body).toMatchObject({ content: "Please check the build", mentioned_agent_ids: ["agent1"] })
    expect(body.client_id).toEqual(expect.any(String))
    expect(options.headers["Idempotency-Key"]).toBe(body.client_id)
  })
  it("uses issue creation and start without manufacturing a run or read receipt", async () => {
    api.mockImplementation(async (path: string, options?: { method?: string }) => options?.method === "POST" && path.endsWith("/issues?workspace_id=ws1") ? ok({ identifier: "DEV-1" }) : ok({ items: [] }))
    mount()
    fireEvent.click(await screen.findByRole("button", { name: "Assign task" }))
    fireEvent.change(screen.getByRole("textbox", { name: "Task title" }), { target: { value: "Build analysis" } })
    fireEvent.change(screen.getByRole("textbox", { name: "Task instructions" }), { target: { value: "Write a short analysis" } })
    fireEvent.click(screen.getByRole("button", { name: "Create and start" }))
    await waitFor(() => expect(api.mock.calls.filter(([, options]) => options?.method === "POST")).toHaveLength(2))
    const writes = api.mock.calls.filter(([, options]) => options?.method === "POST")
    expect(writes[0][0]).toBe("/api/v1/crews/crew1/issues?workspace_id=ws1")
    expect(JSON.parse(writes[0][1].body)).toEqual({ title: "Build analysis", description: "Write a short analysis", delegate_agent_id: "agent1" })
    expect(writes[1][0]).toBe("/api/v1/crews/crew1/issues/DEV-1/start?workspace_id=ws1")
    expect(writes.some(([path]) => path.includes("/agent-inbox/read") || path.includes("/runs"))).toBe(false)
  })
  it("drops prior workspace source text immediately on a workspace switch", async () => {
    api.mockImplementation(async (path: string) => path.includes("workspace_id=ws1") ? ok({ items: [{ id: "private", kind: "chat_message", content: "PRIVATE OLD SOURCE", href: "/chat/worker?session=private", state: "running", read_at: null }] }) : new Promise(() => {}))
    const view = mount()
    expect(await screen.findByText("PRIVATE OLD SOURCE")).toBeVisible()
    view.rerender(view.tree("ws2"))
    expect(screen.queryByText("PRIVATE OLD SOURCE")).not.toBeInTheDocument()
  })
})


it("never renders an old in-flight source after an audience invalidation", async () => {
  let oldResolve: (value: unknown) => void = () => {}
  api.mockImplementationOnce(() => new Promise(resolve => { oldResolve = resolve })).mockResolvedValue(ok({ items: [] }))
  mount()
  await waitFor(() => expect(api).toHaveBeenCalledTimes(1))
  act(() => events.get("inbox.updated")?.())
  await waitFor(() => expect(api.mock.calls.length).toBeGreaterThan(1))
  await act(async () => { oldResolve(ok({ items: [{ id: "hidden", kind: "chat_message", content: "REVOKED PRIVATE BYTES", href: "/chat/worker?session=old", state: "running", read_at: null }] })); await Promise.resolve() })
  expect(screen.queryByText("REVOKED PRIVATE BYTES")).not.toBeInTheDocument()
})
it("clears visible source bytes when the account becomes restricted", async () => {
  api.mockResolvedValue(ok({ items: [{ id: "old", kind: "chat_message", content: "PRIOR TRUSTED SOURCE", href: "/chat/worker", state: "running", read_at: null }] }))
  const view = mount()
  expect(await screen.findByText("PRIOR TRUSTED SOURCE")).toBeVisible()
  accessMode = "restricted"; view.rerender(view.tree("ws1"))
  expect(screen.queryByText("PRIOR TRUSTED SOURCE")).not.toBeInTheDocument()
  expect(screen.getByText("Human messages are unavailable in this session.")).toBeVisible()
})

it("recovers a lost start response by observing the same authorized issue without another write", async () => {
  api.mockImplementation(async (path: string, options?: { method?: string }) => {
    if (options?.method === "POST" && path.includes("/issues?")) return ok({ identifier: "DEV-2" })
    if (options?.method === "POST" && path.includes("/start?")) throw new TypeError("Connection closed after commit")
    if (path.includes("/issues/DEV-2?")) return ok({ status: "IN_PROGRESS", delegate_agent_id: "agent1" })
    return ok({ items: [] })
  })
  mount()
  fireEvent.click(await screen.findByRole("button", { name: "Assign task" }))
  fireEvent.change(screen.getByRole("textbox", { name: "Task title" }), { target: { value: "Canonical task" } })
  fireEvent.change(screen.getByRole("textbox", { name: "Task instructions" }), { target: { value: "Original context" } })
  fireEvent.click(screen.getByRole("button", { name: "Create and start" }))
  expect(await screen.findByRole("status")).toHaveTextContent("Existing state observed; start acknowledgement unavailable")
  expect(api.mock.calls.filter(([, options]) => options?.method === "POST")).toHaveLength(2)
  expect(api.mock.calls.filter(([path]) => path.includes("/issues/DEV-2?"))).toHaveLength(1)
})

it("never masks revoked start authority with an issue readback", async () => {
  api.mockImplementation(async (path: string, options?: { method?: string }) => {
    if (options?.method === "POST" && path.includes("/issues?")) return ok({ identifier: "DEV-3" })
    if (options?.method === "POST" && path.includes("/start?")) return { ok: false, status: 403, json: async () => ({ error: "Requester revoked" }) }
    return ok({ items: [] })
  })
  mount()
  fireEvent.click(await screen.findByRole("button", { name: "Assign task" }))
  fireEvent.change(screen.getByRole("textbox", { name: "Task title" }), { target: { value: "Canonical task" } })
  fireEvent.change(screen.getByRole("textbox", { name: "Task instructions" }), { target: { value: "Original context" } })
  fireEvent.click(screen.getByRole("button", { name: "Create and start" }))
  expect(await screen.findByRole("alert")).toHaveTextContent("Requester revoked")
  expect(api.mock.calls.some(([path]) => path.includes("/issues/DEV-3?"))).toBe(false)
  expect(screen.queryByRole("status")).not.toBeInTheDocument()
})
