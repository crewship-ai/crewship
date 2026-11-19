import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, within } from "@testing-library/react"

// Role gates, per tier. The server: Manager and up write crew links (POST,
// DELETE, PUT file-access are roleCreate); every member reads; the audit list
// behind "Changes to these links" is Admin and up.

const api = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws" }), useCurrentWorkspaceId: () => "ws" }))
let role = "OWNER"
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn(), message: vi.fn() } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }), usePathname: () => "/settings/crew-links" }))

import { CrewLinksPage } from "../crew-links-page"

const CREWS = [
  { id: "eng", name: "Engineering", slug: "engineering" },
  { id: "ops", name: "Ops", slug: "ops" },
]
const CONNS = [{ id: "c1", from_crew_id: "eng", to_crew_id: "ops", direction: "bidirectional", status: "active", forward_file_access: "read", reverse_file_access: "read_write", access_version: 3 }]
const ok = (b: unknown) => ({ ok: true, status: 200, json: async () => b })
const auditCalls = () => api.mock.calls.filter(([u]) => String(u).includes("/api/v1/audit"))

beforeEach(() => {
  api.mockReset()
  api.mockImplementation(async (url: string) =>
    url.includes("/api/v1/audit") ? ok({ data: [], pagination: { total_pages: 1 } }) : url.includes("/crews?") ? ok(CREWS) : url.includes("/crew-connections") ? ok(CONNS) : ok({}))
  window.history.replaceState(null, "", "/settings/crew-links")
})
afterEach(() => cleanup())

const TIERS = [
  { role: "MEMBER", write: false, audit: false },
  { role: "MANAGER", write: true, audit: false },
  { role: "ADMIN", write: true, audit: true },
  { role: "OWNER", write: true, audit: true },
] as const

describe.each(TIERS)("$role", ({ role: r, write, audit }) => {
  beforeEach(() => { role = r })

  it(`${write ? "edits" : "only reads"} on the crew page`, async () => {
    render(<CrewLinksPage />)
    const works = await screen.findByRole("region", { name: "Works with" })
    expect(within(works).queryAllByRole("radiogroup").length > 0).toBe(write)
    expect(screen.queryByRole("button", { name: "New link" }) !== null).toBe(write)
    expect(within(works).queryByRole("button", { name: "Link a crew" }) !== null).toBe(write)
    expect(within(works).queryByRole("button", { name: /^Unlink / }) !== null).toBe(write)
    if (!write) {
      // Level pills instead of ladders.
      expect(within(works).getAllByText("View files").length).toBeGreaterThan(0)
      expect(within(works).getAllByText("Deliver files").length).toBeGreaterThan(0)
    }
  })

  it("explains who may change links once, in the head", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Works with" })
    expect(screen.getAllByText(/Managers and up change links/)).toHaveLength(1)
    expect(screen.queryByText(/Read-only/)).toBeNull()
  })

  it(`${audit ? "shows" : "hides"} Changes to these links and ${audit ? "reads" : "never asks"} the audit list`, async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Works with" })
    expect(screen.queryByRole("region", { name: "Changes to these links" }) !== null).toBe(audit)
    expect(auditCalls().length > 0).toBe(audit)
  })

  it(`${write ? "opens" : "does not open"} the link dialog from a matrix cell`, async () => {
    window.history.replaceState(null, "", "/settings/crew-links?view=matrix")
    render(<CrewLinksPage />)
    const grid = await screen.findByRole("grid", { name: "Rows hand work to columns" })
    expect(within(grid).queryAllByRole("button").length > 0).toBe(write)
    expect(within(grid).getByLabelText("Engineering to Ops: View files")).toBeInTheDocument()
  })

  it("lists are the same for everyone", async () => {
    window.history.replaceState(null, "", "/settings/crew-links?view=all")
    render(<CrewLinksPage />)
    const list = await screen.findByRole("region", { name: "All links" })
    expect(within(list).getByRole("row", { name: "Engineering and Ops" })).toBeInTheDocument()
  })
})
