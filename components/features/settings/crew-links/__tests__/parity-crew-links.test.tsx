import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

// v67 parity for Settings › Crew links (PAR-107, 109): the toolbar with the
// CLI hint and New link, the KPI strip, the radial link map, the single
// column of "Hands work to" and the legend. jsdom has no layout; the sizes
// are measured by e2e/settings-h2.spec.ts after deploy.

const api = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws" }), useCurrentWorkspaceId: () => "ws" }))
let role = "OWNER"
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn(), message: vi.fn() }))
vi.mock("sonner", () => ({ toast }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }), usePathname: () => "/settings/crew-links" }))

import { CrewLinksPage } from "../crew-links-page"

const CREWS = [
  { id: "eng", name: "Engineering", slug: "engineering", color: "#3B82F6" },
  { id: "ops", name: "Ops", slug: "ops", color: "#EF4444" },
  { id: "qa", name: "Quality", slug: "quality", color: "#10B981" },
  { id: "s4", name: "Sampler", slug: "sampler-v4" },
  { id: "s5", name: "Sampler", slug: "sampler-v5" },
]
const CONNS = [
  { id: "c1", from_crew_id: "eng", to_crew_id: "ops", direction: "bidirectional", status: "active", forward_file_access: "read", reverse_file_access: "read_write", access_version: 3 },
  { id: "c2", from_crew_id: "eng", to_crew_id: "qa", direction: "unidirectional", status: "active", forward_file_access: "none", reverse_file_access: "none", access_version: 1 },
]
const ok = (b: unknown) => ({ ok: true, status: 200, json: async () => b })

beforeEach(() => {
  cleanup()
  api.mockReset()
  api.mockImplementation(async (url: string) => (url.includes("/crews?") ? ok(CREWS) : url.includes("/crew-connections") ? ok(CONNS) : ok({})))
  role = "OWNER"
  window.history.replaceState(null, "", "/settings/crew-links")
})
afterEach(() => cleanup())

describe("PAR-107 toolbar", () => {
  it("has the CLI hint and a blue New link", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Hands work to" })
    const cli = screen.getByRole("button", { name: "Command line" })
    fireEvent.click(cli)
    expect(toast.info).toHaveBeenCalledWith(expect.stringContaining("crewship crew connect <A> <B>"))
    expect(screen.getByRole("button", { name: "New link" })).toHaveAttribute("data-variant", "default")
  })

  it("keeps New link for those who may change links", async () => {
    role = "MEMBER"
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Hands work to" })
    expect(screen.getByRole("button", { name: "Command line" })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "New link" })).toBeNull()
  })

  it("New link picks the two crews, then opens that direction", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Hands work to" })
    fireEvent.click(screen.getByRole("button", { name: "New link" }))
    const dialog = await screen.findByRole("dialog", { name: "New link" })
    fireEvent.change(within(dialog).getByLabelText("From crew"), { target: { value: "qa" } })
    fireEvent.change(within(dialog).getByLabelText("To crew"), { target: { value: "ops" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Continue" }))
    expect(await screen.findByRole("dialog", { name: "Quality → Ops" })).toBeInTheDocument()
  })
})

describe("PAR-109 summary, link map and files", () => {
  it("draws the summary as four KPI tiles", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Hands work to" })
    const strip = document.querySelector<HTMLElement>("[data-slot=crew-links-summary]")!
    const tiles = Array.from(strip.querySelectorAll<HTMLElement>("[data-slot=settings-kpi]"))
    expect(tiles.map((t) => t.querySelector("small")!.textContent)).toEqual(["Links", "Directions", "Can deliver files", "Work alone"])
    expect(tiles.map((t) => t.querySelector("b")!.textContent)).toEqual(["2", "3", "1", "2"])
  })

  it("draws the link map as v67 does: lines to a crew circle, names beside them", async () => {
    render(<CrewLinksPage />)
    const map = await screen.findByRole("region", { name: "Link map" })
    const svg = map.querySelector("svg")!
    expect(svg.getAttribute("viewBox")).toMatch(/^0 0 640 \d+$/)
    // Engineering hands work to Ops and Quality and receives from Ops: three lines.
    expect(svg.querySelectorAll("line")).toHaveLength(3)
    const centre = svg.querySelector("circle[data-slot=link-map-centre]")!
    expect(centre.getAttribute("r")).toBe("30")
    expect(within(map).getAllByRole("button", { name: "Open Ops" })).toHaveLength(2)
    expect(within(map).getByText("Receives work from")).toBeInTheDocument()
    expect(within(map).getByText("Hands work to")).toBeInTheDocument()
    expect(map.querySelector("foreignObject")).toBeNull()
    // The line to a crew whose files may be read is drawn in the "read" colour.
    expect(svg.querySelector("line[data-files=read_write]")).not.toBeNull()
  })

  it("lists Hands work to and Receives work from one under the other", async () => {
    render(<CrewLinksPage />)
    const out = await screen.findByRole("region", { name: "Hands work to" })
    expect(out.parentElement!.className).not.toContain("grid-cols-2")
    expect(out.parentElement!.className).not.toContain("xl:grid")
  })

  it("explains the colours with small squares, and says what a link never grants", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Hands work to" })
    const legend = document.querySelector<HTMLElement>("[data-slot=crew-links-legend]")!
    expect(legend).toHaveTextContent("Work — may hand work")
    expect(legend).toHaveTextContent("View — reads their shared files")
    expect(legend).toHaveTextContent("View + deliver — reads and delivers into their incoming folder")
    expect(legend).toHaveTextContent("Links never grant shell, credentials or private agent homes.")
    expect(legend.querySelectorAll("i[data-swatch]")).toHaveLength(3)
    expect(legend.querySelector("[class*='font-mono']")).toBeNull()
  })
})

describe("PAR-103 panel groups", () => {
  it("are plain sentence-case headings, with no fold chevron", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Hands work to" })
    const sections = Array.from(document.querySelectorAll("[data-slot=drill-section]"))
    expect(sections.length).toBeGreaterThan(0)
    for (const section of sections) expect(section.querySelector("button[aria-expanded]")).toBeNull()
  })
})
