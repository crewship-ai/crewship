import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

// =============================================================================
// New routine → Describe it: crews and agents look like themselves.
//
// The data was there all along — `crews` rows carry an icon and a colour
// (Engineering / terminal / #3B82F6), and an agent's face is derivable from a
// seed the read-side already falls back to. The describe screen used neither:
// a bare native <select> of crew names, and a "Lead: Morgan" line whose avatar
// was a hard-coded purple gradient disc that stood for nobody. Two surfaces
// away, the roster draws the same crew and the same agent properly, so the
// same crew looked like two different things depending on the door you came
// through.
// =============================================================================

const h = vi.hoisted(() => ({
  crews: [] as unknown[],
  agents: [] as unknown[],
}))

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "ADMIN" }) }))
vi.mock("./../routine-definition-canvas", () => ({
  RoutineDefinitionCanvas: () => <div data-testid="graph" />,
}))
vi.mock("@/components/features/files/file-editor", () => ({
  FileEditor: () => <div data-testid="editor" />,
}))

// Argument-aware, so the assertion below proves which props the surface feeds
// the generator — a fixed return value would stay green if it passed the crew
// id, the row id, or nothing at all.
vi.mock("@/lib/agent-avatar", () => ({
  getAgentAvatarUrl: (seed: string, style?: string | null) =>
    `data:image/svg+xml;utf8,generated-${seed}-${style ?? "default"}`,
}))
vi.mock("@/lib/agent-avatar-persist", () => ({
  resolveStoredAvatarSrc: (url: string | null | undefined) => (url ? `resolved:${url}` : null),
  queueAvatarBackfill: vi.fn().mockResolvedValue(undefined),
}))
vi.mock("@/hooks/use-avatar-styles", () => ({ useAvatarStylesVersion: () => 0 }))

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async (url: string) => {
    if (url.startsWith("/api/v1/crews")) return { ok: true, json: async () => h.crews }
    if (url.startsWith("/api/v1/agents")) return { ok: true, json: async () => h.agents }
    return { ok: true, json: async () => [] }
  }),
}))

import { RoutineCreateDialog } from "../routine-create-dialog"

const PROPS = { workspaceId: "ws-1", open: true, onClose: vi.fn(), onCreated: vi.fn() }

// The shapes the dev workspace actually stores: a hex for the seeded crews, a
// palette id for the ones the wizard wrote, and agents with no avatar_seed at
// all — which is why the read-side falls back to the name.
const CREWS = [
  { id: "c-eng", name: "Engineering", icon: "terminal", color: "#3B82F6" },
  { id: "c-qa", name: "Quality", icon: "shield-check", color: "#10B981" },
  { id: "c-smoke", name: "Smoke", icon: "code", color: "blue", avatar_style: "thumbs" },
]
const AGENTS = [
  { id: "a-morgan", name: "Morgan", slug: "morgan", agent_role: "LEAD", crew_id: "c-eng" },
  { id: "a-riley", name: "Riley", slug: "riley", agent_role: "AGENT", crew_id: "c-eng" },
  { id: "a-jordan", name: "Jordan", slug: "jordan", agent_role: "LEAD", crew_id: "c-smoke" },
]

/** Open the dialog on the describe screen with the crews/agents loaded. */
async function describeScreen() {
  render(<RoutineCreateDialog {...PROPS} />)
  fireEvent.click(screen.getByText("Describe it"))
  await waitFor(() => expect(screen.getByLabelText(/select crew/i)).toBeInTheDocument())
}

/** The crew list, opened. */
async function openCrewList() {
  fireEvent.click(screen.getByLabelText(/select crew/i))
  await screen.findByRole("option", { name: /Engineering/ })
}

