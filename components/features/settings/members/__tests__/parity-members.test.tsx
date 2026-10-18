import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

// v67 parity for Settings › Members (PAR-101, 102, 104): the KPI strip, the
// sans table header, the flat pills and chips and the d MMM joined date.
// jsdom has no layout, so these pin the structure and the classes behind the
// sizes; e2e/settings-h2.spec.ts measures them in a browser after deploy.

const api = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() } }))
vi.mock("@/components/features/members/invite-member-dialog", () => ({ InviteMemberDialog: () => <div data-testid="invite-dialog" /> }))
const h = vi.hoisted(() => ({ role: "ADMIN" as string | null, mobile: false }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws1", role: h.role, loading: false }) }))
vi.mock("@/hooks/use-auth", () => ({ useAuth: () => ({ session: { user: { id: "u-caller", name: "Caller", email: "c@x.io" } } }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => h.mobile }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn(), back: vi.fn() }),
  useSearchParams: () => new URLSearchParams(window.location.search),
  usePathname: () => "/settings/members",
}))

import { MembersPage } from "../members-page"

beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn()
  Element.prototype.hasPointerCapture = vi.fn(() => false)
  Element.prototype.setPointerCapture = vi.fn()
  Element.prototype.releasePointerCapture = vi.fn()
})

const member = (id: string, role: string, userId: string, name: string, mode?: string) => ({
  id, role, access_mode: mode, created_at: "2026-03-03T10:00:00Z", user: { id: userId, email: `${userId}@x.io`, full_name: name, avatar_url: null },
})
const ROSTER = [
  member("m1", "OWNER", "u-olive", "Olive Owner"),
  member("m2", "ADMIN", "u-ada", "Ada Admin"),
  member("m3", "MEMBER", "u-mel", "Mel Member", "restricted"),
  member("m4", "VIEWER", "u-vic", "Vic Viewer"),
]
const CAPS = {
  members: [
    { user_id: "u-olive", role: "OWNER", capabilities: ["chat", "routine.create", "issue.create", "credentials:reveal"] },
    { user_id: "u-ada", role: "ADMIN", capabilities: ["chat", "credentials:reveal"] },
    { user_id: "u-mel", role: "MEMBER", capabilities: ["chat", "issue.create"] },
    { user_id: "u-vic", role: "VIEWER", capabilities: ["chat"] },
  ],
}
const ok = (b: unknown) => ({ ok: true, status: 200, json: async () => b, text: async () => "" })

beforeEach(() => {
  cleanup()
  api.mockReset()
  api.mockImplementation(async (url: string) => {
    if (url.includes("/members/capabilities")) return ok(CAPS)
    if (url.includes("/members?")) return ok(ROSTER)
    if (url.includes("/invitations")) return ok([{ id: "i1", email: "a@x.io", role: "MEMBER", expires_at: new Date(Date.now() + 5 * 86_400_000).toISOString(), created_at: new Date().toISOString() }])
    if (url.includes("/reveal-policy")) return ok({ enabled: false })
    return ok({})
  })
  h.role = "ADMIN"
  h.mobile = false
})
afterEach(() => cleanup())

async function renderPage() {
  window.history.replaceState(null, "", "/settings/members")
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(<QueryClientProvider client={qc}><MembersPage /></QueryClientProvider>)
  await screen.findByRole("table", { name: "People and what they may do" })
  await waitFor(() => expect(document.querySelector("[data-area]")).not.toBeNull())
}

describe("PAR-102 summary strip", () => {
  it("is five KPI tiles: People, Invited, Chat only, Can reveal secrets, Above their role", async () => {
    await renderPage()
    const strip = document.querySelector<HTMLElement>("[data-slot=members-summary]")!
    const tiles = Array.from(strip.querySelectorAll<HTMLElement>("[data-slot=settings-kpi]"))
    await waitFor(() => expect(tiles.map((t) => t.querySelector("small")!.textContent)).toEqual(["People", "Invited", "Chat only", "Can reveal secrets", "Above their role"]))
    const value = (label: string) => tiles.find((t) => t.querySelector("small")!.textContent === label)!.querySelector("b")!
    expect(value("People")).toHaveTextContent("4")
    expect(value("Can reveal secrets")).toHaveTextContent("2")
    expect(value("People").className).toContain("tabular-nums")
    expect(value("People").className).not.toContain("font-mono")
    // The two that ask for a look are buttons that filter the roster.
    expect(tiles[3].tagName).toBe("BUTTON")
  })
})

describe("PAR-104 table and pills", () => {
  it("heads the table in sans, sentence case", async () => {
    await renderPage()
    const table = screen.getByRole("table", { name: "People and what they may do" })
    const heads = Array.from(table.querySelectorAll("thead th"))
    for (const th of heads) {
      expect(th.className).not.toContain("font-mono")
      expect(th.className).not.toContain("uppercase")
    }
    expect(heads.map((t) => t.textContent).filter(Boolean)).toEqual(["Person", "Role", "Can do", "Joined", "Open"])
    for (const g of Array.from(table.querySelectorAll("th[scope=rowgroup]"))) expect(g.className).not.toContain("font-mono")
  })

  it("draws the role as a flat sans pill and the chips without icons", async () => {
    await renderPage()
    const row = document.querySelector<HTMLElement>("tr[data-member=u-ada]")!
    const role = row.querySelector<HTMLElement>("[data-slot=role-pill]")!
    expect(role).toHaveTextContent("Admin")
    expect(role.className).toContain("font-sans")
    expect(role.className).not.toContain("font-mono")
    expect(role.querySelector("svg")).toBeNull()
    for (const chip of Array.from(document.querySelectorAll<HTMLElement>("[data-area]"))) {
      expect(chip.querySelector("svg"), chip.getAttribute("data-area") ?? "").toBeNull()
      expect(chip.className).toContain("rounded-full")
      expect(chip.className).toContain("px-2")
    }
    const restricted = document.querySelector<HTMLElement>("tr[data-member=u-mel] [data-slot=restricted-pill]")!
    expect(restricted).toHaveTextContent("Restricted")
    expect(restricted.querySelector("svg")).toBeNull()
  })

  it("writes Joined as day and month, in sans", async () => {
    await renderPage()
    const row = document.querySelector<HTMLElement>("tr[data-member=u-ada]")!
    const joined = within(row).getByText(/Mar/)
    expect(joined).toHaveTextContent(/^3 Mar( 2026)?$/)
    expect(joined.className).not.toContain("font-mono")
    expect(joined.className).toContain("tabular-nums")
  })
})

describe("PAR-103 panel groups", () => {
  it("are plain sentence-case headings, with no fold chevron", async () => {
    await renderPage()
    const sections = Array.from(document.querySelectorAll("[data-slot=drill-section]"))
    expect(sections.map((e) => e.firstElementChild!.textContent!.replace(/\d+$/, "").trim())).toEqual(["People", "Role", "Can do", "Needs a look"])
    for (const section of sections) {
      expect(section.querySelector("button[aria-expanded]")).toBeNull()
      expect(section.querySelector("svg.lucide-chevron-down")).toBeNull()
      expect(section.firstElementChild!.className).not.toContain("uppercase")
      expect(section.firstElementChild!.className).not.toContain("font-mono")
    }
  })
})
