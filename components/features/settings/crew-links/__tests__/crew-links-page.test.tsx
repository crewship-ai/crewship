import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

const api = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws" }), useCurrentWorkspaceId: () => "ws" }))
let role = "OWNER"
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role }) }))
let mobile = false
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mobile }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }))

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
const writes = () => api.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method)

beforeEach(() => {
  api.mockReset()
  api.mockImplementation(async (url: string) => (url.includes("/crews?") ? ok(CREWS) : url.includes("/crew-connections") ? ok(CONNS) : ok({})))
  role = "OWNER"
  mobile = false
  window.history.replaceState(null, "", "/settings/crew-links")
})
afterEach(() => cleanup())

// Settings › Crew links as its own page: the crew list lives in the page's
// side panel (no third column), each direction is a sentence with its file
// level, and three views read the same directions.
describe("Crew links page", () => {
  it("opens the first linked crew and states each direction as a sentence", async () => {
    render(<CrewLinksPage />)
    const out = await screen.findByRole("region", { name: "Hands work to" })
    expect(within(out).getByText("Engineering can hand work to Ops and read its shared files")).toBeInTheDocument()
    expect(within(out).getByText("Engineering can hand work to Quality")).toBeInTheDocument()
    const inn = screen.getByRole("region", { name: "Receives work from" })
    expect(within(inn).getByText("Ops can hand work to Engineering, read its shared files and deliver into them")).toBeInTheDocument()
  })

  it("tells crews with the same name apart by slug", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Hands work to" })
    expect(screen.getAllByText("sampler-v4").length).toBeGreaterThan(0)
    expect(screen.getAllByText("sampler-v5").length).toBeGreaterThan(0)
  })

  it("changes one direction's file level with the connection's version", async () => {
    render(<CrewLinksPage />)
    const out = await screen.findByRole("region", { name: "Hands work to" })
    const group = within(out).getByRole("radiogroup", { name: "Shared files: Engineering to Ops" })
    fireEvent.click(within(group).getByRole("radio", { name: /deliver/i }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    const [url, init] = writes()[0] as [string, RequestInit]
    expect(url).toContain("/crew-connections/c1/file-access")
    expect(JSON.parse(String(init.body))).toEqual({ requester_crew_id: "eng", level: "read_write", expected_version: 3 })
  })

  it("dropping one direction of a two-way link keeps the other", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Hands work to" })
    fireEvent.click(screen.getByRole("button", { name: "Stop Engineering handing work to Ops" }))
    await waitFor(() => expect(writes().length).toBeGreaterThanOrEqual(2))
    const [delUrl, del] = writes()[0] as [string, RequestInit]
    expect(del.method).toBe("DELETE")
    expect(delUrl).toContain("expected_version=3")
    const post = JSON.parse(String((writes()[1] as [string, RequestInit])[1].body))
    expect(post).toMatchObject({ from_crew_id: "ops", to_crew_id: "eng", direction: "unidirectional" })
  })

  it("switches to the matrix and records the view in the URL", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Hands work to" })
    fireEvent.click(screen.getByRole("button", { name: "Matrix" }))
    expect(await screen.findByRole("button", { name: /Engineering can hand work to Ops and read its shared files\. Edit/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Quality cannot hand work to Engineering\. Edit/ })).toBeInTheDocument()
    expect(new URLSearchParams(window.location.search).get("view")).toBe("matrix")
  })

  it("is read-only below Manager", async () => {
    role = "MEMBER"
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Hands work to" })
    expect(screen.queryByRole("radiogroup")).toBeNull()
    expect(screen.queryByRole("button", { name: /^Stop /i })).toBeNull()
    expect(screen.getByText(/Read-only/)).toBeInTheDocument()
  })

  it("on a phone the side panel becomes view tabs and a crew picker", async () => {
    mobile = true
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Hands work to" })
    expect(screen.getByRole("tablist", { name: "View" })).toBeInTheDocument()
    expect(screen.getByRole("combobox", { name: "Crew" })).toHaveValue("engineering")
  })
})

