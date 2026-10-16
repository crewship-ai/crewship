import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, expect, it, vi } from "vitest"
import { DocumentCreationChoice } from "../document-creation-choice"
import { apiFetch } from "@/lib/api-fetch"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
afterEach(() => { cleanup(); vi.clearAllMocks() })

it("offers one output choice, then clears it when Docs becomes unavailable", async () => {
  let enabled = true
  vi.mocked(apiFetch).mockImplementation(async () => new Response(JSON.stringify({ enabled })))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const change = vi.fn()
  render(<QueryClientProvider client={client}><DocumentCreationChoice workspaceId="workspace" selected onChange={change} disabled={false} /></QueryClientProvider>)
  const button = await screen.findByRole("button", { name: "Create document" })
  expect(button.getAttribute("aria-pressed")).toBe("true")
  fireEvent.click(button)
  expect(change).toHaveBeenCalledWith(false)
  change.mockClear()
  enabled = false
  await act(async () => { await client.invalidateQueries({ queryKey: ["document-status", "workspace"] }) })
  await waitFor(() => expect(change).toHaveBeenCalledWith(false))
  expect(screen.queryByRole("button", { name: "Create document" })).toBeNull()
  expect(vi.mocked(apiFetch).mock.calls.every(([, options]) => !options?.method || options.method === "GET")).toBe(true)
  client.clear()
})