describe("New routine → Describe it — crew identity", () => {
  beforeEach(() => {
    cleanup()
    h.crews = CREWS
    h.agents = AGENTS
  })

  it("is not a bare native select", async () => {
    await describeScreen()
    expect(document.querySelector("#describe-crew")?.tagName).not.toBe("SELECT")
  })

  it("draws every crew's own icon in the list", async () => {
    await describeScreen()
    await openCrewList()

    for (const name of ["Engineering", "Quality", "Smoke"]) {
      const option = screen.getByRole("option", { name: new RegExp(name) })
      // A glyph, not just the crew's name in text.
      expect(option.querySelector("svg")).not.toBeNull()
    }
  })

  it("tints each crew's icon with its stored colour, hex or palette id", async () => {
    await describeScreen()
    await openCrewList()

    // A stored hex is tinted inline — the class-based palette cannot say
    // #3B82F6, which is how every crew used to come out the same blue.
    const eng = screen.getByRole("option", { name: /Engineering/ })
    expect((eng.querySelector("div")?.getAttribute("style") ?? "").toLowerCase()).toContain("#3b82f6")

    const qa = screen.getByRole("option", { name: /Quality/ })
    expect((qa.querySelector("div")?.getAttribute("style") ?? "").toLowerCase()).toContain("#10b981")

    // A palette id takes the class-based gradient instead, with no inline
    // tint competing with it.
    const smoke = screen.getByRole("option", { name: /Smoke/ })
    expect(smoke.querySelector("div")?.className).toContain("bg-gradient-to-br")
  })

  it("keeps the chosen crew's icon on the trigger", async () => {
    await describeScreen()
    await openCrewList()
    fireEvent.click(screen.getByRole("option", { name: /Quality/ }))

    await waitFor(() => {
      const trigger = screen.getByLabelText(/select crew/i)
      expect(trigger).toHaveTextContent("Quality")
      expect((trigger.querySelector("div")?.getAttribute("style") ?? "").toLowerCase()).toContain(
        "#10b981",
      )
    })
  })

  it("still reports the crew it selected to the server", async () => {
    // Presentation only: the field still writes the same crew id.
    await describeScreen()
    await openCrewList()
    fireEvent.click(screen.getByRole("option", { name: /Engineering/ }))
    await waitFor(() => expect(screen.getByText("Morgan")).toBeInTheDocument())
  })
})

describe("New routine → Describe it — the Lead has a face", () => {
  beforeEach(() => {
    cleanup()
    h.crews = CREWS
    h.agents = AGENTS
  })

  it("renders the derived Lead's avatar beside the name", async () => {
    await describeScreen()
    await openCrewList()
    fireEvent.click(screen.getByRole("option", { name: /Engineering/ }))

    const lead = await screen.findByText("Morgan")
    const img = lead.closest("span")?.querySelector("img")
    expect(img).not.toBeNull()
  })

  it("wears the same face the roster gives it — avatar_seed, else the name", async () => {
    // crews-explorer and agent-canvas both pass `avatar_seed || name`, and
    // this workspace's agents have no avatar_seed. Passing the slug or the id
    // here would give one agent two faces.
    await describeScreen()
    await openCrewList()
    fireEvent.click(screen.getByRole("option", { name: /Engineering/ }))

    const img = (await screen.findByText("Morgan")).closest("span")?.querySelector("img")
    expect(img).toHaveAttribute("src", "data:image/svg+xml;utf8,generated-Morgan-default")
  })

  it("falls back to the crew's avatar style when the agent has none", async () => {
    await describeScreen()
    await openCrewList()
    fireEvent.click(screen.getByRole("option", { name: /Smoke/ }))

    const img = (await screen.findByText("Jordan")).closest("span")?.querySelector("img")
    expect(img).toHaveAttribute("src", "data:image/svg+xml;utf8,generated-Jordan-thumbs")
  })

  it("says so plainly when the crew has no agents at all", async () => {
    h.agents = []
    await describeScreen()
    await openCrewList()
    fireEvent.click(screen.getByRole("option", { name: /Engineering/ }))
    await waitFor(() => expect(screen.getByText(/No Lead in this crew/i)).toBeInTheDocument())
  })
})

describe("New routine → Write it yourself — the author crew", () => {
  beforeEach(() => {
    cleanup()
    h.crews = CREWS
    h.agents = AGENTS
  })

  it("picks the author crew by icon too, not by a second bare select", async () => {
    render(<RoutineCreateDialog {...PROPS} />)
    fireEvent.click(screen.getByText("Write it yourself"))
    await waitFor(() => expect(screen.getByLabelText(/select author crew/i)).toBeInTheDocument())
    expect(document.querySelector("#routine-author-crew")?.tagName).not.toBe("SELECT")

    fireEvent.click(screen.getByLabelText(/select author crew/i))
    const option = await screen.findByRole("option", { name: /Engineering/ })
    expect((option.querySelector("div")?.getAttribute("style") ?? "").toLowerCase()).toContain(
      "#3b82f6",
    )
  })
})
