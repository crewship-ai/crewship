import React from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ProviderPoolsDialog } from "../provider-pools-dialog"
import { apiFetch } from "@/lib/api-fetch"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const pool = { id: "pool", name: "Team", provider: "OPENAI", mode: "api_key", revision: 4, member_count: 1, allow_cross_owner: false, members: [{ credential_id: "a", priority: 2 }] }
const account = { id: "a", name: "OpenAI account", type: "API_KEY", provider: "OPENAI", status: "ACTIVE", login: { provider: "OPENAI", mode: "api_key", owner_email: "owner@example.test" } }
function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={client}><ProviderPoolsDialog workspaceId="ws" onClose={vi.fn()} /></QueryClientProvider>)
}
beforeEach(() => {
  vi.mocked(apiFetch).mockReset()
  vi.mocked(apiFetch).mockImplementation(async (url, init) => {
    if (init?.method) return new Response(null, { status: 204 })
    return Response.json(String(url).includes("credentials?") ? [account] : String(url).endsWith("/pool") ? pool : { items: [pool], next_cursor: null })
  })
})
describe("provider account groups", () => {
  it("creates a group from compatible accounts with owner consent off by default", async () => {
    mount()
    const create = await screen.findByRole("button", { name: "Create group" })
    await waitFor(() => expect(create).toBeEnabled())
    fireEvent.click(create)
    fireEvent.change(screen.getByLabelText("Group name"), { target: { value: "New group" } })
    fireEvent.change(screen.getByLabelText("Provider and connection"), { target: { value: "OPENAI:api_key" } })
    fireEvent.click(screen.getByRole("checkbox", { name: /OpenAI account/ }))
    fireEvent.click(screen.getByRole("button", { name: "Create group" }))
    await waitFor(() => expect(vi.mocked(apiFetch).mock.calls.some(([, init]) => init?.method === "POST")).toBe(true))
    const [, init] = vi.mocked(apiFetch).mock.calls.find(([, init]) => init?.method === "POST")!
    expect(JSON.parse(init?.body as string)).toEqual({ name: "New group", provider: "OPENAI", mode: "api_key", allow_cross_owner: false, members: [{ credential_id: "a", priority: 0 }] })
  })
  it("loads metadata with explicit workspace and labels definitions as unassigned", async () => {
    mount()
    expect(await screen.findByText("Team")).toBeInTheDocument()
    expect(screen.getByText(/Not assigned/)).toBeInTheDocument()
    for (const [, init] of vi.mocked(apiFetch).mock.calls) expect(new Headers(init?.headers).get("X-Workspace-ID")).toBe("ws")
  })
  it("edits the exact fetched revision without changing identity", async () => {
    mount()
    fireEvent.click(await screen.findByRole("button", { name: "Edit Team" }))
    const name = await screen.findByLabelText("Group name")
    fireEvent.change(name, { target: { value: "Updated team" } })
    expect(screen.getByLabelText("Provider and connection")).toBeDisabled()
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() => expect(vi.mocked(apiFetch).mock.calls.some(([, init]) => init?.method === "PUT")).toBe(true))
    const [, init] = vi.mocked(apiFetch).mock.calls.find(([, init]) => init?.method === "PUT")!
    expect(new Headers(init?.headers).get("If-Match")).toBe('"4"')
    expect(JSON.parse(init?.body as string)).toEqual({ name: "Updated team", allow_cross_owner: false, members: pool.members })
  })
  it("preserves the draft after a stale response and never retries", async () => {
    const original = vi.mocked(apiFetch).getMockImplementation()!
    vi.mocked(apiFetch).mockImplementation((url, init) => init?.method === "PUT" ? Promise.resolve(new Response(null, { status: 412 })) : original(url, init))
    mount()
    fireEvent.click(await screen.findByRole("button", { name: "Edit Team" }))
    fireEvent.change(await screen.findByLabelText("Group name"), { target: { value: "My draft" } })
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("This group changed")
    expect(screen.getByLabelText("Group name")).toHaveValue("My draft")
    expect(vi.mocked(apiFetch).mock.calls.filter(([, init]) => init?.method === "PUT")).toHaveLength(1)
  })
  it("requires confirmation before retirement and keeps provider accounts", async () => {
    mount()
    fireEvent.click(await screen.findByRole("button", { name: "Remove Team" }))
    expect(screen.getByText(/Its provider accounts will remain/)).toBeInTheDocument()
    expect(vi.mocked(apiFetch).mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(false)
    fireEvent.click(screen.getByRole("button", { name: "Remove group" }))
    await waitFor(() => expect(vi.mocked(apiFetch).mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(true))
  })
})
