import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react"
import { Building } from "lucide-react"

// v67 parity (PAR-086, 087, 088, 089, 090): the Settings shell measured
// against the approved wireframe. jsdom has no layout, so these pin the
// classes that produce the v67 sizes; e2e/settings-h2.spec.ts measures them
// in a real browser after deploy.

const h = vi.hoisted(() => ({ mobile: false }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => h.mobile }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn(), push: vi.fn(), prefetch: vi.fn(), back: vi.fn(), forward: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(window.location.search),
  usePathname: () => "/settings",
}))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "w1", role: "OWNER", loading: false }),
}))
vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    session: { user: { id: "u1", name: "Ada", email: "ada@example.com" } },
    signOut: vi.fn().mockResolvedValue(undefined),
    refresh: vi.fn(),
  }),
}))
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => false }, hasCapability: () => false }),
}))
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async (url: string) => ({
    ok: true,
    status: 200,
    json: async () => (url.includes("/members") ? [] : { id: "w1", name: "Acme", slug: "acme", preferred_language: null, _count: { crews: 1, agents: 2, members: 3 } }),
  })),
}))
vi.mock("../sections/general-section", () => ({ GeneralSection: () => <div data-testid="general-section" /> }))

import { SettingsCard } from "../shared"
import { SettingsNav } from "../settings-nav"
import { SettingsLayout } from "../settings-layout"
import { ProfileSection } from "../sections/profile-section"

beforeEach(() => {
  cleanup()
  h.mobile = false
  window.history.replaceState(null, "", "/settings?tab=general")
})
afterEach(cleanup)

describe("PAR-086 content column", () => {
  it("is 820 px wide, as in v67, not the 768 px max-w-3xl", async () => {
    render(<SettingsLayout />)
    await screen.findByTestId("general-section")
    const col = document.querySelector("[data-slot=settings-page-head]")!.closest("div.mx-auto")!
    expect(col.className.split(" ")).toContain("max-w-[820px]")
    expect(col.className.split(" ")).not.toContain("max-w-3xl")
  })
})

describe("PAR-087 section list rows", () => {
  it("are 34 px high with a 13 px label and a 24 px tile", () => {
    render(<SettingsNav activeTab="profile" onTabChange={vi.fn()} role="OWNER" />)
    const row = screen.getByRole("button", { name: "Appearance" })
    const cls = row.className.split(" ")
    // 34 px at the 14.857 px root of the Small text size.
    expect(cls).toContain("h-[2.2885rem]")
    expect(cls).toContain("py-0")
    expect(row.querySelector("span.truncate")!.className).toContain("text-control")
    expect(row.querySelector("span[aria-hidden]")!.className).toContain("h-6")
  })

  it("gives every row a tile, the drill rows a chevron", () => {
    render(<SettingsNav activeTab="profile" onTabChange={vi.fn()} role="OWNER" />)
    const nav = screen.getByRole("navigation", { name: "Settings sections" })
    for (const name of ["Profile", "Appearance", "General", "Members", "Crew links"]) {
      expect(within(nav).getByRole("button", { name }).querySelector("span[aria-hidden]")).not.toBeNull()
    }
    expect(within(nav).getByRole("button", { name: "Members" }).querySelector("svg.lucide-chevron-right")).not.toBeNull()
    expect(within(nav).getByRole("button", { name: "General" }).querySelector("svg.lucide-chevron-right")).toBeNull()
  })
})

describe("PAR-088 card header", () => {
  it("is the 68 px v67 header: 14 px padding, 32 px tile, 13 px title, 12 px subtitle", () => {
    render(<SettingsCard title="Account" description="Your identity on this instance" icon={Building}><p>row</p></SettingsCard>)
    const header = screen.getByRole("region", { name: "Account" }).querySelector("[data-slot=settings-card-header]")!
    const cls = header.className.split(" ")
    expect(cls).toContain("min-h-[4.5767rem]")
    expect(cls).toContain("py-4")
    expect(cls).not.toContain("min-h-[3.3654rem]")
    expect(screen.getByRole("heading", { name: "Account" }).className).toContain("text-control")
    expect(screen.getByText("Your identity on this instance").className).toContain("text-label")
  })
})

describe("PAR-089 / PAR-090 profile", () => {
  it("offers Change and Remove when a picture exists", () => {
    render(<ProfileSection userName="Ada" userEmail="ada@example.com" userAvatarUrl="https://example.com/a.png" />)
    // Change for the picture, Change for the password.
    expect(screen.getAllByRole("button", { name: "Change" })).toHaveLength(2)
    expect(screen.getByRole("button", { name: "Remove" })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Upload" })).toBeNull()
  })

  it("draws the role as a sans pill and the join date as d MMM yyyy", async () => {
    render(<ProfileSection userName="Ada" userEmail="ada@example.com" role="OWNER" workspaceName="Acme" joinedAt="2026-10-12T10:00:00Z" />)
    const pill = document.querySelector<HTMLElement>("[data-slot=status-pill]")!
    expect(pill).toHaveTextContent("Owner")
    expect(pill.className.split(" ")).toContain("font-sans")
    expect(pill.className.split(" ")).not.toContain("font-mono")
    await waitFor(() => expect(screen.getByText("12 Oct 2026")).toBeInTheDocument())
    expect(screen.getByText("12 Oct 2026").className).not.toContain("font-mono")
    expect(screen.getByText("12 Oct 2026").className).toContain("tabular-nums")
  })
})
