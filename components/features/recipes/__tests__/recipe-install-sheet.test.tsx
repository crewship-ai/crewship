import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { RecipeInstallSheet } from "../recipe-install-sheet"

const state = vi.hoisted(() => ({ api: vi.fn(), push: vi.fn(), success: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: state.api }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: state.push }) }))
vi.mock("sonner", () => ({ toast: { success: state.success } }))
const props = { workspaceId: "a", recipeSlug: "review", open: true, onOpenChange: vi.fn(), onInstalled: vi.fn() }
function preview(slug = "review") {
  return { recipe: { slug, name: `Recipe ${slug}`, description: "Review changes", icon: "", color: "", crew_slug: "reviewers", credentials: [
    { env_var_name: "API_KEY", provider: "example", type: "API_KEY", label: "Example key", help_url: "https://example.net/keys" },
    { env_var_name: "EXISTING", provider: "example", type: "API_KEY", label: "Existing key" },
  ], mcp_servers: [{ name: "source", display_name: "Source reader", transport: "stdio" }] }, needed_credentials: ["API_KEY"], existing_credentials: { EXISTING: true } as Record<string, boolean>, crew_slug_available: false, resolved_crew_slug: "reviewers-2" }
}
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((done) => { resolve = done }); return { promise, resolve } }
beforeEach(() => { state.api.mockReset(); state.push.mockReset(); state.success.mockReset(); props.onOpenChange.mockReset(); props.onInstalled.mockReset() })
afterEach(cleanup)
async function credentials() {
  await screen.findByText("Review changes")
  fireEvent.click(screen.getByRole("button", { name: "Continue" }))
}
async function confirmation() {
  await credentials()
  fireEvent.change(screen.getByPlaceholderText("Get from https://example.net/keys"), { target: { value: "synthetic-test-key" } })
  fireEvent.click(screen.getByRole("button", { name: "Continue" }))
}

it("does not read recipes without a complete open selection", () => {
  state.api.mockResolvedValue(json(preview()))
  const view = render(<RecipeInstallSheet {...props} recipeSlug={null} />)
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  view.rerender(<RecipeInstallSheet {...props} open={false} />)
  view.rerender(<RecipeInstallSheet {...props} workspaceId="" />)
  expect(state.api).not.toHaveBeenCalled()
})

it("previews reuse and suffixing, requires a nonblank new secret, and installs the reviewed recipe", async () => {
  state.api.mockImplementation((_url: string, init?: RequestInit) => Promise.resolve(json(init?.method === "POST" ? { crew_slug: "reviewers / team" } : preview())))
  render(<RecipeInstallSheet {...props} />)
  await screen.findByText("Review changes")
  expect(screen.getByText("(suffixed; original taken)")).toBeVisible()
  expect(screen.getByText(/Reuse credential/)).toHaveTextContent("EXISTING")
  fireEvent.click(screen.getByRole("button", { name: "Continue" }))
  const value = screen.getByPlaceholderText("Get from https://example.net/keys")
  expect(value).toHaveAttribute("type", "password")
  expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled()
  fireEvent.change(value, { target: { value: "   " } })
  expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled()
  fireEvent.change(value, { target: { value: "synthetic-test-key" } })
  fireEvent.change(screen.getByPlaceholderText("e.g. production"), { target: { value: "Sandbox" } })
  fireEvent.click(screen.getByRole("button", { name: "Show API_KEY" }))
  expect(value).toHaveAttribute("type", "text")
  fireEvent.click(screen.getByRole("button", { name: "Hide API_KEY" }))
  expect(value).toHaveAttribute("type", "password")
  fireEvent.click(screen.getByRole("button", { name: "Continue" }))
  expect(screen.getByText("1 new credential, 1 reused")).toBeVisible()
  expect(screen.getByText("1 MCP server")).toBeVisible()
  expect(screen.queryByDisplayValue("synthetic-test-key")).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Install" }))
  await waitFor(() => expect(state.push).toHaveBeenCalledWith("/crews?crew=reviewers%20%2F%20team"))
  expect(state.success).toHaveBeenCalledWith("Recipe review installed")
  expect(props.onInstalled).toHaveBeenCalledTimes(1)
  expect(props.onOpenChange).toHaveBeenCalledWith(false)
  const [url, init] = state.api.mock.calls.find(([, init]) => init?.method === "POST")!
  expect(url).toBe("/api/v1/recipes/review/install?workspace_id=a")
  expect(JSON.parse(init.body)).toEqual({ credential_values: { API_KEY: "synthetic-test-key" }, account_labels: { API_KEY: "Sandbox" } })
})

