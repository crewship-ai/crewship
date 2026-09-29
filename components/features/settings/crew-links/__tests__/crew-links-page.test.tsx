import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

const api = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws" }) }))
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
