import { cleanup, render as renderUI, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import PagesPage from "../page"
import { apiFetch } from "@/lib/api-fetch"
const replace = vi.hoisted(() => vi.fn())
vi.mock("next/navigation", () => ({ useRouter: () => ({ replace }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "human" } } }) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "workspace", loading: false }) }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => "standard" }))
vi.mock("@/components/features/restricted/access-notice", () => ({ useAccessContext: () => ({}) }))
vi.mock("@/components/features/restricted/restricted-pages", () => ({ RestrictedPages: () => <p>Restricted catalog</p> }))
vi.mock("@/components/features/pages/pages-layout", () => ({ PagesLayout: () => <p>Pages overview</p> }))
beforeEach(() => { replace.mockReset(); vi.mocked(apiFetch).mockReset(); window.history.replaceState({}, "", "/pages?ref=saved-id&workspace_id=workspace") })
const clients: QueryClient[] = []
function render(content: React.ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  clients.push(client)
  return renderUI(<QueryClientProvider client={client}>{content}</QueryClientProvider>)
}
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()) })
it("opens the saved identity using its currently authorized address", async () => {
  vi.mocked(apiFetch).mockResolvedValue(Response.json([{ id: "other-id", slug: "reused-address" }, { id: "saved-id", slug: "actual-address" }]))
  render(<PagesPage />)
  await waitFor(() => expect(replace).toHaveBeenCalledWith("/pages/actual-address?workspace_id=workspace"))
  expect(apiFetch).toHaveBeenCalledWith("/api/v1/pages?workspace_id=workspace", expect.objectContaining({ signal: expect.any(AbortSignal) }))
  expect(screen.queryByText("Pages overview")).toBeNull()
})
it("does not substitute another identity or reveal inaccessible metadata", async () => {
  vi.mocked(apiFetch).mockResolvedValue(Response.json([{ id: "other-id", slug: "reused-address", name: "Other document title" }]))
  render(<PagesPage />)
  expect(await screen.findByText("Document unavailable.")).toBeTruthy()
  expect(replace).not.toHaveBeenCalled()
  expect(screen.queryByText("Other document title")).toBeNull()
  expect(screen.queryByText("Pages overview")).toBeNull()
})
it.each(["ref=one&ref=two&workspace_id=workspace", "ref=saved-id&workspace_id=other", "ref=&workspace_id=workspace"])("refuses ambiguous or foreign workspace intent: %s", async query => {
  window.history.replaceState({}, "", "/pages?" + query)
  render(<PagesPage />)
  expect(await screen.findByText("Document unavailable.")).toBeTruthy()
  expect(apiFetch).not.toHaveBeenCalled()
  expect(replace).not.toHaveBeenCalled()
})
it("preserves the ordinary Pages entrypoint", async () => {
  window.history.replaceState({}, "", "/pages")
  render(<PagesPage />)
  expect(await screen.findByText("Pages overview")).toBeTruthy()
  expect(apiFetch).not.toHaveBeenCalled()
})
it.each([403, 404, 503])("does not navigate after a failed read: %s", async status => {
  vi.mocked(apiFetch).mockResolvedValue(Response.json({ error: "Private title must stay hidden" }, { status }))
  render(<PagesPage />)
  expect(await screen.findByText("Document unavailable.")).toBeTruthy()
  expect(replace).not.toHaveBeenCalled()
  expect(screen.queryByText(/Private title/)).toBeNull()
})