it.each(["workspace", "recipe", "close"])("clears credentials, labels and reveal state on %s changes", async (change) => {
  state.api.mockImplementation((url: string) => Promise.resolve(json(preview(url.includes("/other/") ? "other" : "review"))))
  const view = render(<RecipeInstallSheet {...props} />)
  await credentials()
  fireEvent.change(screen.getByPlaceholderText("Get from https://example.net/keys"), { target: { value: "synthetic-old-key" } })
  fireEvent.change(screen.getByPlaceholderText("e.g. production"), { target: { value: "Old account" } })
  fireEvent.click(screen.getByPlaceholderText("Get from https://example.net/keys").parentElement!.querySelector("button")!)
  if (change === "close") {
    view.rerender(<RecipeInstallSheet {...props} open={false} />)
    view.rerender(<RecipeInstallSheet {...props} />)
  } else view.rerender(<RecipeInstallSheet {...props} workspaceId={change === "workspace" ? "b" : "a"} recipeSlug={change === "recipe" ? "other" : "review"} />)
  await credentials()
  expect(screen.getByPlaceholderText("Get from https://example.net/keys")).toHaveValue("")
  expect(screen.getByPlaceholderText("Get from https://example.net/keys")).toHaveAttribute("type", "password")
  expect(screen.getByPlaceholderText("e.g. production")).toHaveValue("")
  expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled()
})

it("ignores an old preview that resolves after the selected recipe changes", async () => {
  const old = deferred<Response>()
  state.api.mockImplementation((url: string) => url.includes("/review/") ? old.promise : Promise.resolve(json(preview("other"))))
  const view = render(<RecipeInstallSheet {...props} />)
  view.rerender(<RecipeInstallSheet {...props} recipeSlug="other" />)
  await screen.findByText("Recipe other")
  await act(async () => old.resolve(json(preview())))
  expect(screen.getByText("Recipe other")).toBeVisible()
  expect(screen.queryByText("Recipe review")).not.toBeInTheDocument()
})

it.each(["workspace", "close"])("ignores a completed install after %s changes", async (change) => {
  const installed = deferred<Response>()
  state.api.mockImplementation((_url: string, init?: RequestInit) => init?.method === "POST" ? installed.promise : Promise.resolve(json(preview())))
  const view = render(<RecipeInstallSheet {...props} />)
  await confirmation()
  fireEvent.click(screen.getByRole("button", { name: "Install" }))
  expect(screen.getByRole("button", { name: "Installing..." })).toBeDisabled()
  expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled()
  view.rerender(<RecipeInstallSheet {...props} workspaceId={change === "workspace" ? "b" : "a"} open={change !== "close"} />)
  await act(async () => installed.resolve(json({ crew_slug: "old" })))
  expect(state.success).not.toHaveBeenCalled()
  expect(state.push).not.toHaveBeenCalled()
  expect(props.onInstalled).not.toHaveBeenCalled()
  expect(props.onOpenChange).not.toHaveBeenCalled()
})

