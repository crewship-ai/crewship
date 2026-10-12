import { act, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { AccessEditor, AgentConnectorsCard } from "../access-editor"
import type { AgentBinding, Inventory } from "../types"

const api = vi.hoisted(() => vi.fn())
const workspace = vi.hoisted(() => ({ workspaceId: "ws1" as string | null }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => workspace }))
const response = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body })
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((done) => { resolve = done }); return { promise, resolve } }
const inventory: Inventory = { enabled: true, auth_configs: [], users: [
  { user_id: "alice", connected_accounts: [
    { id: "mail1", user_id: "alice", status: "ACTIVE", toolkit: { slug: "gmail", logo: "https://example.org/mail.svg" } },
    { id: "mail2", user_id: "alice", status: "ACTIVE", toolkit: { slug: "gmail" } },
    { id: "git", user_id: "alice", status: "ACTIVE", toolkit: { slug: "github" } },
  ] }, { user_id: "bob", connected_accounts: [] },
] }
const binding: AgentBinding = { toolkit: "gmail", mode: "full", user_id: "alice", endpoint: "https://example.org/mcp" }
const tools = { tools: [{ slug: "GMAIL_LIST_MESSAGES" }, { slug: "GMAIL_SEND_EMAIL" }], total: 25 }
function routes(bindings: AgentBinding[] = [binding], inv: Inventory = inventory) {
  api.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === "POST") return response({})
    if (url.includes("/inventory?")) return response(inv)
    if (url.includes("/bind?")) return response({ bindings })
    if (url.includes("/tools?")) return response(tools)
    throw new Error(`Unexpected request ${url}`)
  })
}
function editor(props: Partial<React.ComponentProps<typeof AccessEditor>> = {}) {
  const onClose = vi.fn(), onSaved = vi.fn()
  return { onClose, onSaved, ...render(<AccessEditor workspaceId="ws1" agentId="agent1" agentName="Researcher" onClose={onClose} onSaved={onSaved} {...props} />) }
}
const saveButton = () => screen.getByRole("button", { name: "Save access" })
const posted = () => JSON.parse(api.mock.calls.find(([, init]) => init?.method === "POST")![1].body)
const scope = (index = 1) => screen.getAllByRole("combobox")[index]
beforeEach(() => { api.mockReset(); workspace.workspaceId = "ws1" })

