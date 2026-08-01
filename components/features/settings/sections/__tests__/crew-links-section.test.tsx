import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

import { toast } from "sonner"

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

// The colours a seeded workspace actually stores — raw hexes, not palette
// ids. That distinction is the whole reason crews used to render identically.
const CREWS = [
  { id: "c-eng", name: "Engineering", slug: "engineering", color: "#3B82F6", icon: "code" },
  { id: "c-ops", name: "Ops", slug: "ops", color: "#EF4444", icon: "shield" },
  { id: "c-qa", name: "Quality", slug: "quality", color: "#10B981", icon: "bug" },
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

  it("unlinks a pair by deleting the stored row, once confirmed", async () => {
    const confirmSpy = vi.fn(() => true)
    vi.stubGlobal("confirm", confirmSpy)
    render(<ConnectionsSection workspaceId="ws1" />)
    await screen.findByRole("button", { name: "Engineering" })

    openSelect(pairControl("Ops"))
    fireEvent.click(await screen.findByRole("option", { name: /not linked/i }))

    await waitFor(() => expect(mutations()).toHaveLength(1))
    const [url, init] = mutations()[0]
    expect(String(url)).toContain("/api/v1/crew-connections/cc-eng-ops")
    expect((init as RequestInit).method).toBe("DELETE")
    expect(confirmSpy).toHaveBeenCalled()
    vi.unstubAllGlobals()
  })

  // Removing a link severs every dispatch, message and shared-file path
  // between two crews, and the dropdown makes it one arrow-key press away.
  // Declining the question has to mean nothing happened.
  it("does not unlink when the confirmation is declined", async () => {
    const confirmSpy = vi.fn(() => false)
    vi.stubGlobal("confirm", confirmSpy)
    render(<ConnectionsSection workspaceId="ws1" />)
    await screen.findByRole("button", { name: "Engineering" })

    openSelect(pairControl("Ops"))
    fireEvent.click(await screen.findByRole("option", { name: /not linked/i }))

    await waitFor(() => expect(confirmSpy).toHaveBeenCalled())
    expect(mutations()).toHaveLength(0)
    vi.unstubAllGlobals()
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

// The state used to live only in the wording of a dropdown. A link is a
// direction between two things, and direction is something you should be able
// to see without reading — so the row draws it, in the two crews' own colours,
// and the control is a tinted pill rather than a grey select.
describe("Crew links — the link is drawn, not just worded", () => {
  beforeEach(() => {
    cleanup()
    role = "MANAGER"
    apiFetch.mockReset()
    mockApi([conn("cc-eng-ops", "c-eng", "c-ops")])
  })

  it("marks each pair row with the state it is drawing", async () => {
    const { container } = render(<ConnectionsSection workspaceId="ws1" />)
    await screen.findByRole("button", { name: "Engineering" })

    expect(container.querySelector('[data-link-state="both"]')).toBeTruthy()
    expect(container.querySelector('[data-link-state="none"]')).toBeTruthy()
  })

  it("carries each crew's own colour into the drawing", async () => {
    const { container } = render(<ConnectionsSection workspaceId="ws1" />)
    await screen.findByRole("button", { name: "Engineering" })

    const drawn = container.querySelector('[data-link-state="both"]') as HTMLElement
    const style = (drawn.getAttribute("style") ?? "").toLowerCase()
    // Engineering's blue on one end, Ops' red on the other.
    expect(style).toContain("#3b82f6")
    expect(style).toContain("#ef4444")
  })
})

// Both views answer different questions: the editor answers "what can THIS
// crew reach", the matrix answers "show me every pair at once" — which is the
// audit question, and the one a flat list of edges answered worst.
describe("Crew links — matrix overview", () => {
  beforeEach(() => {
    cleanup()
    role = "MANAGER"
    apiFetch.mockReset()
    mockApi([conn("cc-eng-ops", "c-eng", "c-ops"), conn("cc-ops-qa", "c-ops", "c-qa", "unidirectional")])
  })

  async function openMatrix() {
    render(<ConnectionsSection workspaceId="ws1" />)
    await screen.findByRole("button", { name: "Engineering" })
    fireEvent.click(screen.getByRole("button", { name: /matrix/i }))
  }

  it("shows one cell per ordered pair, and none for a crew against itself", async () => {
    await openMatrix()
    const grid = screen.getByRole("table", { name: /crew links/i })
    // 3 crews → 3 rows; each row has 3 cells + the row header.
    expect(within(grid).getAllByRole("row")).toHaveLength(4) // + header row
    expect(within(grid).getAllByText("—")).toHaveLength(3) // the diagonal
  })

  it("reads a one-way link in one direction only", async () => {
    await openMatrix()
    // ops → quality is one-way, so the ops row is on and the quality row is not.
    expect(screen.getByLabelText("Ops can hand work to Quality")).toBeInTheDocument()
    expect(screen.getByLabelText("Quality cannot hand work to Ops")).toBeInTheDocument()
  })

  it("clicking a crew in the matrix opens that crew's pairs for editing", async () => {
    await openMatrix()
    fireEvent.click(screen.getByRole("button", { name: /edit Quality/i }))

    // Back on the editor, on Quality: the pair control for Ops exists, and
    // there is no self-pair.
    expect(
      screen.getByRole("combobox", { name: /between Quality and Ops/i }),
    ).toBeInTheDocument()
  })

  it("is read-only — the one place a link changes is the editor", async () => {
    await openMatrix()
    expect(screen.queryByRole("combobox")).toBeNull()
    expect(mutations()).toHaveLength(0)
  })
})

// ── Re-point rollback (#1594) ────────────────────────────────────────
//
// A re-point is DELETE-then-POST. When the POST is refused, the old link
// is already gone, so the component puts it back. Both halves of that
// have to be honest: the refusal must be reported at all (apiFetch
// RESOLVES on 4xx, so an unchecked `await` reads as success), and a
// FAILED restore must be reported too — otherwise the user is told
// "failed to change the link", reasonably reads it as "nothing
// happened", and walks away believing they still have a link that no
// longer exists.
describe("Crew links — a refused re-point", () => {
  beforeEach(() => {
    cleanup()
    role = "MANAGER"
    apiFetch.mockReset()
    vi.mocked(toast.error).mockReset()
  })

  /** DELETE succeeds; the first POST (the re-point) is refused with
   *  `reason`; the second POST (the restore) answers `restoreStatus`. */
  function mockRefusedRepoint(reason: unknown, restoreStatus: number) {
    let posts = 0
    apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "DELETE") return jsonResponse(null, 204)
      if (init?.method === "POST") {
        posts += 1
        if (posts === 1) return jsonResponse(reason, 409)
        return jsonResponse(posts === 2 ? { id: "cc-restored" } : null, restoreStatus)
      }
      if (url.startsWith("/api/v1/crew-connections")) {
        return jsonResponse([conn("cc-ops-eng", "c-ops", "c-eng", "unidirectional")])
      }
      if (url.startsWith("/api/v1/crews")) return jsonResponse(CREWS)
      return jsonResponse(null, 404)
    })
  }

  async function repointEngToOps() {
    render(<ConnectionsSection workspaceId="ws1" />)
    await screen.findByRole("button", { name: "Engineering" })
    openSelect(pairControl("Ops"))
    fireEvent.click(await screen.findByRole("option", { name: /sends work/i }))
  }

  it("reports the server's reason when the old link is put back", async () => {
    mockRefusedRepoint({ detail: "target crew is archived" }, 201)
    await repointEngToOps()

    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(toast.error).toHaveBeenCalledWith("target crew is archived")
    expect(toast.success).not.toHaveBeenCalled()
  })

  // The one that matters: the delete landed, the create was refused, and
  // the restore was refused too. The pair is now unlinked and only this
  // message says so.
  it("warns that the previous link is gone when the restore also fails", async () => {
    mockRefusedRepoint({ detail: "target crew is archived" }, 500)
    await repointEngToOps()

    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    const msg = String(vi.mocked(toast.error).mock.calls[0][0])
    expect(msg).toContain("target crew is archived")
    expect(msg).toMatch(/could not be restored/i)
    expect(msg).toMatch(/re-create the link manually/i)
  })
})