it.each(["reason", "invalid json", "invalid error", "network", "invalid success"])("keeps install retryable after %s failure", async (failure) => {
  state.api.mockImplementation((_url: string, init?: RequestInit) => {
    if (init?.method !== "POST") return Promise.resolve(json(preview()))
    if (failure === "network") return Promise.reject(new Error("offline"))
    if (failure === "reason") return Promise.resolve(json({ error: "Permission revoked" }, 403))
    if (failure === "invalid error") return Promise.resolve(json({ error: 42 }, 502))
    return Promise.resolve(new Response("bad response", { status: failure === "invalid success" ? 200 : 502 }))
  })
  render(<RecipeInstallSheet {...props} />)
  await confirmation()
  fireEvent.click(screen.getByRole("button", { name: "Install" }))
  expect(await screen.findByText(failure === "reason" ? "Permission revoked" : ["network", "invalid success"].includes(failure) ? "Network error" : "Install failed")).toBeVisible()
  expect(screen.getByRole("button", { name: "Install" })).toBeEnabled()
  fireEvent.click(screen.getByRole("button", { name: "← Back" }))
  expect(screen.getByPlaceholderText("Get from https://example.net/keys")).toHaveValue("synthetic-test-key")
  expect(state.push).not.toHaveBeenCalled()
})

it("installs without new credentials when all are reusable and no callback is provided", async () => {
  const data = preview()
  data.existing_credentials.API_KEY = true
  data.needed_credentials = []
  data.crew_slug_available = true
  data.recipe.mcp_servers = []
  state.api.mockImplementation((_url: string, init?: RequestInit) => Promise.resolve(json(init?.method === "POST" ? { crew_slug: "reviewers" } : data)))
  render(<RecipeInstallSheet {...props} onInstalled={undefined} />)
  await credentials()
  expect(screen.getByText(/All credentials this recipe needs are already/)).toBeVisible()
  fireEvent.click(screen.getByRole("button", { name: "Continue" }))
  expect(screen.getByText("0 new credentials, 2 reused")).toBeVisible()
  expect(screen.getByText("0 MCP servers")).toBeVisible()
  fireEvent.click(screen.getByRole("button", { name: "Install" }))
  await waitFor(() => expect(state.push).toHaveBeenCalledWith("/crews?crew=reviewers"))
})

it.each(["refused", "network", "invalid json"])("keeps progression disabled after a %s preview", async (failure) => {
  state.api.mockImplementation(() => failure === "network" ? Promise.reject(new Error("offline")) : Promise.resolve(failure === "refused" ? json({}, 404) : new Response("invalid")))
  render(<RecipeInstallSheet {...props} />)
  expect(await screen.findByText("Recipe not found.")).toBeVisible()
  expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled()
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
  expect(props.onOpenChange).toHaveBeenCalledWith(false)
})

it("requires each new credential and counts multiple resources in confirmation", async () => {
  const data = preview()
  data.existing_credentials = {}
  data.needed_credentials = ["API_KEY", "EXISTING"]
  data.recipe.mcp_servers.push({ name: "second", display_name: "Second reader", transport: "streamable-http" })
  state.api.mockResolvedValue(json(data))
  render(<RecipeInstallSheet {...props} />)
  await screen.findByText(/prompted for 2 credentials/)
  await credentials()
  fireEvent.change(screen.getByPlaceholderText("Get from https://example.net/keys"), { target: { value: "synthetic-one" } })
  expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled()
  fireEvent.change(screen.getByPlaceholderText("Paste value..."), { target: { value: "synthetic-two" } })
  fireEvent.click(screen.getByRole("button", { name: "Continue" }))
  expect(screen.getByText("2 new credentials, 0 reused")).toBeVisible()
  expect(screen.getByText("2 MCP servers")).toBeVisible()
})

it("cancels its preview request on unmount", () => {
  const pending = deferred<Response>()
  state.api.mockReturnValue(pending.promise)
  const view = render(<RecipeInstallSheet {...props} />)
  const signal = state.api.mock.calls[0][1]?.signal as AbortSignal | undefined
  view.unmount()
  expect(signal?.aborted).toBe(true)
})
