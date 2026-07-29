import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

import { ConnectionsSection } from "../connections-section"

// The old screen was a from/to/direction form plus a flat list of every link
// in the workspace. Two problems: you had to hold "which way round did I pick"
// in your head while filling a form, and the list answered a question nobody
// asks ("all edges, newest first") instead of the one everybody asks ("who can
// Engineering hand work to?").
//
// This view answers that one: pick a crew, see every other crew with the state
// of that pair, change it in one control.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

let role = "MANAGER"
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({
    abilities: { can: () => true },
    role,
    capabilities: null,
    hasCapability: () => false,
    loading: false,
  }),
}))

function jsonResponse(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body }
}

// Radix Select opens on pointerdown, and jsdom needs the explicit
// down/up pair before the portal content mounts.
function openSelect(trigger: HTMLElement) {
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false, pointerId: 1 })
  fireEvent.pointerUp(trigger, { button: 0, pointerId: 1 })
  fireEvent.click(trigger)
}

const CREWS = [
  { id: "c-eng", name: "Engineering", slug: "engineering", color: "blue", icon: "code" },
  { id: "c-ops", name: "Ops", slug: "ops", color: "green", icon: "shield" },
  { id: "c-qa", name: "Quality", slug: "quality", color: "purple", icon: "bug" },
]

function conn(id: string, from: string, to: string, direction = "bidirectional") {
  return {
    id,
    from_crew_id: from,
    from_crew_name: CREWS.find((c) => c.id === from)!.name,
    from_crew_slug: CREWS.find((c) => c.id === from)!.slug,
    to_crew_id: to,
    to_crew_name: CREWS.find((c) => c.id === to)!.name,
    to_crew_slug: CREWS.find((c) => c.id === to)!.slug,
    direction,
    status: "active",
    created_at: new Date().toISOString(),
  }
}

function mockApi(connections: unknown[]) {
  apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === "POST") return jsonResponse({ id: "cc-new" }, 201)
    if (init?.method === "DELETE") return jsonResponse(null, 204)
    if (url.startsWith("/api/v1/crew-connections")) return jsonResponse(connections)
    if (url.startsWith("/api/v1/crews")) return jsonResponse(CREWS)
    return jsonResponse(null, 404)
  })
}

/** The state control for the pair (selected crew ↔ other crew). */
function pairControl(otherName: string) {
  return screen.getByRole("combobox", { name: new RegExp(`link .*${otherName}`, "i") })
}

const mutations = () =>
  apiFetch.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method)

