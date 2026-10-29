import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { PagesLayout } from "../pages-layout"
import { pagesKeys } from "@/hooks/use-pages"

vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "pages-test-human" } }, status: "authenticated" }) }))
const requests = vi.hoisted(() => vi.fn())
const htmlHost = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: requests, broadcastSessionExpired: vi.fn() }))
vi.mock("@/hooks/use-workspace", async importOriginal => ({
  ...await importOriginal<typeof import("@/hooks/use-workspace")>(),
  useWorkspace: () => ({ workspaceId: "ws", role: "MANAGER", capabilities: [], loading: false, error: false, workspaces: [{ id: "ws", currentUserRole: "MANAGER" }] }),
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }), usePathname: () => "/pages/budget", useSearchParams: () => new URLSearchParams(), useParams: () => ({}) }))
vi.mock("../page-application", () => ({ PageApplicationView: ({ fallback }: { fallback: React.ReactNode }) => { htmlHost(); return <>{fallback}</> } }))
afterEach(() => { cleanup(); requests.mockReset(); htmlHost.mockReset() })

const page = { id: "workbook-id", slug: "budget", name: "Budget", has_workbook: true, owner: "user/owner", panels: [] as unknown[] }
const snapshot = { sheets: [{ name: "Sheet1", cells: { bands: { "0": { rows: { "1": { "0": { value: { t: "Text", v: "Apples" }, formula: null, style: 0 } } } } }, len: 1 } }], active_sheet: 0 }
async function open() {
  window.history.replaceState(null, "", "/pages/budget")
  requests.mockImplementation(async (input: string) => {
    const url = new URL(input, "http://localhost")
    if (url.pathname === "/api/v1/documents/status") return Response.json({ enabled: true })
    if (url.pathname === "/api/v1/page-folders") return Response.json({ folders: [] })
    if (url.pathname === "/api/v1/pages") return Response.json([page])
    if (url.pathname === "/api/v1/pages/budget") return Response.json(page)
    if (url.pathname === "/api/v1/pages/%40workbook-id/workbook") return Response.json({ schema_version: 1, rev: 4, generation: 1, snapshot })
    return Response.json({})
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  client.setQueryData(pagesKeys.detail("ws", "budget", "pages-test-human"), page)
  await act(async () => { render(<QueryClientProvider client={client}><PagesLayout workspaceId="ws" slug="budget" /></QueryClientProvider>) })
}
const workbookCalls = () => requests.mock.calls.filter(([path]) => String(path).includes("/workbook"))

it("has_workbook selects the read-only workbook view instead of panels", async () => {
  await open()
  expect(await screen.findByRole("table", { name: "Sheet Sheet1" })).toBeInTheDocument()
  expect(screen.getByRole("heading", { name: "Budget" })).toBeInTheDocument()
  expect(screen.getByText("Revision 4")).toBeInTheDocument()
  expect(screen.getByText("Apples")).toBeInTheDocument()
  expect(htmlHost).not.toHaveBeenCalled()
  expect(screen.queryByText(/declares no panels/i)).toBeNull()
  // The panel editor cannot edit a workbook, so neither Edit nor Share (which opens it) is offered.
  expect(screen.queryByRole("button", { name: "Edit" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Share" })).toBeNull()
})

it("a Page without has_workbook is unaffected and never reads the workbook route", async () => {
  page.has_workbook = false
  try {
    await open()
    await waitFor(() => expect(htmlHost).toHaveBeenCalled())
    expect(workbookCalls()).toHaveLength(0)
    expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument()
  } finally { page.has_workbook = true }
})
