import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { TriggersTab } from "../triggers-tab"
import type { TriggerType } from "../types"

const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
const trigger = (slug = "NEW_MESSAGE"): TriggerType => ({ slug, name: `Trigger ${slug}`, description: "New message", type: "webhook", toolkit: { slug: "gmail" } })
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
const active = (name: string) => ({ id: name, trigger_name: name, user_id: "alice", connected_account_id: "account" })
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((done) => { resolve = done }); return { promise, resolve } }
beforeEach(() => { api.mockReset() })
afterEach(cleanup)
function defaultReads(url: string) { return Promise.resolve(json({ triggers: url.includes("/active?") ? [] : [trigger()] })) }
async function modal(users = ["alice", "bob"]) {
  render(<TriggersTab workspaceId="a" users={users} />)
  fireEvent.click(await screen.findByRole("button", { name: "Enable" }))
}

it("renders live and disabled subscriptions separately from available trigger types", async () => {
  api.mockImplementation((url: string) => Promise.resolve(json({ triggers: url.includes("/active?") ? [active("live-event"), { ...active("disabled-event"), disabled_at: "yesterday" }] : [trigger()] })))
  render(<TriggersTab workspaceId="a" users={["alice"]} />)
  expect(await screen.findByText("live-event")).toBeVisible()
  expect(screen.getByText("● disabled")).toBeVisible()
  expect(screen.getByText("● live")).toBeVisible()
  expect(await screen.findByText("Trigger NEW_MESSAGE")).toBeVisible()
  expect(screen.getByRole("link", { name: "Incoming webhooks" })).toHaveAttribute("href", "/integrations?tab=incoming")
})

it("debounces trimmed toolkit and search filters into a scoped request", async () => {
  api.mockImplementation(defaultReads)
  render(<TriggersTab workspaceId="a & b" users={[]} />)
  fireEvent.change(screen.getByPlaceholderText("Filter by toolkit (gmail…)"), { target: { value: " gmail " } })
  fireEvent.change(screen.getByPlaceholderText("Search trigger types…"), { target: { value: " new & old " } })
  await screen.findByText("Trigger NEW_MESSAGE")
  expect(api).toHaveBeenCalledWith("/api/v1/integrations/composio/triggers?workspace_id=a+%26+b&toolkit=gmail&search=new+%26+old", expect.anything())
  expect(api.mock.calls.filter(([url]) => !url.includes("/active?")).length).toBe(1)
})

it("does not request an unselected workspace", async () => {
  api.mockImplementation(defaultReads)
  render(<TriggersTab workspaceId="" users={[]} />)
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 350)) })
  expect(api).not.toHaveBeenCalled()
})

it("clears former subscriptions, available types and an open enable dialog on workspace change", async () => {
  const next = deferred<Response>()
  api.mockImplementation((url: string) => url.endsWith("=b") ? next.promise : Promise.resolve(json({ triggers: url.includes("/active?") ? [active("old-active")] : [trigger("OLD")] })))
  const view = render(<TriggersTab workspaceId="a" users={["alice"]} />)
  fireEvent.click(await screen.findByRole("button", { name: "Enable" }))
  view.rerender(<TriggersTab workspaceId="b" users={["bob"]} />)
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  expect(screen.queryByText("old-active")).not.toBeInTheDocument()
  expect(screen.queryByText("Trigger OLD")).not.toBeInTheDocument()
  await act(async () => next.resolve(json({ triggers: [] })))
})

it("ignores delayed active subscriptions from a former workspace", async () => {
  const old = deferred<Response>()
  api.mockImplementation((url: string) => url.includes("/active?workspace_id=a") ? old.promise : Promise.resolve(json({ triggers: url.includes("/active?") ? [active("current-active")] : [] })))
  const view = render(<TriggersTab workspaceId="a" users={[]} />)
  view.rerender(<TriggersTab workspaceId="b" users={[]} />)
  await screen.findByText("current-active")
  await act(async () => old.resolve(json({ triggers: [active("old-active")] })))
  expect(screen.getByText("current-active")).toBeVisible()
  expect(screen.queryByText("old-active")).not.toBeInTheDocument()
})

it("ignores an older available-types response after search changed", async () => {
  const old = deferred<Response>()
  api.mockImplementation((url: string) => url.includes("/active?") ? Promise.resolve(json({ triggers: [] })) : url.includes("search=") ? Promise.resolve(json({ triggers: [trigger("CURRENT")] })) : old.promise)
  render(<TriggersTab workspaceId="a" users={[]} />)
  await waitFor(() => expect(api).toHaveBeenCalledWith("/api/v1/integrations/composio/triggers?workspace_id=a", expect.anything()))
  fireEvent.change(screen.getByPlaceholderText("Search trigger types…"), { target: { value: "current" } })
  await screen.findByText("Trigger CURRENT")
  await act(async () => old.resolve(json({ triggers: [trigger("OLD")] })))
  expect(screen.getByText("Trigger CURRENT")).toBeVisible()
  expect(screen.queryByText("Trigger OLD")).not.toBeInTheDocument()
})

