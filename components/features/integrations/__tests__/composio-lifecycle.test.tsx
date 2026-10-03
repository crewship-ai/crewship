import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { ComposioStatus } from "../composio-integrations"
import type { Inventory, ComposioSettings, ToolkitInfo, ConnectedAccount } from "../composio/types"

const state = vi.hoisted(() => ({ workspaceId: "workspace", loading: false, fetch: vi.fn() }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => state }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => state.fetch(...args) }))
vi.mock("../composio/catalog-tab", () => ({ CatalogTab: ({ toolkits, onConnect, onSearch, search, loading }: { toolkits: ToolkitInfo[]; onConnect: (t: { slug: string; name: string }) => void; onSearch: (s: string) => void; search: string; loading: boolean }) => <section aria-label="Catalog content"><input aria-label="Catalog search" value={search} onChange={e => onSearch(e.target.value)} /><span>{loading ? "Loading catalog" : toolkits.map(t => t.slug).join(",")}</span><button onClick={() => onConnect({ slug: "gmail", name: "Gmail" })}>Connect Gmail</button></section> }))
vi.mock("../composio/connected-accounts-tab", () => ({ ConnectedAccountsTab: ({ onConnectForUser, onChanged }: { onConnectForUser: (u: string) => void; onChanged: () => void }) => <section aria-label="Accounts content"><button onClick={() => onConnectForUser("alice")}>Connect for Alice</button><button onClick={onChanged}>Account changed</button></section> }))
vi.mock("../composio/agent-access-tab", () => ({ AgentAccessTab: ({ onChanged }: { onChanged: () => void }) => <section aria-label="Agent content"><button onClick={onChanged}>Access changed</button></section> }))
vi.mock("../composio/tools-tab", () => ({ ToolsTab: ({ suggestions }: { suggestions: string[] }) => <section aria-label="Tools content">{suggestions.join(",")}</section> }))
vi.mock("../composio/triggers-tab", () => ({ TriggersTab: ({ users }: { users: string[] }) => <section aria-label="Triggers content">{users.join(",")}</section> }))
vi.mock("../composio/mcp-endpoints-tab", () => ({ McpEndpointsTab: () => <section aria-label="MCP content" /> }))

import { ComposioIntegrations } from "../composio-integrations"
import { invalidate, readThrough } from "@/lib/stale-cache"