describe("Crew links — per-crew view", () => {
  beforeEach(() => {
    cleanup()
    role = "MANAGER"
    apiFetch.mockReset()
    mockApi([conn("cc-eng-ops", "c-eng", "c-ops")])
  })

  it("opens on the first crew and lists every other crew as a pair", async () => {
    render(<ConnectionsSection workspaceId="ws1" />)
    await screen.findByRole("button", { name: "Engineering" })

    // The rail carries all three; the pair list carries the other two.
    for (const name of ["Engineering", "Ops", "Quality"]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument()
    }
    expect(pairControl("Ops")).toBeInTheDocument()
    expect(pairControl("Quality")).toBeInTheDocument()
    // A crew is never offered a link to itself — that is the one pair that
    // cannot exist, and the old form only caught it on submit.
    expect(
      screen.queryByRole("combobox", { name: /between Engineering and Engineering/i }),
    ).toBeNull()
  })

  it("shows the stored state of each pair", async () => {
    render(<ConnectionsSection workspaceId="ws1" />)
    await screen.findByRole("button", { name: "Engineering" })

    expect(pairControl("Ops")).toHaveTextContent(/both ways/i)
    expect(pairControl("Quality")).toHaveTextContent(/not linked/i)
  })

  it("links a pair both ways from the selected crew", async () => {
    render(<ConnectionsSection workspaceId="ws1" />)
    await screen.findByRole("button", { name: "Engineering" })

    openSelect(pairControl("Quality"))
    fireEvent.click(await screen.findByRole("option", { name: /both ways/i }))

    await waitFor(() => expect(mutations()).toHaveLength(1))
    const [url, init] = mutations()[0]
    expect(String(url)).toContain("/api/v1/crew-connections")
    expect((init as RequestInit).method).toBe("POST")
    expect(JSON.parse((init as RequestInit).body as string)).toMatchObject({
      from_crew_id: "c-eng",
      to_crew_id: "c-qa",
      direction: "bidirectional",
    })
  })

  it("unlinks a pair by deleting the stored row", async () => {
    render(<ConnectionsSection workspaceId="ws1" />)
    await screen.findByRole("button", { name: "Engineering" })

    openSelect(pairControl("Ops"))
    fireEvent.click(await screen.findByRole("option", { name: /not linked/i }))

    await waitFor(() => expect(mutations()).toHaveLength(1))
    const [url, init] = mutations()[0]
    expect(String(url)).toContain("/api/v1/crew-connections/cc-eng-ops")
    expect((init as RequestInit).method).toBe("DELETE")
  })

  // The stored row has an orientation. Asking for "sends work" when the row
  // points the other way cannot be expressed as an update of that row — the
  // server would read it as "link both ways" — so the pair is replaced.
  it("re-points a link stored the other way round", async () => {
    mockApi([conn("cc-ops-eng", "c-ops", "c-eng", "unidirectional")])
    render(<ConnectionsSection workspaceId="ws1" />)
    await screen.findByRole("button", { name: "Engineering" })

    // Engineering currently only RECEIVES from Ops.
    expect(pairControl("Ops")).toHaveTextContent(/receives/i)

    openSelect(pairControl("Ops"))
    fireEvent.click(await screen.findByRole("option", { name: /sends work/i }))

    await waitFor(() => expect(mutations()).toHaveLength(2))
    const [delUrl, delInit] = mutations()[0]
    expect((delInit as RequestInit).method).toBe("DELETE")
    expect(String(delUrl)).toContain("cc-ops-eng")
    const [, postInit] = mutations()[1]
    expect(JSON.parse((postInit as RequestInit).body as string)).toMatchObject({
      from_crew_id: "c-eng",
      to_crew_id: "c-ops",
      direction: "unidirectional",
    })
  })

  it("switching the selected crew shows that crew's pairs", async () => {
    render(<ConnectionsSection workspaceId="ws1" />)
    await screen.findByRole("button", { name: "Engineering" })

    fireEvent.click(screen.getByRole("button", { name: "Quality" }))

    // From Quality's side: Engineering and Ops, and no self-pair.
    expect(pairControl("Engineering")).toBeInTheDocument()
    expect(pairControl("Ops")).toBeInTheDocument()
    expect(
      screen.queryByRole("combobox", { name: /between Quality and Quality/i }),
    ).toBeNull()
  })

  it("shows a MEMBER the state without any control that could change it", async () => {
    role = "MEMBER"
    render(<ConnectionsSection workspaceId="ws1" />)
    await screen.findByRole("button", { name: "Engineering" })

    // The graph is readable — it explains why an agent can or cannot reach
    // another crew, which a MEMBER debugging a run needs.
    expect(screen.getByText(/both ways/i)).toBeInTheDocument()
    expect(screen.queryByRole("combobox")).toBeNull()
    expect(mutations()).toHaveLength(0)
  })

  it("says what a link actually does, per direction", async () => {
    render(<ConnectionsSection workspaceId="ws1" />)
    await screen.findByRole("button", { name: "Engineering" })

    // Not "dispatch tasks to the other" — that was the one path a link did
    // NOT enable until the sidecar learned to name a target crew.
    const panel = screen.getByRole("region", { name: /crew links/i })
    expect(within(panel).getByText(/hand work to/i)).toBeInTheDocument()
  })
})