it("does not claim there are no subscriptions when the active read fails", async () => {
  api.mockImplementation((url: string) => Promise.resolve(url.includes("/active?") ? json({}, 503) : json({ triggers: [] })))
  render(<TriggersTab workspaceId="a" users={[]} />)
  expect(await screen.findByRole("alert")).toHaveTextContent("Could not load active triggers")
  expect(screen.queryByText(/No active triggers yet/)).not.toBeInTheDocument()
})

it.each(["refused", "non-error", "network"])("accounts for %s trigger-type reads", async (failure) => {
  api.mockImplementation((url: string) => {
    if (url.includes("/active?")) return Promise.resolve(json({}))
    if (failure === "refused") return Promise.resolve(json({}, 503))
    return Promise.reject(failure === "network" ? new Error("offline") : "unavailable")
  })
  render(<TriggersTab workspaceId="a" users={[]} />)
  expect(await screen.findByText(failure === "refused" ? "Failed (503)" : failure === "network" ? "offline" : "Failed to load triggers")).toBeVisible()
})

it("creates for a selected connected user and refreshes subscriptions", async () => {
  api.mockImplementation((url: string, init?: RequestInit) => init?.method === "POST" ? Promise.resolve(json({})) : defaultReads(url))
  await modal()
  fireEvent.change(screen.getByRole("combobox", { name: "For user" }), { target: { value: "bob" } })
  fireEvent.click(screen.getByRole("button", { name: "Create trigger" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
  const [url, init] = api.mock.calls.find(([, init]) => init?.method === "POST")!
  expect(url).toBe("/api/v1/integrations/composio/triggers?workspace_id=a")
  expect(JSON.parse(init.body)).toEqual({ slug: "NEW_MESSAGE", user_id: "bob" })
  expect(api.mock.calls.filter(([url]) => url.includes("/active?")).length).toBe(2)
})

it("allows an explicitly entered user, trims it and prevents blank submission", async () => {
  api.mockImplementation((url: string, init?: RequestInit) => init?.method === "POST" ? Promise.resolve(json({})) : defaultReads(url))
  await modal([])
  expect(screen.queryByRole("combobox")).not.toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Create trigger" })).toBeDisabled()
  fireEvent.change(screen.getByPlaceholderText("e.g. alice@acme.com"), { target: { value: "   " } })
  expect(screen.getByRole("button", { name: "Create trigger" })).toBeDisabled()
  fireEvent.change(screen.getByRole("textbox", { name: "User id" }), { target: { value: "  external-user  " } })
  fireEvent.click(screen.getByRole("button", { name: "Create trigger" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
  expect(JSON.parse(api.mock.calls.find(([, init]) => init?.method === "POST")![1].body).user_id).toBe("external-user")
})

it.each(["detail", "invalid json", "network", "non-error"])("keeps a %s create failure visible and retryable", async (failure) => {
  api.mockImplementation((url: string, init?: RequestInit) => {
    if (init?.method !== "POST") return defaultReads(url)
    if (failure === "detail") return Promise.resolve(json({ detail: "Account disconnected" }, 409))
    if (failure === "invalid json") return Promise.resolve(new Response("bad response", { status: 502 }))
    return Promise.reject(failure === "network" ? new Error("offline") : "unavailable")
  })
  await modal()
  fireEvent.click(screen.getByRole("button", { name: "Create trigger" }))
  expect(await screen.findByText(failure === "detail" ? "Account disconnected" : failure === "invalid json" ? "Failed (502)" : failure === "network" ? "offline" : "Failed to create trigger")).toBeVisible()
  expect(screen.getByRole("button", { name: "Create trigger" })).toBeEnabled()
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
})

it("ignores an abandoned create when the selected workspace changes", async () => {
  const created = deferred<Response>()
  api.mockImplementation((url: string, init?: RequestInit) => init?.method === "POST" ? created.promise : defaultReads(url))
  const view = render(<TriggersTab workspaceId="a" users={["alice"]} />)
  fireEvent.click(await screen.findByRole("button", { name: "Enable" }))
  fireEvent.click(screen.getByRole("button", { name: "Create trigger" }))
  expect(screen.getByRole("button", { name: "Creating…" })).toBeDisabled()
  view.rerender(<TriggersTab workspaceId="b" users={["bob"]} />)
  await waitFor(() => expect(api).toHaveBeenCalledWith(expect.stringContaining("/active?workspace_id=b"), expect.anything()))
  await act(async () => created.resolve(json({})))
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  expect(api.mock.calls.filter(([url]) => url.includes("/active?workspace_id=a")).length).toBe(1)
})

it("does not refresh subscriptions after closing a pending creation through the dialog control", async () => {
  const created = deferred<Response>()
  api.mockImplementation((url: string, init?: RequestInit) => init?.method === "POST" ? created.promise : defaultReads(url))
  await modal()
  fireEvent.click(screen.getByRole("button", { name: "Create trigger" }))
  fireEvent.click(screen.getByRole("button", { name: "Close" }))
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  await act(async () => created.resolve(json({})))
  expect(api.mock.calls.filter(([url]) => url.includes("/active?")).length).toBe(1)
})
