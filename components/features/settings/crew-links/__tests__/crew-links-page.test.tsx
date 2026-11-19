import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

const api = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws" }), useCurrentWorkspaceId: () => "ws" }))
let role = "OWNER"
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role }) }))
let mobile = false
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mobile }))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn(), message: vi.fn() }))
vi.mock("sonner", () => ({ toast }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }), usePathname: () => "/settings/crew-links" }))

import { CrewLinksPage } from "../crew-links-page"

type Row = { id: string; from_crew_id: string; to_crew_id: string; direction: string; status: string; forward_file_access?: string; reverse_file_access?: string; access_version?: number }

let CREWS: Record<string, unknown>[] = []
let CONNS: Row[] = []
let AUDIT: Record<string, unknown>[] = []

const BASE_CREWS = [
  { id: "eng", name: "Engineering", slug: "engineering", color: "#3B82F6", network_mode: "restricted", _count: { agents: 2, members: 1 } },
  { id: "ops", name: "Ops", slug: "ops", color: "#EF4444", network_mode: "free", description: "Keeps the lights on.", _count: { agents: 1, members: 0 } },
  { id: "qa", name: "Quality", slug: "quality", color: "#10B981", network_mode: "restricted", _count: { agents: 0, members: 0 } },
  { id: "s4", name: "Sampler", slug: "sampler-v4", _count: { agents: 1 } },
  { id: "s5", name: "Sampler", slug: "sampler-v5", _count: { agents: 1 } },
]
const BASE_CONNS: Row[] = [
  { id: "c1", from_crew_id: "eng", to_crew_id: "ops", direction: "bidirectional", status: "active", forward_file_access: "read", reverse_file_access: "read_write", access_version: 3 },
  { id: "c2", from_crew_id: "eng", to_crew_id: "qa", direction: "unidirectional", status: "active", forward_file_access: "none", reverse_file_access: "none", access_version: 1 },
]
const ok = (b: unknown) => ({ ok: true, status: 200, json: async () => b })
const writes = () => api.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method)
const search = () => new URLSearchParams(window.location.search)
const panel = () => screen.getByRole("complementary", { name: "Crew links navigation" })

function serve() {
  api.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      // A new pair appears with the levels it was created with, as the server stores it.
      const b = JSON.parse(String(init.body))
      if (!CONNS.some((c) => (c.from_crew_id === b.from_crew_id && c.to_crew_id === b.to_crew_id) || (c.from_crew_id === b.to_crew_id && c.to_crew_id === b.from_crew_id))) {
        CONNS = [...CONNS, { id: `n${CONNS.length}`, from_crew_id: b.from_crew_id, to_crew_id: b.to_crew_id, direction: b.direction, status: "active", forward_file_access: b.forward_file_access ?? "none", reverse_file_access: b.reverse_file_access ?? "none", access_version: 1 }]
      }
      return ok({})
    }
    if (init?.method) return ok({})
    if (url.includes("/api/v1/audit")) return ok({ data: AUDIT, pagination: { total_pages: 1 } })
    if (url.includes("/crews?")) return ok(CREWS)
    if (url.includes("/crew-connections")) return ok(CONNS)
    return ok({})
  })
}

beforeEach(() => {
  api.mockReset()
  Object.values(toast).forEach((f) => f.mockReset())
  CREWS = BASE_CREWS
  CONNS = BASE_CONNS
  AUDIT = []
  serve()
  role = "OWNER"
  mobile = false
  window.history.replaceState(null, "", "/settings/crew-links")
})
afterEach(() => cleanup())

/** n crews named C01.. all linked to a hub, so the lists and the panel have something to cut. */
function many(n: number) {
  CREWS = [{ id: "hub", name: "Hub", slug: "hub" }, ...Array.from({ length: n }, (_, i) => ({ id: `c${i}`, name: `Crew ${String(i).padStart(2, "0")}`, slug: `crew-${i}`, _count: { agents: 1 } }))]
  CONNS = Array.from({ length: n }, (_, i) => ({
    id: `l${i}`, from_crew_id: "hub", to_crew_id: `c${i}`, direction: i % 2 ? "bidirectional" : "unidirectional", status: "active",
    forward_file_access: ["none", "read", "read_write"][i % 3], reverse_file_access: "none", access_version: 1,
  }))
  serve()
}