describe("AccessEditor grant editing", () => {
  it("loads the existing grant, disables save while loading and forwards a complete scoped save", async () => {
    routes()
    const saved = deferred<ReturnType<typeof response>>()
    const original = api.getMockImplementation()!
    api.mockImplementation((url, init) => init?.method === "POST" ? saved.promise : original(url, init))
    const { onSaved } = editor({ agentCrew: "Research crew" })
    expect(saveButton()).toBeDisabled()
    await screen.findByText("Gmail")
    expect(screen.getByText(/Research crew ·/)).toBeInTheDocument()
    fireEvent.change(scope(), { target: { value: "read" } })
    fireEvent.click(saveButton())
    expect(screen.getByRole("button", { name: "Saving…" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled()
    expect(onSaved).not.toHaveBeenCalled()
    await act(async () => { saved.resolve(response({})) })
    expect(onSaved).toHaveBeenCalledTimes(1)
    expect(posted()).toEqual({ user_id: "alice", apps: [{ toolkit: "gmail", mode: "read" }] })
  })
  it("omits off and removed apps from the replacement grant", async () => {
    routes([binding, { ...binding, toolkit: "github" }])
    editor()
    await screen.findByText("Gmail")
    fireEvent.change(scope(), { target: { value: "off" } })
    fireEvent.click(screen.getByRole("button", { name: "Remove github" }))
    fireEvent.click(saveButton())
    await waitFor(() => expect(posted()).toEqual({ user_id: "alice", apps: [] }))
  })
  it("adds a connected app once and changes the available pool with the acts-as user", async () => {
    routes([])
    editor()
    await screen.findByText(/No apps granted yet/)
    fireEvent.click(screen.getByRole("button", { name: "Add app" }))
    expect(await screen.findAllByRole("option", { name: "Gmail" })).toHaveLength(1)
    fireEvent.click(screen.getByRole("option", { name: "Gmail" }))
    expect(screen.getByRole("button", { name: "Remove gmail" })).toBeInTheDocument()
    fireEvent.change(scope(0), { target: { value: "bob" } })
    expect(screen.getByRole("button", { name: "Add app" })).toBeDisabled()
    expect(screen.getByText("this user has no connected apps")).toBeInTheDocument()
    fireEvent.change(scope(0), { target: { value: "alice" } })
    fireEvent.click(screen.getByRole("button", { name: "Add app" }))
    fireEvent.click(await screen.findByRole("option", { name: "Github" }))
    expect(screen.getByText("all connected apps already granted")).toBeInTheDocument()
    fireEvent.click(saveButton())
    await waitFor(() => expect(posted().apps).toEqual([{ toolkit: "gmail", mode: "full" }, { toolkit: "github", mode: "full" }]))
  })
  it("refuses a save without a connected user", async () => {
    routes([], { ...inventory, users: [] })
    editor()
    await screen.findByText(/No connected users yet/)
    fireEvent.click(saveButton())
    expect(screen.getByText("Pick the user this agent acts as.")).toBeInTheDocument()
    expect(api.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false)
  })
  it.each(["Cancel", "Close"])("closes without saving through %s", async (name) => {
    routes()
    const { onClose } = editor()
    await screen.findByText("Gmail")
    fireEvent.click(screen.getByRole("button", { name }))
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(api.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false)
  })
  it.each(["inventory", "bindings"])("blocks save when %s cannot be read", async (failed) => {
    routes()
    const original = api.getMockImplementation()!
    api.mockImplementation((url, init) => (failed === "inventory" ? url.includes("/inventory?") : url.includes("/bind?")) ? Promise.resolve(response({}, 503)) : original(url, init))
    editor()
    expect(await screen.findByText(failed === "inventory" ? "Inventory failed (503)" : "Access failed (503)")).toBeInTheDocument()
    expect(saveButton()).toBeDisabled()
    expect(screen.queryByText(/No apps granted yet/)).not.toBeInTheDocument()
  })
  it.each(["detailed", "unreadable", "network", "non-error"])("shows save failure and preserves editing state: %s", async (failure) => {
    routes()
    const original = api.getMockImplementation()!
    api.mockImplementation((url, init) => {
      if (init?.method !== "POST") return original(url, init)
      if (failure === "network") return Promise.reject(new Error("Connection lost"))
      if (failure === "non-error") return Promise.reject("offline")
      if (failure === "unreadable") return Promise.resolve({ ok: false, status: 500, json: async () => { throw new Error("invalid JSON") } })
      return Promise.resolve(response({ detail: "Scope denied" }, 403))
    })
    const { onSaved } = editor()
    await screen.findByText("Gmail")
    fireEvent.click(saveButton())
    expect(await screen.findByText(({ detailed: "Scope denied", unreadable: "Failed (500)", network: "Connection lost", "non-error": "Failed to save access" })[failure]!)).toBeInTheDocument()
    expect(onSaved).not.toHaveBeenCalled()
    expect(saveButton()).toBeEnabled()
    expect(screen.getByText("Gmail")).toBeInTheDocument()
  })
  it("ignores loading results from a previous agent", async () => {
    const old = deferred<ReturnType<typeof response>>()
    routes()
    const original = api.getMockImplementation()!
    api.mockImplementation((url, init) => url.includes("/agents/agent1/") ? old.promise : original(url, init))
    const { rerender } = editor()
    rerender(<AccessEditor workspaceId="ws1" agentId="agent2" agentName="Current" onClose={vi.fn()} onSaved={vi.fn()} />)
    await screen.findByText("Gmail")
    await act(async () => { old.resolve(response({ bindings: [{ ...binding, toolkit: "obsolete" }] })) })
    expect(screen.queryByText("Obsolete")).not.toBeInTheDocument()
    expect(screen.getByText("Gmail")).toBeInTheDocument()
  })
})

describe("custom tool scopes", () => {
  it("quick-selects only loaded rows, preserves hidden selections, toggles and saves exact tools", async () => {
    routes([{ ...binding, mode: "custom", tools: ["HIDDEN_TOOL", "GMAIL_SEND_EMAIL"] }])
    editor()
    const read = await screen.findByRole("checkbox", { name: /GMAIL_LIST_MESSAGES/ })
    const write = screen.getByRole("checkbox", { name: /GMAIL_SEND_EMAIL/ })
    expect(write).toBeChecked()
    expect(read).not.toBeChecked()
    expect(screen.getByText("Showing 2 of 25 — search to narrow.")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "All" }))
    expect(read).toBeChecked()
    fireEvent.click(screen.getByRole("button", { name: "Read-only" }))
    expect(write).not.toBeChecked()
    expect(read).toBeChecked()
    fireEvent.click(screen.getByRole("button", { name: "None" }))
    expect(read).not.toBeChecked()
    expect(screen.getByText("1 selected")).toBeInTheDocument()
    fireEvent.click(write)
    fireEvent.click(write)
    fireEvent.click(read)
    fireEvent.click(saveButton())
    await waitFor(() => expect(posted()).toEqual({ user_id: "alice", apps: [{ toolkit: "gmail", mode: "custom", tools: ["HIDDEN_TOOL", "GMAIL_LIST_MESSAGES"] }] }))
  })
  it("keeps current search results when an aborted request resolves late", async () => {
    const old = deferred<ReturnType<typeof response>>()
    routes([{ ...binding, mode: "custom" }])
    const original = api.getMockImplementation()!
    api.mockImplementation((url, init) => url.includes("/tools?") ? url.includes("search=") ? Promise.resolve(response({ tools: [{ slug: "CURRENT_TOOL" }], total: 1 })) : old.promise : original(url, init))
    editor()
    await waitFor(() => expect(api.mock.calls.some(([url]) => url.includes("/tools?"))).toBe(true))
    fireEvent.change(screen.getByPlaceholderText(/Search .*tools/), { target: { value: " current " } })
    expect(await screen.findByText("CURRENT_TOOL")).toBeInTheDocument()
    await act(async () => { old.resolve(response({ tools: [{ slug: "STALE_TOOL" }], total: 1 })) })
    expect(screen.queryByText("STALE_TOOL")).not.toBeInTheDocument()
    expect(screen.getByText("CURRENT_TOOL")).toBeInTheDocument()
  })
})

describe("AgentConnectorsCard", () => {
  it("clears loading without a workspace and disables editing", () => {
    workspace.workspaceId = null
    render(<AgentConnectorsCard agentId="a" agentName="Researcher" />)
    expect(screen.getByRole("button", { name: "Manage access" })).toBeDisabled()
    expect(screen.getByText("— no connector access —")).toBeInTheDocument()
    expect(api).not.toHaveBeenCalled()
  })
  it("uses an explicit workspace and reloads the summary after saving", async () => {
    routes()
    render(<AgentConnectorsCard agentId="a" agentName="Researcher" agentCrew="Crew" workspaceId="explicit" className="card-fixture" />)
    expect(await screen.findByText("alice")).toBeInTheDocument()
    expect(api.mock.calls[0][0]).toContain("workspace_id=explicit")
    fireEvent.click(screen.getByRole("button", { name: "Manage access" }))
    expect(await screen.findByRole("dialog")).toBeInTheDocument()
    await waitFor(() => expect(saveButton()).toBeEnabled())
    fireEvent.click(saveButton())
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
    expect(api.mock.calls.filter(([url, init]) => url.includes("/bind?") && !init?.method)).toHaveLength(3)
  })
  it("ignores late bindings after the workspace changes", async () => {
    const old = deferred<ReturnType<typeof response>>()
    api.mockReturnValueOnce(old.promise).mockResolvedValue(response({ bindings: [{ ...binding, user_id: "new-user" }] }))
    const { rerender } = render(<AgentConnectorsCard agentId="a" agentName="Researcher" />)
    workspace.workspaceId = "ws2"
    rerender(<AgentConnectorsCard agentId="a" agentName="Researcher" />)
    expect(await screen.findByText("new-user")).toBeInTheDocument()
    await act(async () => { old.resolve(response({ bindings: [{ ...binding, user_id: "old-user" }] })) })
    expect(screen.queryByText("old-user")).not.toBeInTheDocument()
    expect(screen.getByText("new-user")).toBeInTheDocument()
  })
})

describe("access scope failure boundaries", () => {
  it.each(["status", "network", "null", "abort"])("handles a tool lookup failure without losing the saved grant: %s", async (kind) => {
    routes([{ ...binding, mode: "custom", tools: ["EXISTING_TOOL"] }])
    const original = api.getMockImplementation()!
    api.mockImplementation((url, init) => {
      if (!url.includes("/tools?")) return original(url, init)
      if (kind === "status") return Promise.resolve(response({}, 503))
      if (kind === "abort") return Promise.reject(new DOMException("Cancelled", "AbortError"))
      return Promise.reject(kind === "null" ? null : new Error("Tools unavailable"))
    })
    editor()
    if (kind === "abort") expect(await screen.findByText("No tools found.")).toBeInTheDocument()
    else expect(await screen.findByText(kind === "status" ? "Failed (503)" : kind === "null" ? "Failed to load tools" : "Tools unavailable")).toBeInTheDocument()
    fireEvent.click(saveButton())
    await waitFor(() => expect(posted().apps[0].tools).toEqual(["EXISTING_TOOL"]))
  })
  it("accepts absent optional bindings and tools collections as empty", async () => {
    routes([])
    const original = api.getMockImplementation()!
    api.mockImplementation((url, init) => url.includes("/bind?") || url.includes("/tools?") ? Promise.resolve(response({})) : original(url, init))
    editor()
    await screen.findByText(/No apps granted yet/)
    fireEvent.click(screen.getByRole("button", { name: "Add app" }))
    fireEvent.click(await screen.findByRole("option", { name: "Gmail" }))
    fireEvent.change(scope(), { target: { value: "custom" } })
    expect(await screen.findByText("No tools found.")).toBeInTheDocument()
    expect(screen.getByText("0 selected")).toBeInTheDocument()
  })
  it("shows a generic initial-load failure for non-Error rejection", async () => {
    api.mockRejectedValue("offline")
    editor()
    expect(await screen.findByText("Failed to load access")).toBeInTheDocument()
    expect(saveButton()).toBeDisabled()
  })
  it.each(["http", "network", "empty", "no-user"])("summarizes absent or unavailable bindings without crashing: %s", async (kind) => {
    if (kind === "network") api.mockRejectedValue(new Error("offline"))
    else api.mockResolvedValue(response(kind === "no-user" ? { bindings: [{ ...binding, user_id: "", mode: "custom", tools: ["READ"] }] } : {}, kind === "http" ? 503 : 200))
    render(<AgentConnectorsCard agentId="a" agentName="Researcher" />)
    if (kind === "no-user") {
      expect(await screen.findByText(/Gmail/)).toBeInTheDocument()
      expect(screen.queryByText(/acts as/)).not.toBeInTheDocument()
    } else expect(await screen.findByText("— no connector access —")).toBeInTheDocument()
  })
  it("can cancel management from the summary without reloading or saving", async () => {
    routes()
    render(<AgentConnectorsCard agentId="a" agentName="Researcher" />)
    await screen.findByText("alice")
    fireEvent.click(screen.getByRole("button", { name: "Manage access" }))
    await waitFor(() => expect(saveButton()).toBeEnabled())
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    expect(api.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false)
  })
  it("ignores completion of a save belonging to the previous agent", async () => {
    routes()
    const pending = deferred<ReturnType<typeof response>>()
    const original = api.getMockImplementation()!
    api.mockImplementation((url, init) => init?.method === "POST" ? pending.promise : original(url, init))
    const onSaved = vi.fn()
    const { rerender } = editor({ onSaved })
    await screen.findByText("Gmail")
    fireEvent.click(saveButton())
    rerender(<AccessEditor workspaceId="ws2" agentId="agent2" agentName="New agent" onClose={vi.fn()} onSaved={onSaved} />)
    await screen.findByText("Gmail")
    await act(async () => { pending.resolve(response({})) })
    expect(onSaved).not.toHaveBeenCalled()
    expect(saveButton()).toBeEnabled()
  })
  it("encodes workspace and agent identifiers for both reads and writes", async () => {
    routes()
    editor({ workspaceId: "ws&admin=yes", agentId: "agent/a?b" })
    await screen.findByText("Gmail")
    fireEvent.click(saveButton())
    await waitFor(() => expect(api.mock.calls.some(([, init]) => init?.method === "POST")).toBe(true))
    const urls = api.mock.calls.map(([url]) => url)
    expect(urls).toContain("/api/v1/integrations/composio/inventory?workspace_id=ws%26admin%3Dyes")
    expect(urls.filter((url) => url === "/api/v1/integrations/composio/agents/agent%2Fa%3Fb/bind?workspace_id=ws%26admin%3Dyes")).toHaveLength(2)
  })
})
