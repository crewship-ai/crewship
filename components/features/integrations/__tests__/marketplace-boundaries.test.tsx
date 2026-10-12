import type { ComponentProps } from "react"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { Marketplace } from "../marketplace"

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch }))
vi.mock("@/components/icons/mcp-logos", () => ({ MCPLogo: () => null }))
type Entry = Parameters<ComponentProps<typeof Marketplace>["onAdd"]>[0]
function entry(id: string, overrides: Partial<Entry> = {}): Entry {
  return { id, name: id, display_name: `Server ${id}`, description: `Description ${id}`, icon: "", transport: "stdio", homepage_url: "", source_url: "", package_name: "", package_registry: "", command: "", endpoint: "", auth_type: "none", env_vars_json: "{}", category: "tools", is_verified: false, trust_tier: "community", is_featured: false, synced_at: "", ...overrides }
}
const response = (servers: Entry[], total = servers.length) => new Response(JSON.stringify({ servers, total, limit: 200, offset: 0 }), { status: 200 })
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((done) => { resolve = done }); return { promise, resolve } }
beforeEach(() => { apiFetch.mockReset() })
afterEach(cleanup)

it("keeps an older delayed search from replacing the current server list", async () => {
  const old = deferred<Response>()
  apiFetch.mockImplementation((url: string) => url.includes("featured=true") ? Promise.resolve(response([])) : url.includes("/search?") ? Promise.resolve(response([entry("new")])) : old.promise)
  render(<Marketplace onAdd={vi.fn()} />)
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "new" } })
  await screen.findByText("Server new")
  await act(async () => old.resolve(response([entry("old")])))
  await waitFor(() => expect(screen.getByText("Server new")).toBeVisible())
  expect(screen.queryByText("Server old")).not.toBeInTheDocument()
})

it("does not let an aborted list clear the loading state of its replacement", async () => {
  const old = deferred<Response>()
  const current = deferred<Response>()
  apiFetch.mockImplementation((url: string) => url.includes("featured=true") ? Promise.resolve(response([])) : url.includes("/search?") ? current.promise : old.promise)
  render(<Marketplace onAdd={vi.fn()} />)
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "new" } })
  await waitFor(() => expect(apiFetch).toHaveBeenCalledWith(expect.stringContaining("/search?"), expect.anything()))
  await act(async () => old.resolve(response([])))
  expect(screen.queryByText("No servers match your search.")).not.toBeInTheDocument()
  await act(async () => current.resolve(response([])))
  expect(screen.getByText("No servers match your search.")).toBeVisible()
})

it("lets the uncategorised facet show entries whose category is absent", async () => {
  apiFetch.mockImplementation((url: string) => Promise.resolve(response(url.includes("featured=true") ? [] : [entry("none", { category: "" }), entry("named") ])))
  render(<Marketplace onAdd={vi.fn()} />)
  await screen.findByText("Server none")
  fireEvent.click(screen.getByRole("button", { name: /uncategorised/ }))
  await waitFor(() => expect(screen.getByText("Server none")).toBeVisible())
  expect(screen.queryByText("Server named")).not.toBeInTheDocument()
})

it("resets the advertised server total after a transport failure", async () => {
  apiFetch.mockImplementation((url: string) => url.includes("featured=true") ? Promise.resolve(response([])) : url.includes("/search?") ? Promise.reject(new Error("offline")) : Promise.resolve(response([entry("old")], 42)))
  render(<Marketplace onAdd={vi.fn()} />)
  await screen.findByText("Server old")
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "new" } })
  await screen.findByText("No servers match your search.")
  expect(screen.getByRole("textbox")).toHaveAttribute("placeholder", "Search 0 servers…")
})

it("filters transports locally, toggles categories, and passes the selected entry to installation", async () => {
  const local = entry("local", { trust_tier: "anthropic" })
  const remote = entry("remote", { transport: "streamable-http", category: "data-tools", trust_tier: "crewship" })
  const onAdd = vi.fn()
  apiFetch.mockImplementation((url: string) => Promise.resolve(response(url.includes("featured=true") ? [] : [local, remote, entry("other") ])))
  render(<Marketplace onAdd={onAdd} />)
  await screen.findByText("Server local")
  fireEvent.click(screen.getByRole("button", { name: "HTTP" }))
  await waitFor(() => expect(screen.getByText("Server remote")).toBeVisible())
  expect(screen.queryByText("Server local")).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Install" }))
  expect(onAdd).toHaveBeenCalledWith(remote)
  fireEvent.click(screen.getByRole("button", { name: "stdio" }))
  await waitFor(() => expect(screen.getByText("Server local")).toBeVisible())
  expect(screen.queryByText("Server remote")).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "All transports" }))
  fireEvent.click(screen.getByRole("button", { name: /data tools/ }))
  expect(screen.getByText("1 server")).toBeVisible()
  fireEvent.click(screen.getByRole("button", { name: /data tools/ }))
  expect(screen.getByText("3 servers")).toBeVisible()
  expect(apiFetch).toHaveBeenCalledTimes(2)
})

it("shows featured entries with name fallbacks and hides them while searching", async () => {
  const featured = entry("featured", { display_name: "", is_featured: true })
  const onAdd = vi.fn()
  apiFetch.mockImplementation((url: string) => Promise.resolve(response(url.includes("featured=true") ? [featured, entry("unflagged", { icon: "custom" })] : [entry("plain", { display_name: "", icon: "custom", description: "" })])))
  render(<Marketplace onAdd={onAdd} />)
  fireEvent.click(await screen.findByRole("button", { name: /featured/ }))
  expect(onAdd).toHaveBeenCalledWith(featured)
  await waitFor(() => expect(screen.getByText("plain")).toBeVisible())
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "  docs & files  " } })
  await waitFor(() => expect(apiFetch).toHaveBeenCalledWith("/api/v1/mcp-registry/search?q=docs%20%26%20files&limit=200", expect.anything()))
  expect(screen.queryByText("Featured")).not.toBeInTheDocument()
})

it.each(["anthropic", "crewship", "community"])("asks the server for the %s trust filter", async (tier) => {
  apiFetch.mockImplementation(() => Promise.resolve(response([])))
  render(<Marketplace onAdd={vi.fn()} />)
  await screen.findByText("Registry empty — wait for first sync.")
  fireEvent.click(screen.getByRole("button", { name: new RegExp(`^${tier}`) }))
  await waitFor(() => expect(apiFetch).toHaveBeenCalledWith(`/api/v1/mcp-registry?limit=200&trust_tier=${tier}`, expect.anything()))
})

it("uses a recipe empty state after refused registry reads", async () => {
  apiFetch.mockResolvedValue(new Response("unavailable", { status: 503 }))
  render(<Marketplace onAdd={vi.fn()} recipeEmptyState={<p>Start from a recipe</p>} />)
  expect(await screen.findByText("Start from a recipe")).toBeVisible()
  expect(screen.queryByText("Featured")).not.toBeInTheDocument()
})

it("allows the main registry to load when the independent featured read fails", async () => {
  apiFetch.mockImplementation((url: string) => url.includes("featured=true") ? Promise.reject(new Error("featured unavailable")) : Promise.resolve(response([entry("available")])) )
  const view = render(<Marketplace onAdd={vi.fn()} />)
  await waitFor(() => expect(screen.getByText("Server available")).toBeVisible())
  const signal = apiFetch.mock.calls.find(([url]) => !String(url).includes("featured=true"))![1].signal as AbortSignal
  view.unmount()
  expect(signal.aborted).toBe(true)
})