describe("Crew page: one row per partner, two ladders when open", () => {
  it("opens the most linked crew and shows each direction as a ladder under its sentence", async () => {
    render(<CrewLinksPage />)
    const works = await screen.findByRole("region", { name: "Works with" })
    // Two partners: all expanded.
    const toOps = within(works).getByRole("radiogroup", { name: "Engineering to Ops" })
    expect(within(toOps).getAllByRole("radio").map((r) => r.textContent)).toEqual(["Off", "Work only", "View files", "Deliver files"])
    expect(within(toOps).getByRole("radio", { name: "View files" })).toBeChecked()
    expect(within(works).getByText("Assigns tasks, and reads Ops’s shared files")).toBeInTheDocument()
    expect(within(works).getByRole("radiogroup", { name: "Ops to Engineering" })).toBeInTheDocument()
    expect(within(works).getByRole("radio", { name: "Deliver files", checked: true })).toBeInTheDocument()
    expect(within(works).getByText("Quality cannot hand work to Engineering")).toBeInTheDocument()
  })

  it("heads the page with the crew's facts and keeps the old profile's details", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Works with" })
    const head = document.querySelector<HTMLElement>("[data-slot=crew-profile]")!
    await waitFor(() => expect(head).toHaveTextContent("2 agents · Restricted network"))
    expect(head).toHaveTextContent("No description")
    expect(within(head).getByRole("link", { name: /Open crew/ })).toHaveAttribute("href", "/crews?crew=engineering")
    expect(head).toHaveTextContent("1 member")
  })

  it("tells crews with the same name apart by slug", async () => {
    window.history.replaceState(null, "", "/settings/crew-links?view=crews")
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Crews" })
    expect(screen.getAllByText("sampler-v4").length).toBeGreaterThan(0)
    expect(screen.getAllByText("sampler-v5").length).toBeGreaterThan(0)
  })

  it("a ladder change saves at once, with the connection's version, and the toast names the new state", async () => {
    render(<CrewLinksPage />)
    const works = await screen.findByRole("region", { name: "Works with" })
    fireEvent.click(within(within(works).getByRole("radiogroup", { name: "Engineering to Ops" })).getByRole("radio", { name: "Deliver files" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    const [url, init] = writes()[0] as [string, RequestInit]
    expect(url).toContain("/crew-connections/c1/file-access")
    expect(JSON.parse(String(init.body))).toEqual({ requester_crew_id: "eng", level: "read_write", expected_version: 3 })
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Engineering → Ops: Deliver files"))
  })

  it("putting one direction of a two-way link Off keeps the other", async () => {
    render(<CrewLinksPage />)
    const works = await screen.findByRole("region", { name: "Works with" })
    fireEvent.click(within(within(works).getByRole("radiogroup", { name: "Engineering to Ops" })).getByRole("radio", { name: "Off" }))
    await waitFor(() => expect(writes().length).toBeGreaterThanOrEqual(2))
    const [delUrl, del] = writes()[0] as [string, RequestInit]
    expect(del.method).toBe("DELETE")
    expect(delUrl).toContain("expected_version=3")
    expect(JSON.parse(String((writes()[1] as [string, RequestInit])[1].body))).toMatchObject({ from_crew_id: "ops", to_crew_id: "eng", direction: "unidirectional" })
  })

  it("asks before removing the last direction, naming what is lost and kept, and writes nothing until confirmed", async () => {
    render(<CrewLinksPage />)
    const works = await screen.findByRole("region", { name: "Works with" })
    fireEvent.click(within(within(works).getByRole("radiogroup", { name: "Engineering to Quality" })).getByRole("radio", { name: "Off" }))
    const dialog = await screen.findByRole("alertdialog")
    expect(dialog).toHaveTextContent("Unlink Engineering and Quality?")
    expect(dialog).toHaveTextContent("Work handoff in both directions")
    expect(dialog).toHaveTextContent("Both crews, their agents and their files")
    expect(writes()).toHaveLength(0)
    fireEvent.click(within(dialog).getByRole("button", { name: "Unlink" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(((writes()[0] as [string, RequestInit])[1]).method).toBe("DELETE")
  })

  it("Unlink on a partner asks the same question", async () => {
    render(<CrewLinksPage />)
    const works = await screen.findByRole("region", { name: "Works with" })
    fireEvent.click(within(works).getByRole("button", { name: "Unlink Engineering and Ops" }))
    expect(await screen.findByRole("alertdialog")).toHaveTextContent("Unlink Engineering and Ops?")
  })

  it("lists many partners as rows with two pills, opens one on click, and pages eight at a time", async () => {
    many(10)
    render(<CrewLinksPage />)
    const works = await screen.findByRole("region", { name: "Works with" })
    expect(within(works).getAllByRole("button", { expanded: false })).toHaveLength(8)
    expect(within(works).queryByRole("radiogroup")).toBeNull()
    expect(within(works).getByText("8 of 10 crews shown")).toBeInTheDocument()
    const first = within(works).getByRole("button", { name: /^Crew 00:/ })
    expect(first).toHaveTextContent("Work only")
    fireEvent.click(first)
    expect(within(works).getByRole("radiogroup", { name: "Hub to Crew 00" })).toBeInTheDocument()
    fireEvent.click(within(works).getByRole("button", { name: "Show 2 more" }))
    expect(within(works).getByText("All 10 shown")).toBeInTheDocument()
  })

  it("links a crew with this crew prefilled", async () => {
    render(<CrewLinksPage />)
    const works = await screen.findByRole("region", { name: "Works with" })
    fireEvent.click(within(works).getByRole("button", { name: "Link a crew" }))
    const dialog = await screen.findByRole("dialog", { name: "New link" })
    expect(within(dialog).getByRole("combobox", { name: "Crew" })).toHaveTextContent("Engineering")
    expect(within(dialog).getByRole("combobox", { name: "Works with" })).toHaveTextContent("Choose a crew")
  })

  it("shows a muted Inactive pill and reads an inactive row's levels as Off", async () => {
    CONNS = [{ ...BASE_CONNS[0], status: "inactive" }]
    serve()
    render(<CrewLinksPage />)
    const works = await screen.findByRole("region", { name: "Works with" })
    expect(within(works).getByText("Inactive")).toBeInTheDocument()
    expect(within(within(works).getByRole("radiogroup", { name: "Engineering to Ops" })).getByRole("radio", { name: "Off" })).toBeChecked()
  })

  it("an old server that reports no file access gets Off and Work only, and says so once", async () => {
    CONNS = [{ id: "c1", from_crew_id: "eng", to_crew_id: "ops", direction: "unidirectional", status: "active" }]
    serve()
    render(<CrewLinksPage />)
    const works = await screen.findByRole("region", { name: "Works with" })
    const ladder = within(works).getByRole("radiogroup", { name: "Engineering to Ops" })
    expect(within(ladder).getAllByRole("radio").map((r) => r.textContent)).toEqual(["Off", "Work only"])
    expect(within(ladder).getByRole("radio", { name: "Work only" })).toBeChecked()
    expect(screen.getAllByText(/does not report shared-file access/)).toHaveLength(1)
  })

  it("the removed pieces are gone: no KPI strip, no link map, no separate legend", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Works with" })
    expect(document.querySelector("[data-slot=crew-links-summary]")).toBeNull()
    expect(screen.queryByRole("region", { name: "Link map" })).toBeNull()
    expect(document.querySelector("[data-slot=crew-links-legend]")).toBeNull()
    expect(screen.getByRole("region", { name: "What each level allows" })).toHaveTextContent("A link does not grant shell, credentials or agents’ private files")
  })
})

describe("Changes to these links", () => {
  const entry = (id: string, action: string, previous: unknown, current: unknown, name = "Tomáš Krejčí") => ({
    id, action, entity_type: "CREW_LINK", created_at: "2026-11-18T08:12:00Z", user_name: name, metadata: JSON.stringify({ previous, current }),
  })
  const st = (from: string, to: string, f: string, r: string) => ({ from_crew_id: from, to_crew_id: to, forward_file_access: f, reverse_file_access: r })

  it("is read from the audit list for CREW_LINK and keeps this crew's changes", async () => {
    AUDIT = [
      entry("a1", "crew_link.file_access", st("eng", "ops", "read", "read_write"), st("eng", "ops", "read_write", "read_write")),
      entry("a2", "crew_link.create", null, st("s4", "s5", "none", "none"), "Jana Nováková"),
    ]
    render(<CrewLinksPage />)
    const card = await screen.findByRole("region", { name: "Changes to these links" })
    await waitFor(() => expect(within(card).getByText("Engineering → Ops: View files → Deliver files")).toBeInTheDocument())
    expect(within(card).getByText("by Tomáš Krejčí")).toBeInTheDocument()
    expect(within(card).queryByText(/Sampler/)).toBeNull()
    expect(within(card).getByRole("link", { name: "Open the audit log" })).toHaveAttribute("href", "/settings/audit?audit_type=CREW_LINK")
    const auditCall = api.mock.calls.find(([u]) => String(u).includes("/api/v1/audit"))![0] as string
    expect(auditCall).toContain("entity_type=CREW_LINK")
  })
})

describe("Panel", () => {
  it("names four views with their counts, the most linked crews, and an All N crews row", async () => {
    many(12)
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Works with" })
    const p = panel()
    expect(within(p).getByRole("button", { name: /^All links\s*12/ })).toBeInTheDocument()
    expect(within(p).getByRole("button", { name: /^Can deliver files\s*(\d+)/ })).toHaveTextContent("4")
    expect(within(p).getByRole("button", { name: /^All crews\s*13/ })).toBeInTheDocument()
    expect(within(p).getByRole("button", { name: /^Working alone\s*0/ })).toBeInTheDocument()
    // Hub plus seven of its partners: eight crews named, never all thirteen.
    const named = within(p).getAllByRole("button").filter((b) => /^(Hub|Crew \d\d)/.test(b.textContent ?? ""))
    expect(named).toHaveLength(8)
    fireEvent.click(within(p).getByRole("button", { name: "All 13 crews…" }))
    expect(search().get("view")).toBe("crews")
  })

  it("has no spaces switch and no Links section or full crew lists", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Works with" })
    expect(within(panel()).queryByRole("tablist", { name: "Spaces" })).toBeNull()
    expect(within(panel()).queryByText("Linked crews")).toBeNull()
    expect(within(panel()).queryByText("Crews without links")).toBeNull()
  })

  it("opens a crew from Most linked, and keeps Search and the Network and Agents facets", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Works with" })
    fireEvent.click(within(panel()).getByRole("button", { name: /^Ops/ }))
    expect(search().get("crew")).toBe("ops")
    fireEvent.change(within(panel()).getByPlaceholderText("Search crews, agents…"), { target: { value: "qual" } })
    expect(search().get("q")).toBe("qual")
    fireEvent.change(within(panel()).getByPlaceholderText("Search crews, agents…"), { target: { value: "" } })
    fireEvent.click(within(panel()).getByRole("button", { name: /Filter/ }))
    const block = within(panel()).getByRole("group", { name: "Filter crews" })
    fireEvent.click(within(block).getByRole("button", { name: /^Network/ }))
    fireEvent.click(within(block).getByRole("button", { name: /^Open/ }))
    expect(search().get("net")).toBe("open")
    fireEvent.click(within(block).getByRole("button", { name: /^Agents/ }))
    expect(within(block).getByRole("button", { name: /No agents \(collectors\)/ })).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Remove filter Open network" }))
    expect(search().get("net")).toBeNull()
  })

  it("the old ?links= maps to All crews and Working alone", async () => {
    window.history.replaceState(null, "", "/settings/crew-links?links=none")
    const { unmount } = render(<CrewLinksPage />)
    const table = await screen.findByRole("region", { name: "Crews" })
    expect(within(table).getByText("2 crews")).toBeInTheDocument()
    expect(within(table).queryByRole("row", { name: /Engineering/ })).toBeNull()
    unmount()
    window.history.replaceState(null, "", "/settings/crew-links?links=linked")
    render(<CrewLinksPage />)
    expect(await screen.findByText("5 crews")).toBeInTheDocument()
  })
})

describe("List views", () => {
  it("All links is one paged list: A → or ⇄ B with a pill for each direction", async () => {
    window.history.replaceState(null, "", "/settings/crew-links?view=all")
    render(<CrewLinksPage />)
    const list = await screen.findByRole("region", { name: "All links" })
    expect(within(list).getByText("2 links")).toBeInTheDocument()
    const both = within(list).getByRole("row", { name: "Engineering and Ops" })
    expect(within(both).getByLabelText("both ways")).toBeInTheDocument()
    expect(within(both).getByText("View files")).toBeInTheDocument()
    expect(within(both).getByText("Deliver files")).toBeInTheDocument()
    const one = within(list).getByRole("row", { name: "Engineering and Quality" })
    expect(within(one).getByLabelText("one way")).toBeInTheDocument()
    expect(within(one).getByText("Work only")).toBeInTheDocument()
    expect(within(one).getByText("Off")).toBeInTheDocument()
    fireEvent.click(one)
    expect(search().get("crew")).toBe("engineering")
    expect(search().get("view")).toBeNull()
  })

  it("filters by level with chips, and keeps the level in the URL", async () => {
    window.history.replaceState(null, "", "/settings/crew-links?view=all")
    render(<CrewLinksPage />)
    const list = await screen.findByRole("region", { name: "All links" })
    fireEvent.click(within(list).getByRole("button", { name: "Work only" }))
    expect(within(list).getByText("1 link")).toBeInTheDocument()
    expect(search().get("level")).toBe("work")
    fireEvent.click(within(list).getByRole("button", { name: "Any level" }))
    expect(within(list).getByText("2 links")).toBeInTheDocument()
  })

  it("pages the list 25 at a time", async () => {
    many(30)
    window.history.replaceState(null, "", "/settings/crew-links?view=all")
    render(<CrewLinksPage />)
    const list = await screen.findByRole("region", { name: "All links" })
    expect(within(list).getByText("25 of 30 links shown")).toBeInTheDocument()
    fireEvent.click(within(list).getByRole("button", { name: "Show 5 more" }))
    expect(within(list).getByText("All 30 shown")).toBeInTheDocument()
  })

  it("Can deliver files lists links where either direction delivers", async () => {
    window.history.replaceState(null, "", "/settings/crew-links?view=deliver")
    render(<CrewLinksPage />)
    const list = await screen.findByRole("region", { name: "Can deliver files" })
    expect(within(list).getByText("1 link")).toBeInTheDocument()
    expect(within(list).getByRole("row", { name: "Engineering and Ops" })).toBeInTheDocument()
    expect(within(list).queryByRole("group", { name: "Level" })).toBeNull()
  })

  it("switches List and Matrix; the matrix draws at most 12 crews and says so", async () => {
    many(20)
    window.history.replaceState(null, "", "/settings/crew-links?view=all")
    render(<CrewLinksPage />)
    const list = await screen.findByRole("region", { name: "All links" })
    fireEvent.click(within(list).getByRole("radio", { name: "Matrix" }))
    expect(search().get("view")).toBe("matrix")
    const grid = await screen.findByRole("grid", { name: "Rows hand work to columns" })
    expect(within(grid).getAllByRole("columnheader")).toHaveLength(13)
    expect(screen.getByText(/The 12 most linked of 21 crews/)).toBeInTheDocument()
    fireEvent.click(within(grid).getByRole("button", { name: "Hub to Crew 00: Work only" }))
    expect(await screen.findByRole("dialog", { name: "Edit link" })).toBeInTheDocument()
  })

  it("All crews is a paged table that opens the crew's page", async () => {
    window.history.replaceState(null, "", "/settings/crew-links?view=crews")
    render(<CrewLinksPage />)
    const table = await screen.findByRole("region", { name: "Crews" })
    expect(within(table).getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Crew", "Works with", "Hands work to", "Receives from", "Agents", "Network"])
    const eng = within(table).getByRole("row", { name: "Open Engineering" })
    expect(within(eng).getAllByRole("cell").map((c) => c.textContent).slice(1, 5)).toEqual(["2", "2", "1", "2"])
    fireEvent.click(eng)
    expect(search().get("crew")).toBe("engineering")
  })

  it("on a phone the tables become rows and the crew is picked from a select", async () => {
    mobile = true
    window.history.replaceState(null, "", "/settings/crew-links?view=crews")
    render(<CrewLinksPage />)
    const table = await screen.findByRole("region", { name: "Crews" })
    expect(within(table).queryByRole("table")).toBeNull()
    expect(within(table).getByRole("button", { name: /Engineering/ })).toHaveTextContent("works with 2")
    cleanup()
    window.history.replaceState(null, "", "/settings/crew-links")
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Works with" })
    expect(screen.getByRole("combobox", { name: "Crew" })).toHaveValue("engineering")
  })
})

describe("Link dialog", () => {
  async function pick(dialog: HTMLElement, field: string, name: string) {
    fireEvent.click(within(dialog).getByRole("combobox", { name: field }))
    fireEvent.click(await screen.findByRole("option", { name: new RegExp(name) }))
  }

  it("New link: two searchable pickers, each leaving out the other; nothing is written until Save", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Works with" })
    fireEvent.click(screen.getByRole("button", { name: "New link" }))
    const dialog = await screen.findByRole("dialog", { name: "New link" })
    await pick(dialog, "Crew", "Quality")
    fireEvent.click(within(dialog).getByRole("combobox", { name: "Works with" }))
    expect(screen.queryByRole("option", { name: /Quality/ })).toBeNull()
    fireEvent.click(await screen.findByRole("option", { name: /Ops/ }))
    expect(within(dialog).getByRole("button", { name: "Save link" })).toBeDisabled()
    fireEvent.click(within(within(dialog).getByRole("radiogroup", { name: "Quality to Ops" })).getByRole("radio", { name: "View files" }))
    fireEvent.click(within(within(dialog).getByRole("radiogroup", { name: "Ops to Quality" })).getByRole("radio", { name: "Work only" }))
    expect(writes()).toHaveLength(0)
    fireEvent.click(within(dialog).getByRole("button", { name: "Save link" }))
    // Created two-way, then Quality's level (View files); Ops' Work only is what a new direction starts at.
    await waitFor(() => expect(writes()).toHaveLength(2))
    const bodies = writes().map(([, init]) => JSON.parse(String((init as RequestInit).body)))
    expect(bodies[0]).toMatchObject({ from_crew_id: "qa", to_crew_id: "ops", direction: "bidirectional" })
    expect(bodies[1]).toMatchObject({ requester_crew_id: "qa", level: "read" })
  })

  it("an existing pair loads its levels, and closing with changes asks first", async () => {
    window.history.replaceState(null, "", "/settings/crew-links?view=matrix")
    render(<CrewLinksPage />)
    fireEvent.click(await screen.findByRole("button", { name: "Engineering to Ops: View files" }))
    const dialog = await screen.findByRole("dialog", { name: "Edit link" })
    expect(within(within(dialog).getByRole("radiogroup", { name: "Engineering to Ops" })).getByRole("radio", { name: "View files" })).toBeChecked()
    expect(within(within(dialog).getByRole("radiogroup", { name: "Ops to Engineering" })).getByRole("radio", { name: "Deliver files" })).toBeChecked()
    fireEvent.click(within(within(dialog).getByRole("radiogroup", { name: "Engineering to Ops" })).getByRole("radio", { name: "Off" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))
    expect(await screen.findByRole("alertdialog")).toHaveTextContent(/discard/i)
    expect(writes()).toHaveLength(0)
  })

  it("unlinking both directions in the dialog asks with the Unlink confirmation", async () => {
    window.history.replaceState(null, "", "/settings/crew-links?view=matrix")
    render(<CrewLinksPage />)
    fireEvent.click(await screen.findByRole("button", { name: "Engineering to Quality: Work only" }))
    const dialog = await screen.findByRole("dialog", { name: "Edit link" })
    fireEvent.click(within(within(dialog).getByRole("radiogroup", { name: "Engineering to Quality" })).getByRole("radio", { name: "Off" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Save link" }))
    expect(await screen.findByRole("alertdialog")).toHaveTextContent("Unlink Engineering and Quality?")
    expect(writes()).toHaveLength(0)
  })
})

describe("One section head", () => {
  it("says what the page is once, with who may change it", async () => {
    render(<CrewLinksPage />)
    const head = document.querySelector<HTMLElement>("[data-slot=drill-head]")!
    expect(within(head).getByRole("heading", { level: 2, name: "Crew links" })).toBeInTheDocument()
    expect(head).toHaveTextContent("Managers and up change links")
    expect(screen.getAllByText(/Who hands work to whom/)).toHaveLength(1)
    await screen.findByRole("region", { name: "Works with" })
  })

  it("has the CLI hint and a blue New link", async () => {
    render(<CrewLinksPage />)
    await screen.findByRole("region", { name: "Works with" })
    fireEvent.click(screen.getByRole("button", { name: "Command line" }))
    expect(toast.info).toHaveBeenCalledWith(expect.stringContaining("crewship crew connect <A> <B>"))
    expect(screen.getByRole("button", { name: "New link" })).toHaveAttribute("data-variant", "default")
  })
})
