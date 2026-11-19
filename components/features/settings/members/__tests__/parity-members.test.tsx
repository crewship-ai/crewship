import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

// v67 parity for Settings › Members (PAR-101, 103, 104): the sans table
// header, the flat pills and the d MMM joined date. (PAR-102's strip of five
// figures is gone: every fact is shown once, in the panel or the banner.)
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
  await screen.findByRole("table", { name: "People and their roles" })
  await waitFor(() => expect(document.querySelector("[data-slot=abilities-line]")).not.toBeNull())
}

describe("no strip of figures", () => {
  it("repeats nothing the panel and the banner already say", async () => {
    await renderPage()
    expect(document.querySelector("[data-slot=members-summary]")).toBeNull()
    expect(document.querySelector("[data-slot=settings-kpi]")).toBeNull()
    expect(document.querySelectorAll("[data-slot=members-look-banner]")).toHaveLength(1)
  })
})

describe("PAR-104 table and pills", () => {
  it("heads the table in sans, sentence case", async () => {
    await renderPage()
    const table = screen.getByRole("table", { name: "People and their roles" })
    const heads = Array.from(table.querySelectorAll("thead th"))
    for (const th of heads) {
      expect(th.className).not.toContain("font-mono")
      expect(th.className).not.toContain("uppercase")
    }
    expect(heads.map((t) => t.textContent).filter(Boolean)).toEqual(["Person", "Role", "Abilities", "Joined", "Open"])
    // Role groups read like every other grouped list (v67 .grp): "Owners 1".
    for (const g of Array.from(table.querySelectorAll("th[scope=rowgroup]"))) {
      expect(g.className).not.toContain("uppercase")
      expect(g.textContent).not.toContain("·")
    }
    for (const g of Array.from(table.querySelectorAll("th[scope=rowgroup]"))) expect(g.className).not.toContain("font-mono")
  })

  it("draws the role as a flat sans pill and abilities as one line", async () => {
    await renderPage()
    const row = document.querySelector<HTMLElement>("tr[data-member=u-ada]")!
    const role = row.querySelector<HTMLElement>("[data-slot=role-pill]")!
    expect(role).toHaveTextContent("Admin")
    expect(role.className).toContain("font-sans")
    expect(role.className).not.toContain("font-mono")
    expect(role.querySelector("svg")).toBeNull()
    const line = row.querySelector<HTMLElement>("[data-slot=abilities-line]")!
    expect(line).toHaveTextContent("Chat only + reveal secrets")
    expect(line.querySelector("svg")).toBeNull()
    const guest = document.querySelector<HTMLElement>("tr[data-member=u-mel] [data-slot=guest-pill]")!
    expect(guest).toHaveTextContent("Guest")
    expect(guest.querySelector("svg")).toBeNull()
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
    expect(sections.map((e) => e.firstElementChild!.textContent!.replace(/\d+$/, "").trim())).toEqual(["People", "Role"])
    for (const section of sections) {
      expect(section.querySelector("button[aria-expanded]")).toBeNull()
      expect(section.querySelector("svg.lucide-chevron-down")).toBeNull()
      expect(section.firstElementChild!.className).not.toContain("uppercase")
      expect(section.firstElementChild!.className).not.toContain("font-mono")
    }
  })
})