const reply = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status })
let inventory: Inventory
let settings: ComposioSettings
let request: (url: URL, init?: RequestInit) => Promise<Response> | Response | undefined
const account = (id: string, slug: string, userId = "alice"): ConnectedAccount => ({ id, user_id: userId, toolkit: { slug }, status: "ACTIVE" })
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (reason: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
async function settleCatalog() { await act(async () => { await new Promise(resolve => setTimeout(resolve, 330)) }) }
function latest(callback: ReturnType<typeof vi.fn>) { return callback.mock.calls.at(-1)?.[0] as ComposioStatus }

beforeEach(() => {
 state.workspaceId = "workspace"; state.loading = false; state.fetch.mockReset(); invalidate("composio:")
 inventory = { enabled: true, users: [{ user_id: "alice", connected_accounts: [account("one", "gmail")] }], auth_configs: [] }
 settings = { configured: true, source: "workspace", label: "Project", base_url: "https://provider.invalid" }
 request = () => undefined
 state.fetch.mockImplementation(async (input: string, init?: RequestInit) => {
  const url = new URL(input, "http://localhost")
  const override = request(url, init); if (override !== undefined) return override
  if (url.pathname.endsWith("/inventory")) return reply(inventory)
  if (url.pathname.endsWith("/settings")) return reply(settings)
  if (url.pathname.endsWith("/toolkits")) return reply({ toolkits: [{ slug: "gmail" }], total: 123 })
  if (url.pathname === "/api/v1/agents") return reply([{ id: "agent", name: "Agent" }])
  if (url.pathname.endsWith("/bind")) return reply({ bindings: [{ user_id: "alice", toolkit: "gmail", mode: "read" }] })
  if (url.pathname.endsWith("/connect")) return reply({})
  throw new Error(`Unexpected request ${input}`)
 })
})
afterEach(() => { cleanup(); vi.restoreAllMocks(); invalidate("composio:") })

describe("managed integration lifecycle", () => {
 it("reports inventory, sorted facets, bindings and catalog totals to the host", async () => {
  inventory.users.push({ user_id: "bob", connected_accounts: [account("two", "github", "bob"), account("three", "gmail", "bob")] })
  inventory.auth_configs = [{ id: "auth", name: "Slack", status: "ACTIVE", toolkit: { slug: "slack" } }]
  const status = vi.fn(); render(<ComposioIntegrations onStatus={status} />)
  await screen.findByRole("region", { name: "Catalog content" }); await settleCatalog()
  expect(latest(status)).toMatchObject({ loading: false, configured: true, keyLabel: "Project", counts: { apps: 123, accounts: 3, users: 2, agentsBound: 1, agentsTotal: 1, endpoints: 1 }, toolkits: [{ slug: "gmail", count: 2 }, { slug: "github", count: 1 }], users: [{ id: "bob", count: 2 }, { id: "alice", count: 1 }] })
  expect(screen.getByText("across 2 users")).toBeInTheDocument()
  for (const [tab, region] of [["Connected accounts", "Accounts"], ["Agent access", "Agent"], ["Tools", "Tools"], ["Triggers", "Triggers"], ["MCP endpoints", "MCP"]]) { fireEvent.click(screen.getByRole("button", { name: tab })); expect(screen.getByRole("region", { name: `${region} content` })).toBeInTheDocument() }
 })
 it("refreshes current workspace after account and access mutations", async () => {
  const view = render(<ComposioIntegrations section="accounts" embedded />)
  fireEvent.click(await screen.findByRole("button", { name: "Account changed" }))
  await waitFor(() => expect(state.fetch.mock.calls.filter(([url]) => String(url).includes("/inventory")).length).toBe(2))
  view.rerender(<ComposioIntegrations section="agents" embedded />)
  fireEvent.click(await screen.findByRole("button", { name: "Access changed" }))
  await waitFor(() => expect(state.fetch.mock.calls.filter(([url]) => String(url).includes("/inventory")).length).toBe(3))
  expect(screen.queryByRole("heading", { name: "Integrations" })).not.toBeInTheDocument()
 })
 it.each(["env", "workspace"])("reports a configured %s key without a label", async source => {
  settings = { ...settings, source, label: undefined }
  const status = vi.fn(); render(<ComposioIntegrations onStatus={status} />)
  await waitFor(() => expect(latest(status).keyLabel).toBe(source === "env" ? "env" : "set"))
 })
 it("opens the key editor from the unconfigured state and closes without writing", async () => {
  settings = { configured: false, source: "" }; inventory.enabled = false
  render(<ComposioIntegrations />)
  fireEvent.click(await screen.findByRole("button", { name: /Add API key/i }))
  expect(screen.getByRole("button", { name: "Validate & save" })).toBeDisabled()
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  expect(state.fetch.mock.calls.some(([, init]) => init?.method)).toBe(false)
 })
 it("validates a trimmed API key, refreshes settings and closes the dialog", async () => {
  const change = vi.fn(); render(<ComposioIntegrations onApiKeyOpenChange={change} />)
  await screen.findByRole("region", { name: "Catalog content" })
  fireEvent.click(screen.getByRole("button", { name: "API key" }))
  fireEvent.change(screen.getByPlaceholderText("ak_…"), { target: { value: " sample-input " } })
  fireEvent.change(screen.getByPlaceholderText("Crewship_dev_1"), { target: { value: " Label " } })
  fireEvent.click(screen.getByRole("button", { name: "Validate & save" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
  const call = state.fetch.mock.calls.find(([, init]) => init?.method === "PUT")!
  expect(JSON.parse(call[1].body)).toEqual({ api_key: "sample-input", label: "Label", base_url: "https://provider.invalid" })
  expect(change).toHaveBeenLastCalledWith(false)
 })
 it.each(["detail", "http", "network"])("keeps API key form open after %s failure", async failure => {
  request = (_url, init) => { if (init?.method === "PUT") { if (failure === "network") return Promise.reject(null); return failure === "detail" ? reply({ detail: "Provider refused" }, 400) : new Response("broken", { status: 503 }) } }
  render(<ComposioIntegrations apiKeyOpen />)
  fireEvent.change(screen.getByPlaceholderText("ak_…"), { target: { value: "sample-input" } })
  fireEvent.click(screen.getByRole("button", { name: "Validate & save" }))
  await screen.findByText(failure === "detail" ? "Provider refused" : failure === "http" ? "Failed (503)" : "Failed to save")
  expect(screen.getByRole("button", { name: "Validate & save" })).toBeEnabled()
 })
 it.each(["success", "http", "network"])("handles API key removal: %s", async outcome => {
  request = (_url, init) => { if (init?.method === "DELETE") return outcome === "success" ? reply({}) : outcome === "http" ? reply({}, 503) : Promise.reject(null) }
  render(<ComposioIntegrations />)
  await screen.findByRole("region", { name: "Catalog content" }); fireEvent.click(screen.getByRole("button", { name: "API key" }))
  fireEvent.click(screen.getByRole("button", { name: "Remove key" }))
  if (outcome === "success") await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
  else await screen.findByText(outcome === "http" ? "Failed (503)" : "Failed to remove")
 })
 it.each(["redirect", "blocked popup", "no redirect", "http", "network"])("handles OAuth start: %s", async outcome => {
  const popup = { location: { href: "" }, close: vi.fn() }
  const open = vi.spyOn(window, "open").mockReturnValue(outcome === "blocked popup" ? null : popup as unknown as Window)
  request = (url, init) => { if (url.pathname.endsWith("/connect") && init?.method === "POST") return outcome === "http" ? reply({ detail: "Connection refused" }, 400) : outcome === "network" ? Promise.reject(null) : reply(outcome === "no redirect" ? {} : { redirect_url: "https://oauth.invalid/authorize" }) }
  render(<ComposioIntegrations />)
  fireEvent.click(await screen.findByRole("button", { name: "Connect Gmail" }))
  expect(screen.getByRole("dialog", { name: "Connect Gmail" })).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Connect with OAuth" }))
  expect(open).toHaveBeenCalledWith("", "_blank", "noopener,noreferrer")
  if (outcome === "http" || outcome === "network") { await screen.findByText(outcome === "http" ? "Connection refused" : "Failed to start connection"); expect(popup.close).toHaveBeenCalled() }
  else { await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument()); if (outcome === "redirect") expect(popup.location.href).toBe("https://oauth.invalid/authorize"); if (outcome === "blocked popup") expect(open).toHaveBeenLastCalledWith("https://oauth.invalid/authorize", "_blank", "noopener,noreferrer"); if (outcome === "no redirect") expect(popup.close).toHaveBeenCalled() }
 })
 it("connects a manually chosen app for a new user and supports cancel", async () => {
  vi.spyOn(window, "open").mockReturnValue(null)
  render(<ComposioIntegrations section="accounts" />)
  fireEvent.click(await screen.findByRole("button", { name: "Connect for Alice" }))
  expect(screen.getByRole("button", { name: "Connect with OAuth" })).toBeDisabled()
  fireEvent.change(screen.getByPlaceholderText("e.g. gmail, github, slack"), { target: { value: " github " } })
  fireEvent.change(screen.getByRole("combobox"), { target: { value: "" } })
  expect(screen.getByRole("button", { name: "Connect with OAuth" })).toBeDisabled()
  fireEvent.change(screen.getByPlaceholderText("e.g. alice@acme.com or a stable user id"), { target: { value: " new-user " } })
  fireEvent.click(screen.getByRole("button", { name: "Connect with OAuth" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
  expect(JSON.parse(state.fetch.mock.calls.find(([, init]) => init?.method === "POST")![1].body)).toEqual({ toolkit: "github", user_id: "new-user" })
  fireEvent.click(await screen.findByRole("button", { name: "Connect for Alice" })); fireEvent.click(screen.getByRole("button", { name: "Cancel" })); expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
 })
 it("serves cached inventory and preserves it when background refresh fails", async () => {
  await readThrough("composio:workspace:inventory", async () => inventory).fresh
  const realNow = Date.now(); vi.spyOn(Date, "now").mockReturnValue(realNow + 31_000)
  request = url => url.pathname.endsWith("/inventory") ? reply({}, 503) : undefined
  render(<ComposioIntegrations />)
  await screen.findByRole("region", { name: "Catalog content" })
  expect(screen.queryByText(/Couldn.t load/)).not.toBeInTheDocument()
 })
 it("ignores late inventory and settings from a previous workspace", async () => {
  const old = deferred<Response>()
  request = url => url.searchParams.get("workspace_id") === "workspace" && url.pathname.endsWith("/inventory") ? old.promise : undefined
  const status = vi.fn(); const view = render(<ComposioIntegrations onStatus={status} />)
  state.workspaceId = "next"; view.rerender(<ComposioIntegrations onStatus={status} />)
  await screen.findByRole("region", { name: "Catalog content" })
  await act(async () => old.resolve(reply({ ...inventory, users: [] })))
  expect(latest(status).counts.accounts).toBe(1)
 })
 it("keeps the newest catalog search when an older query finishes late", async () => {
  const old = deferred<Response>()
  request = url => url.pathname.endsWith("/toolkits") ? url.searchParams.get("search") === "old" ? old.promise : reply({ toolkits: [{ slug: "new-result" }], total: 1 }) : undefined
  const view = render(<ComposioIntegrations search="old" />); await settleCatalog()
  view.rerender(<ComposioIntegrations search="new" />); await settleCatalog()
  expect(screen.getByText("new-result")).toBeInTheDocument()
  await act(async () => old.resolve(reply({ toolkits: [{ slug: "obsolete-result" }], total: 999 })))
  expect(screen.queryByText("obsolete-result")).not.toBeInTheDocument()
  expect(screen.getByText("new-result")).toBeInTheDocument()
 })
 it("clears reported accounts and loading when workspace selection is removed", async () => {
  const status = vi.fn(); const view = render(<ComposioIntegrations onStatus={status} />)
  await waitFor(() => expect(latest(status).counts.accounts).toBe(1))
  state.workspaceId = ""; view.rerender(<ComposioIntegrations onStatus={status} />)
  await waitFor(() => expect(latest(status)).toMatchObject({ loading: false, configured: false, counts: { accounts: 0, agentsTotal: 0 } }))
 })
 it.each([false, true])("serves all cached catalogs and tolerates background failure: %s", async failRefresh => {
  await Promise.all([
   readThrough("composio:workspace:inventory", async () => inventory).fresh,
   readThrough("composio:workspace:settings", async () => settings).fresh,
   readThrough("composio:workspace:agents", async () => ({ agents: [{ id: "cached-agent" }], bindings: {} })).fresh,
   readThrough("composio:workspace:toolkits:", async () => ({ toolkits: [{ slug: "cached-tool" }], total: 4 })).fresh,
  ])
  if (failRefresh) { vi.spyOn(Date, "now").mockReturnValue(Date.now() + 31_000); request = () => reply({}, 503) }
  const status = vi.fn(); render(<ComposioIntegrations onStatus={status} />); await settleCatalog()
  expect(screen.getByText("cached-tool")).toBeInTheDocument()
  expect(latest(status).counts.agentsTotal).toBe(1)
  expect(latest(status).counts.agentsBound).toBe(0)
 })
 it.each(["http", "network", "empty"])("isolates an agent binding lookup failure: %s", async failure => {
  request = url => url.pathname.endsWith("/bind") ? failure === "http" ? reply({}, 503) : failure === "network" ? Promise.reject(new Error("offline")) : reply({}) : undefined
  const status = vi.fn(); render(<ComposioIntegrations onStatus={status} />)
  await waitFor(() => expect(latest(status).counts.agentsTotal).toBe(1))
  expect(latest(status).counts.agentsBound).toBe(0)
 })
 it.each(["http", "empty"])("settles an unavailable or empty toolkit catalog: %s", async failure => {
  request = url => url.pathname.endsWith("/toolkits") ? reply({}, failure === "http" ? 503 : 200) : undefined
  const status = vi.fn(); render(<ComposioIntegrations onStatus={status} />); await settleCatalog()
  expect(screen.queryByText("Loading catalog")).not.toBeInTheDocument()
  expect(latest(status).counts.apps).toBe(0)
 })
 it("uses stable alphabetical facets when account counts tie and refreshes explicitly", async () => {
  inventory.users = [{ user_id: "bob", connected_accounts: [account("b", "slack", "bob")] }, { user_id: "alice", connected_accounts: [account("a", "gmail")] }]
  const status = vi.fn(); render(<ComposioIntegrations onStatus={status} />)
  await screen.findByRole("region", { name: "Catalog content" })
  expect(latest(status).users.map(u => u.id)).toEqual(["alice", "bob"])
  expect(latest(status).toolkits.map(t => t.slug)).toEqual(["gmail", "slack"])
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }))
  await waitFor(() => expect(state.fetch.mock.calls.filter(([url]) => String(url).includes("/inventory")).length).toBe(2))
 })
 it("settles a non-Error inventory failure with a readable message", async () => {
  request = url => url.pathname.endsWith("/inventory") ? Promise.reject(null) : undefined
  render(<ComposioIntegrations />)
  await screen.findByText(/Failed to load inventory/)
 })
 it("closes API key and OAuth dialogs through their dismissal controls", async () => {
  render(<ComposioIntegrations />)
  fireEvent.click(await screen.findByRole("button", { name: "Connect Gmail" }))
  fireEvent.click(screen.getByRole("button", { name: "Close" }))
  fireEvent.click(screen.getByRole("button", { name: "API key" }))
  fireEvent.click(screen.getByRole("button", { name: "Close" }))
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
 })
 it("requires a user for the first connection and reports a malformed upstream failure", async () => {
  inventory.users = []
  vi.spyOn(window, "open").mockReturnValue(null)
  request = (url, init) => url.pathname.endsWith("/connect") && init?.method === "POST" ? new Response("broken", { status: 503 }) : undefined
  render(<ComposioIntegrations />)
  fireEvent.click(await screen.findByRole("button", { name: "Connect Gmail" }))
  expect(screen.getByRole("button", { name: "Connect with OAuth" })).toBeDisabled()
  fireEvent.change(screen.getByPlaceholderText("e.g. alice@acme.com or a stable user id"), { target: { value: "first-user" } })
  fireEvent.click(screen.getByRole("button", { name: "Connect with OAuth" }))
  await screen.findByText("Failed (503)")
 })
 it("encodes workspace and agent identifiers in every scoped request", async () => {
  state.workspaceId = "team &other=foreign"
  request = url => url.pathname === "/api/v1/agents" ? reply([{ id: "agent/with?slash" }]) : undefined
  render(<ComposioIntegrations />); await settleCatalog()
  for (const [input] of state.fetch.mock.calls) expect(new URL(input, "http://localhost").searchParams.get("workspace_id")).toBe(state.workspaceId)
  expect(state.fetch.mock.calls.some(([input]) => String(input).includes("/agent%2Fwith%3Fslash/bind?"))).toBe(true)
 })
 it("ignores an old key save after switching to another workspace", async () => {
  const save = deferred<Response>()
  request = (_url, init) => init?.method === "PUT" ? save.promise : undefined
  const status = vi.fn(); const view = render(<ComposioIntegrations onStatus={status} />)
  await screen.findByRole("region", { name: "Catalog content" })
  fireEvent.click(screen.getByRole("button", { name: "API key" }))
  fireEvent.change(screen.getByPlaceholderText("ak_…"), { target: { value: "sample-input" } })
  fireEvent.click(screen.getByRole("button", { name: "Validate & save" }))
  state.workspaceId = "next"; view.rerender(<ComposioIntegrations onStatus={status} />)
  await waitFor(() => expect(latest(status).loading).toBe(false))
  await act(async () => save.resolve(reply({})))
  expect(latest(status).loading).toBe(false)
  expect(screen.getByRole("region", { name: "Catalog content" })).toBeInTheDocument()
 })

})
