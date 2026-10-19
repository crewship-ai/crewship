import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { PagesLayout } from "../pages-layout"

const fixture = vi.hoisted(() => ({ mobile: false, requests: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "space-human" } }, status: "authenticated" }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fixture.requests, broadcastSessionExpired: vi.fn() }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => fixture.mobile }))
vi.mock("@/hooks/use-workspace", async (original) => ({
 ...await original<typeof import("@/hooks/use-workspace")>(),
 useWorkspace: () => ({ workspaceId: "ws", role: "MEMBER", capabilities: [], loading: false, error: false, workspaces: [{ id: "ws", currentUserRole: "MEMBER" }] }),
}))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }), usePathname: () => "/pages", useSearchParams: () => new URLSearchParams(), useParams: () => ({}) }))
vi.mock("../personal-document-space", () => ({ PersonalDocumentSpace: ({ workspaceId }: { workspaceId: string }) => <section aria-label="Personal Documents fixture">{workspaceId}</section> }))

afterEach(() => { cleanup(); fixture.requests.mockReset() })

it.each([false, true])("opens the personal Documents view from the actual shell (mobile=%s)", async (mobile) => {
 fixture.mobile = mobile
 window.history.replaceState(null, "", "/pages")
 fixture.requests.mockImplementation(async (input: string) => {
  const url = new URL(input, "http://localhost")
  if (url.pathname === "/api/v1/documents/status") return Response.json({ enabled: true })
  if (url.pathname === "/api/v1/page-folders") return Response.json({ folders: [] })
  if (url.pathname === "/api/v1/pages") {
   const own = { id: "own-private", slug: "own-private", name: "Own private", owner: "user/space-human", has_document: true, private_audience: true, panels: [] }
   const legacy = { id: "own-private-html", slug: "own-private-html", name: "Own private HTML", owner: "user/space-human", has_document: false, private_audience: true, panels: [] }
   return Response.json(url.searchParams.get("audience") === "personal" ? [own, legacy] : [own, legacy, { id: "shared", slug: "shared", name: "Shared", panels: [] }])
  }
  return Response.json({})
 })
 const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
 try {
  await act(async () => { render(<QueryClientProvider client={client}><PagesLayout workspaceId="ws" /></QueryClientProvider>) })
  expect(screen.queryByRole("region", { name: "Personal Documents fixture" })).toBeNull()
  await screen.findByText("3 pages")
  const toggle = await screen.findByRole("button", { name: "My space" })
  expect(toggle).toHaveAttribute("aria-pressed", "false")
  fireEvent.click(toggle)
  expect(await screen.findByRole("region", { name: "Personal Documents fixture" })).toHaveTextContent("ws")
  expect(toggle).toHaveAttribute("aria-pressed", "true")
  await screen.findByText("1 page")
  fireEvent.click(toggle)
  expect(screen.queryByRole("region", { name: "Personal Documents fixture" })).toBeNull()
  expect(toggle).toHaveAttribute("aria-pressed", "false")
 } finally { cleanup(); client.clear() }
})
