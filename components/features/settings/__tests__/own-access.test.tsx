import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { OwnAccessCard } from "../own-access"

const mocks = vi.hoisted(() => ({
  fetch: vi.fn(),
  workspace: { workspaceId: "w", workspaceUnavailable: false, loading: false },
  user: { id: "h1" },
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: mocks.fetch }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => mocks.workspace }))
vi.mock("@/hooks/use-auth", () => ({ useAuth: () => ({ session: { user: mocks.user } }) }))

function view() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const result = render(<QueryClientProvider client={client}><OwnAccessCard /></QueryClientProvider>)
  return { ...result, client }
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  mocks.workspace = { workspaceId: "w", workspaceUnavailable: false, loading: false }
  mocks.user = { id: "h1" }
})

describe("your access", () => {
  it("shows only server-derived decisions and supports error recovery", async () => {
    mocks.fetch.mockResolvedValueOnce({ ok: false }).mockResolvedValueOnce({ ok: true, json: async () => ({ explanation: "Guest access", decisions: [{ kind: "routine", id: "r1", name: "Shared routine", operation: "run", state: "unavailable", reason: "Runtime not installed" }] }) })
    view()
    expect(await screen.findByRole("alert")).toHaveTextContent("Access details unavailable")
    fireEvent.click(screen.getByRole("button", { name: "Retry" }))
    expect(await screen.findByText("Runtime not installed")).toBeVisible()
    expect(screen.getByText(/Shared routine · run/)).toBeVisible()
    expect(mocks.fetch).toHaveBeenCalledWith("/api/v1/workspaces/w/access/me", { signal: expect.any(AbortSignal) })
  })

  it("does not request an unavailable workspace", () => {
    mocks.workspace.workspaceUnavailable = true
    view()
    expect(screen.getByText(/Select an available workspace/)).toBeVisible()
    expect(mocks.fetch).not.toHaveBeenCalled()
  })

  it("does not reuse another account's access in the same workspace", async () => {
    mocks.fetch.mockResolvedValueOnce({ ok: true, json: async () => ({ explanation: "First account policy", decisions: [] }) })
    const { client, rerender } = view()
    expect(await screen.findByText("First account policy")).toBeVisible()
    mocks.user = { id: "h2" }
    mocks.fetch.mockResolvedValueOnce({ ok: true, json: async () => ({ explanation: "Second account policy", decisions: [] }) })
    rerender(<QueryClientProvider client={client}><OwnAccessCard /></QueryClientProvider>)
    expect(screen.queryByText("First account policy")).not.toBeInTheDocument()
    expect(await screen.findByText("Second account policy")).toBeVisible()
    await waitFor(() => expect(mocks.fetch).toHaveBeenCalledTimes(2))
  })
})

it.each([409, 500])("keeps the last successful access visible on refetch failure %s", async (status) => {
  mocks.fetch.mockResolvedValueOnce({ok:true,json:async()=>({explanation:"Saved access",decisions:[{kind:"agent",id:"a",name:"Shared",operation:"chat",state:"conditional",reason:"Private audience"},{kind:"routine",id:"r",name:"Shared",operation:"run",state:"denied",reason:"Missing agent grant"}]})})
  const {client}=view()
  expect(await screen.findByText("Agent · Shared · chat · Subject to execution checks")).toBeVisible()
  expect(screen.getByText("Routine · Shared · run · No access")).toBeVisible()
  mocks.fetch.mockResolvedValueOnce({ok:false,status})
  await client.invalidateQueries({queryKey:["own-access"]})
  expect(await screen.findByRole("status")).toHaveTextContent("last successful check")
  expect(screen.getByText("Saved access")).toBeVisible()
  expect(screen.queryByRole("alert")).not.toBeInTheDocument()
})

it("rejects an invalid access response before rendering its rows", async () => {
  mocks.fetch.mockResolvedValueOnce({ok:true,json:async()=>({explanation:"Invalid",decisions:[{kind:"credential",id:"c",state:"allowed"}]})})
  view()
  expect(await screen.findByRole("alert")).toBeVisible()
  expect(screen.queryByText("Invalid")).not.toBeInTheDocument()
})