// The side panel works like every explorer sidebar: search, a Filter popover,
// facets with counts; the crew is shown with what the database knows of it.
describe("Crew links side panel and profile", () => {
  const RICH = [
    { id: "eng", name: "Engineering", slug: "engineering", icon: "terminal", color: "#3B82F6", network_mode: "restricted", container_memory_mb: 4096, container_cpus: 2,
      runtime_image: "mcr.microsoft.com/devcontainers/javascript-node:22-bookworm@sha256:abc", _count: { agents: 2, members: 1 } },
    { id: "ops", name: "Ops", slug: "ops", icon: "server", color: "#EF4444", network_mode: "free", description: "Keeps the lights on.", _count: { agents: 1, members: 0 } },
    { id: "qa", name: "Quality", slug: "quality", network_mode: "restricted", _count: { agents: 0, members: 0 } },
    { id: "col", name: "Collector", slug: "collector", network_mode: "restricted", _count: { agents: 0, members: 0 } },
  ]
  const AGENTS = [
    { id: "a1", name: "Jamie", slug: "jamie", crew_id: "eng", status: "IDLE" },
    { id: "a2", name: "Taylor", slug: "taylor", crew_id: "eng", status: "STOPPED" },
    { id: "a3", name: "Riley", slug: "riley", crew_id: "ops", status: "IDLE" },
  ]
  beforeEach(() => {
    api.mockImplementation(async (url: string) =>
      url.includes("/crews?") ? ok(RICH) : url.includes("/crew-connections") ? ok(CONNS) : url.includes("/agents?") ? ok(AGENTS) : ok({}))
  })
  const panel = () => screen.getByRole("complementary", { name: "Crew links navigation" })

  it("shows the crew's profile: description, agents, members, network, box and image", async () => {
    render(<CrewLinksPage />)
    const profile = await screen.findByRole("region", { name: "Engineering profile" })
    await waitFor(() => expect(within(profile).getByText(/1 stopped/)).toBeInTheDocument())
    expect(within(profile).getByText(/No description/)).toBeInTheDocument()
    expect(within(profile).getByText("Restricted network")).toBeInTheDocument()
    expect(within(profile).getByText("4 GB · 2 CPU")).toBeInTheDocument()
    expect(within(profile).getByText("javascript-node:22-bookworm")).toBeInTheDocument()
    expect(within(profile).getByRole("link", { name: /Open crew/ })).toHaveAttribute("href", "/crews?crew=engineering")
    expect(within(profile).queryByText("Set icon and colour")).toBeNull()
  })

  it("offers to set an icon for a crew without one, and draws its tile dashed", async () => {
    window.history.replaceState(null, "", "/settings/crew-links?crew=quality")
    render(<CrewLinksPage />)
    const profile = await screen.findByRole("region", { name: "Quality profile" })
    expect(within(profile).getByRole("link", { name: "Set icon and colour" })).toHaveAttribute("href", "/crews?crew=quality")
    expect(within(profile).getByTitle("No icon set")).toBeInTheDocument()
  })

  it("the search finds a crew by one of its agents", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Engineering profile" })
    await waitFor(() => expect(within(panel()).getByText("2 agents · 1 stopped")).toBeInTheDocument())
    fireEvent.change(within(panel()).getByPlaceholderText("Search crews, agents…"), { target: { value: "riley" } })
    expect(within(panel()).queryByRole("button", { name: /Engineering/ })).toBeNull()
    expect(within(panel()).getByRole("button", { name: /^Ops/ })).toBeInTheDocument()
    expect(new URLSearchParams(window.location.search).get("q")).toBe("riley")
  })

  it("the Links facet and the Agents filter narrow the crew list, and a chip removes one", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Engineering profile" })
    fireEvent.click(within(panel()).getByRole("button", { name: /No links/ }))
    expect(within(panel()).queryByRole("button", { name: /^Engineering/ })).toBeNull()
    expect(within(panel()).getByRole("button", { name: /^Collector/ })).toBeInTheDocument()
    fireEvent.click(within(panel()).getByRole("button", { name: /Filter/ }))
    fireEvent.click(screen.getByRole("button", { name: /With agents/ }))
    expect(within(panel()).queryByRole("button", { name: /^Collector/ })).toBeNull()
    expect(new URLSearchParams(window.location.search).get("agents")).toBe("with")
    fireEvent.click(screen.getByRole("button", { name: "Remove filter No links" }))
    expect(within(panel()).getByRole("button", { name: /^Engineering/ })).toBeInTheDocument()
    expect(new URLSearchParams(window.location.search).get("links")).toBeNull()
  })

  it("the link map shows who hands work in and out, and a node opens that crew", async () => {
    render(<CrewLinksPage />)
    const map = await screen.findByRole("region", { name: "Link map" })
    expect(within(map).getAllByRole("button", { name: "Open Ops" })).toHaveLength(2)
    expect(within(map).getAllByRole("button", { name: "Open Quality" })).toHaveLength(1)
    fireEvent.click(within(map).getAllByRole("button", { name: "Open Ops" })[0])
    expect(await screen.findByRole("region", { name: "Ops profile" })).toBeInTheDocument()
    expect(new URLSearchParams(window.location.search).get("crew")).toBe("ops")
  })

  it("heads the matrix columns with the crews' icons, named on hover", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Engineering profile" })
    fireEvent.click(screen.getByRole("button", { name: "Matrix" }))
    const grid = await screen.findByRole("grid", { name: "Rows hand work to columns" })
    expect(within(grid).getByRole("columnheader", { name: "Engineering (engineering)" })).toHaveAttribute("title", "Engineering (engineering)")
    expect(within(grid).getByRole("columnheader", { name: "Collector (collector)" })).toBeInTheDocument()
  })
})
