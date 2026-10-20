import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import fixture from "@/testdata/slides-schema-v1.json"
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
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }), usePathname: () => "/pages/deck", useSearchParams: () => new URLSearchParams(), useParams: () => ({}) }))
vi.mock("../page-application", () => ({ PageApplicationView: ({ fallback }: { fallback: React.ReactNode }) => { htmlHost(); return <>{fallback}</> } }))
afterEach(() => { cleanup(); requests.mockReset(); htmlHost.mockReset(); vi.unstubAllEnvs() })

const page = { id: "slides-id", slug: "deck", name: "Quarterly deck", has_slides: true, owner: "user/owner", panels: [] }
async function open(slidesRoute: (path: string) => Response | undefined = () => undefined) {
  window.history.replaceState(null, "", "/pages/deck")
  requests.mockImplementation(async (input: string) => {
    const url = new URL(input, "http://localhost")
    if (url.pathname === "/api/v1/documents/status") return Response.json({ enabled: true })
    if (url.pathname === "/api/v1/page-folders") return Response.json({ folders: [] })
    if (url.pathname === "/api/v1/pages") return Response.json([page])
    if (url.pathname === "/api/v1/pages/deck") return Response.json(page)
    return slidesRoute(url.pathname) ?? Response.json({})
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  client.setQueryData(pagesKeys.detail("ws", "deck", "pages-test-human"), page)
  await act(async () => { render(<QueryClientProvider client={client}><PagesLayout workspaceId="ws" slug="deck" /></QueryClientProvider>) })
}
const slidesCalls = () => requests.mock.calls.filter(([path]) => String(path).includes("/slides"))

it("flag off: a has_slides Page renders as before and no Slides request is made", async () => {
  await open()
  await waitFor(() => expect(htmlHost).toHaveBeenCalled())
  expect(screen.queryByLabelText("Slides editor")).toBeNull()
  expect(slidesCalls()).toHaveLength(0)
  expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument()
})

it("flag on: mounts the editor and loads the deck through the Slides route", async () => {
  vi.stubEnv("NEXT_PUBLIC_SLIDES_EDITOR", "true")
  await open(path => path === "/api/v1/pages/%40slides-id/slides" ? Response.json({ schema_version: 1, revision: 3, order_version: 1, content_hash: "a".repeat(64), deck: { ...fixture.deck, deck_id: page.id } }) : undefined)
  expect(await screen.findByLabelText("Slides editor")).toBeInTheDocument()
  expect(screen.getByRole("heading", { name: "Quarterly deck" })).toBeInTheDocument()
  expect(htmlHost).not.toHaveBeenCalled()
  // Edit would open the panel editor, which cannot edit a deck.
  expect(screen.queryByRole("button", { name: "Edit" })).toBeNull()
  // The server has no save route: edits are a labelled local draft and there is no Save button.
  expect(screen.getByText(/Revision 3\. Saving is not available yet/)).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull()
})

it("flag on, route missing: says Slides are not available and never an empty editor", async () => {
  vi.stubEnv("NEXT_PUBLIC_SLIDES_EDITOR", "true")
  await open(() => new Response("404 page not found\n", { status: 404, headers: { "content-type": "text/plain" } }))
  expect(await screen.findByText(/Slides editing is not available on this server yet/)).toBeInTheDocument()
  expect(screen.queryByLabelText("Slides editor")).toBeNull()
})

it("flag on, a non-slides Page is unaffected", async () => {
  vi.stubEnv("NEXT_PUBLIC_SLIDES_EDITOR", "true")
  page.has_slides = false
  try { await open(); await waitFor(() => expect(htmlHost).toHaveBeenCalled()); expect(slidesCalls()).toHaveLength(0) } finally { page.has_slides = true }
})
