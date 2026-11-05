import { afterEach, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { DesignAccessDialog } from "../design-access-dialog"

vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "owner" } } }) }))
afterEach(() => { cleanup(); vi.unstubAllGlobals() })
it("explains the shared snapshot to an owner granting ordinary Read access", async () => {
 const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } })
 vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => String(input).includes("/grants") ? json({ grants: [] }) : String(input).includes("/access") ? json({ subjects: [] }) : json({ id: "page", slug: "shop", name: "Design", owner: "user/owner", panels: [] })))
 const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
 render(<QueryClientProvider client={client}><DesignAccessDialog workspaceId="workspace" slug="shop" onClose={vi.fn()} /></QueryClientProvider>)
 expect(await screen.findByLabelText("Level")).toHaveTextContent("read")
 expect(screen.getByText(/Read sees the shared version and presence/)).toBeInTheDocument()
 expect(screen.getByText(/Invite people to view or edit this Design/)).toBeInTheDocument()
 expect(screen.queryByText(/Read follows changes live/)).not.toBeInTheDocument()
 expect(screen.queryByText(/view or edit the live canvas/)).not.toBeInTheDocument()
})
