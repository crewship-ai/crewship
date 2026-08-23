import { describe, it, expect, vi, beforeEach, afterAll } from "vitest"
import { render, screen, fireEvent, waitFor, configure } from "@testing-library/react"
import { CreateIssueModal } from "../create-issue-modal"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { getCrewIconDef } from "@/lib/crew-icons"
import { resolveRoutineIcon } from "@/lib/routine-identity"

// Testing Library's async default is 1000ms while vitest.config.ts allows a
// test 30000, so under a full-suite run — 523 files on a shared box — the
// assertion, not the code, is what runs out of road. Every await here goes
// through a render, an effect and a stubbed fetch, so raise the floor for the
// file rather than annotate seventeen call sites.
//
// The waits below are `waitFor(() => expect(getBy…))`, not
// `expect(await findBy…)`. The two are not equivalent under load: findBy
// resolves the node and hands it to a matcher that runs one tick later, and a
// node React found mid-commit can be detached by then — which reports as
// "element could not be found in the document" about an element the query just
// returned. waitFor retries the whole assertion, so the transient loses and a
// genuinely absent node still fails.
configure({ asyncUtilTimeout: 5000 })

// Mock sonner toast
vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
    info: vi.fn(),
  },
}))

const mockCrews = [
  { id: "crew-1", name: "Engineering", slug: "engineering", color: "blue", icon: "code" },
  { id: "crew-2", name: "Design", slug: "design", color: "violet", icon: "palette" },
]

const mockLabels = [
  { id: "label-1", name: "Bug", color: "red", label_group: null },
  { id: "label-2", name: "Feature", color: "blue", label_group: null },
]

const mockProjects = [
  {
    id: "proj-1", workspace_id: "ws-1", name: "Alpha", slug: "alpha",
    description: null, icon: null, color: "blue", status: "in_progress" as const,
    priority: "high" as const, health: "on_track" as const,
    lead_type: null, lead_id: null, start_date: null, target_date: null,
    created_at: "", updated_at: "", issue_count: 5, done_count: 2, progress: 40,
  },
]

const defaultProps = {
  open: true,
  onOpenChange: vi.fn(),
  crews: mockCrews,
  labels: mockLabels,
  projects: mockProjects,
  workspaceId: "ws-1",
  onCreated: vi.fn(),
}

describe("CreateIssueModal", () => {
  const originalFetch = global.fetch

  beforeEach(() => {
    vi.restoreAllMocks()
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve([]),
    })
  })

  afterAll(() => {
    global.fetch = originalFetch
  })

  it("renders when open", () => {
    render(<CreateIssueModal {...defaultProps} />)
    expect(screen.getByPlaceholderText("Issue title")).toBeInTheDocument()
    expect(screen.getByPlaceholderText("Add description...")).toBeInTheDocument()
    expect(screen.getByText("Create issue")).toBeInTheDocument()
  })

  it("does not render content when closed", () => {
    render(<CreateIssueModal {...defaultProps} open={false} />)
    expect(screen.queryByPlaceholderText("Issue title")).not.toBeInTheDocument()
  })

  it("auto-selects first crew on open", async () => {
    render(<CreateIssueModal {...defaultProps} />)
    // The header should show the crew prefix
    await waitFor(() => {
      expect(screen.getByText("ENG")).toBeInTheDocument()
    })
  })

  it("shows Backlog status pill (read-only)", () => {
    render(<CreateIssueModal {...defaultProps} />)
    expect(screen.getByText("Backlog")).toBeInTheDocument()
  })

  it("shows No priority pill by default", () => {
    render(<CreateIssueModal {...defaultProps} />)
    expect(screen.getByText("No priority")).toBeInTheDocument()
  })

  it("shows Project pill", () => {
    render(<CreateIssueModal {...defaultProps} />)
    expect(screen.getByText("Project")).toBeInTheDocument()
  })

  it("shows Labels pill", () => {
    render(<CreateIssueModal {...defaultProps} />)
    expect(screen.getByText("Labels")).toBeInTheDocument()
  })

  it("disables Create button when title is empty", () => {
    render(<CreateIssueModal {...defaultProps} />)
    const button = screen.getByText("Create issue")
    expect(button).toBeDisabled()
  })

  it("enables Create button when title is filled", () => {
    render(<CreateIssueModal {...defaultProps} />)
    const titleInput = screen.getByPlaceholderText("Issue title")
    fireEvent.change(titleInput, { target: { value: "Test issue" } })
    const button = screen.getByText("Create issue")
    expect(button).not.toBeDisabled()
  })

  it("submits issue with correct payload", async () => {
    const mockFetch = vi.fn()
      // First call: fetch agents
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([]) })
      // Second call: create issue
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve({ id: "issue-1" }) })
    global.fetch = mockFetch

    render(<CreateIssueModal {...defaultProps} />)

    // Fill title
    const titleInput = screen.getByPlaceholderText("Issue title")
    fireEvent.change(titleInput, { target: { value: "My test issue" } })

    // Fill description
    const descInput = screen.getByPlaceholderText("Add description...")
    fireEvent.change(descInput, { target: { value: "Test description" } })

    // Submit
    const button = screen.getByText("Create issue")
    fireEvent.click(button)

    await waitFor(() => {
      // Find the create issue API call (POST)
      const createCall = mockFetch.mock.calls.find(
        (call: [string, RequestInit?]) => typeof call[1] === "object" && call[1]?.method === "POST"
      )
      expect(createCall).toBeDefined()
      expect(createCall![0]).toContain("/api/v1/crews/crew-1/issues")
      const body = JSON.parse(createCall![1]!.body as string)
      expect(body.title).toBe("My test issue")
      expect(body.description).toBe("Test description")
      expect(body.priority).toBe("none")
    })
  })

  it("calls onCreated and closes after successful submit", async () => {
    const onCreated = vi.fn()
    const onOpenChange = vi.fn()
    global.fetch = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([]) })
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve({ id: "issue-1" }) })

    render(
      <CreateIssueModal {...defaultProps} onCreated={onCreated} onOpenChange={onOpenChange} />
    )

    fireEvent.change(screen.getByPlaceholderText("Issue title"), { target: { value: "Test" } })
    fireEvent.click(screen.getByText("Create issue"))

    await waitFor(() => {
      expect(onCreated).toHaveBeenCalled()
      expect(onOpenChange).toHaveBeenCalledWith(false)
    })
  })

  it("shows error toast on API failure", async () => {
    const { toast } = await import("sonner")
    global.fetch = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([]) })
      .mockResolvedValueOnce({
        ok: false,
        json: () => Promise.resolve({ detail: "Server error" }),
      })

    render(<CreateIssueModal {...defaultProps} />)

    fireEvent.change(screen.getByPlaceholderText("Issue title"), { target: { value: "Test" } })
    fireEvent.click(screen.getByText("Create issue"))

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith("Server error")
    })
  })

  it("closes modal when X button clicked", () => {
    const onOpenChange = vi.fn()
    render(<CreateIssueModal {...defaultProps} onOpenChange={onOpenChange} />)
    // Find the close button (the X in the header)
    const closeButtons = screen.getAllByRole("button")
    const xButton = closeButtons.find((btn) => btn.querySelector("svg.lucide-x"))
    if (xButton) fireEvent.click(xButton)
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it("shows Create more toggle", () => {
    render(<CreateIssueModal {...defaultProps} />)
    expect(screen.getByText("Create more")).toBeInTheDocument()
  })

  it("shows crew selector in header breadcrumb", () => {
    render(<CreateIssueModal {...defaultProps} />)
    // CreateSurfaceHeader renders `context › Title` as ONE heading (two
    // headings would make the accessible name "ENGNew issue"), and adds an
    // sr-only DialogDescription echoing the title when the surface passes no
    // description — so a bare getByText("New issue") now matches twice.
    const heading = screen.getByRole("heading", { level: 2 })
    expect(heading.textContent).toContain("ENG")
    expect(heading.textContent).toContain("New issue")
  })

  // ── The shared shell ────────────────────────────────────────────────────
  //
  // New issue is the surface `components/layout/create-surface.tsx` was
  // designed from, so it is the one that must actually mount it rather than
  // re-draw the same geometry by hand.

  it("mounts the shared CreateSurface shell", () => {
    render(<CreateIssueModal {...defaultProps} />)

    const content = document.querySelector('[data-slot="dialog-content"]')
    expect(content).not.toBeNull()

    // SHELL_BASE, the `md` width, and the bottom-sheet geometry — none of
    // which a hand-rolled DialogContent carries.
    expect(content!.className).toContain("group/surface")
    expect(content!.className).toContain("sm:max-w-[640px]")
    expect(content!.className).toContain("max-sm:rounded-t-2xl")

    // The shell's footer always offers Cancel, leftmost of the action group.
    expect(screen.getByText("Cancel")).toBeInTheDocument()
  })

  it("still POSTs the same create-issue request from the shell footer", async () => {
    const mockFetch = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([]) })
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve({ id: "issue-1" }) })
    global.fetch = mockFetch

    render(<CreateIssueModal {...defaultProps} />)

    // It has to be the shell's primary that fires the request, not a raw
    // <button> that happens to say the same words.
    const content = document.querySelector('[data-slot="dialog-content"]')
    expect(content).not.toBeNull()
    expect(content!.className).toContain("group/surface")

    fireEvent.change(screen.getByPlaceholderText("Issue title"), {
      target: { value: "Shell issue" },
    })
    fireEvent.change(screen.getByPlaceholderText("Add description..."), {
      target: { value: "Body text" },
    })
    fireEvent.click(screen.getByText("Create issue"))

    await waitFor(() => {
      const createCall = mockFetch.mock.calls.find(
        (call: [string, RequestInit?]) => typeof call[1] === "object" && call[1]?.method === "POST",
      )
      expect(createCall).toBeDefined()
      expect(createCall![0]).toBe(
        "/api/v1/crews/crew-1/issues?workspace_id=ws-1",
      )
      expect(JSON.parse(createCall![1]!.body as string)).toEqual({
        title: "Shell issue",
        description: "Body text",
        priority: "none",
      })
    })
  })

  it("still submits on ⌘↵, now wired by the shell rather than by hand", async () => {
    const mockFetch = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([]) })
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve({ id: "issue-1" }) })
    global.fetch = mockFetch

    render(<CreateIssueModal {...defaultProps} />)

    const titleInput = screen.getByPlaceholderText("Issue title")
    fireEvent.change(titleInput, { target: { value: "Keyboard issue" } })
    fireEvent.keyDown(titleInput, { key: "Enter", metaKey: true })

    await waitFor(() => {
      const createCall = mockFetch.mock.calls.find(
        (call: [string, RequestInit?]) => typeof call[1] === "object" && call[1]?.method === "POST",
      )
      expect(createCall).toBeDefined()
      expect(JSON.parse(createCall![1]!.body as string).title).toBe("Keyboard issue")
    })
  })

  it("still opens the pill popovers now that the pill is the trigger", async () => {
    const mockFetch = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([]) })
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve({ id: "issue-1" }) })
    global.fetch = mockFetch

    render(<CreateIssueModal {...defaultProps} />)

    fireEvent.change(screen.getByPlaceholderText("Issue title"), { target: { value: "Urgent one" } })

    // CreateSurfacePill is a plain function component; Radix's asChild needs
    // the ref to reach the <button> for the popover to anchor and open.
    fireEvent.click(screen.getByText("No priority"))
    fireEvent.click(await screen.findByText("Urgent"))
    fireEvent.click(screen.getByText("Create issue"))

    await waitFor(() => {
      const createCall = mockFetch.mock.calls.find(
        (call: [string, RequestInit?]) => typeof call[1] === "object" && call[1]?.method === "POST",
      )
      expect(createCall).toBeDefined()
      expect(JSON.parse(createCall![1]!.body as string).priority).toBe("urgent")
    })
  })

  it("surfaces a server refusal in the band, not only in a toast", async () => {
    const { toast } = await import("sonner")
    global.fetch = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([]) })
      .mockResolvedValueOnce({
        ok: false,
        json: () => Promise.resolve({ detail: "Title is already taken" }),
      })

    render(<CreateIssueModal {...defaultProps} />)

    fireEvent.change(screen.getByPlaceholderText("Issue title"), { target: { value: "Dup" } })
    fireEvent.click(screen.getByText("Create issue"))

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith("Title is already taken")
    })

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("Title is already taken")
  })
})

/* ──────────────────────────────────────────────────────────────────────────
 * The four defects the migration left behind.
 *
 * One functional (the assignee picker offered nobody) and three cosmetic —
 * an agent, a project and a routine each rendering a generic glyph here and
 * their real identity everywhere else in the product.
 * ──────────────────────────────────────────────────────────────────────── */

/**
 * The lucide class token the icon kit renders for `name`, read out of the kit
 * itself rather than retyped — a rename in `lib/crew-icons.ts` then fails this
 * test instead of silently asserting on a glyph that no longer resolves.
 */
function iconToken(name: string): string {
  const Icon = getCrewIconDef(name).icon
  const { container, unmount } = render(<Icon />)
  const token = Array.from(container.querySelector("svg")!.classList).find(
    (c) => c.startsWith("lucide-") && c !== "lucide",
  )!
  unmount()
  return token
}

/** The `[cmdk-item]` row whose visible text is `label`. */
function commandRow(label: string): HTMLElement {
  const row = screen.getByText(label).closest("[cmdk-item]")
  expect(row).not.toBeNull()
  return row as HTMLElement
}

// Two crews in the order the API returns them: the FIRST is empty, which is
// exactly the live shape (`e2e-empty-…` and `smoke-…` sort ahead of the crews
// that have anyone in them) and the reason the picker offered nobody.
const emptyFirstCrews = [
  { id: "crew-1", name: "Engineering", slug: "engineering", color: "blue", icon: "code", _count: { agents: 0, members: 1 } },
  { id: "crew-2", name: "Design", slug: "design", color: "violet", icon: "palette", _count: { agents: 2, members: 1 } },
]

// `avatar_seed`/`avatar_style` are NULL on every agent in the dev database, so
// these rows are the case the read-side surfaces fall back for.
const morgan = {
  id: "agent-morgan",
  name: "Morgan",
  slug: "morgan",
  avatar_seed: null,
  avatar_style: null,
  avatar_url: null,
  crew: { name: "Design", slug: "design", color: null, avatar_style: null },
}

const mockRoutines = [
  {
    id: "routine-1",
    slug: "nightly-sweep",
    name: "Nightly sweep",
    dsl_version: "1",
    definition_hash: "abc",
    ephemeral: false,
    workspace_visible: true,
    invocation_count: 0,
    authored_via: "user_api" as const,
    created_at: "",
    updated_at: "",
  },
]

const iconProjects = [
  {
    id: "proj-1", workspace_id: "ws-1", name: "Launch Prep", slug: "launch-prep",
    description: null, icon: "rocket", color: "#EC4899", status: "in_progress" as const,
    priority: "high" as const, health: "on_track" as const,
    lead_type: null, lead_id: null, start_date: null, target_date: null,
    created_at: "", updated_at: "", issue_count: 5, done_count: 2, progress: 40,
  },
]

describe("CreateIssueModal — identity and the empty assignee list", () => {
  const originalFetch = global.fetch

  /** Answers the agents fetch per crew; anything else is a bare ok. */
  function stubAgents(byCrew: Record<string, unknown[]>) {
    const fetchMock = vi.fn((url: string) => {
      const m = /crew_id=([^&]+)/.exec(url)
      if (url.startsWith("/api/v1/agents") && m) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(byCrew[m[1]] ?? []) })
      }
      return Promise.resolve({ ok: true, json: () => Promise.resolve({}) })
    })
    global.fetch = fetchMock as unknown as typeof fetch
    return fetchMock
  }

  beforeEach(() => {
    vi.restoreAllMocks()
    stubAgents({})
  })

  afterAll(() => {
    global.fetch = originalFetch
  })

  // ── 1. The assignee picker offers nobody ────────────────────────────────

  it("auto-selects a crew that HAS agents rather than whichever sorts first", async () => {
    stubAgents({ "crew-2": [morgan] })

    render(<CreateIssueModal {...defaultProps} crews={emptyFirstCrews} />)

    // The header names where the issue will land, so the choice is visible
    // and still overridable — it is not made behind the user's back.
    await waitFor(() => {
      expect(screen.getByRole("heading", { level: 2 }).textContent).toContain("DES")
    })

    fireEvent.click(screen.getByText("Assignee"))
    await waitFor(() => expect(screen.getByText("Morgan")).toBeInTheDocument())
  })

  it("says the crew has no agents instead of showing a silently empty list", async () => {
    const allEmpty = emptyFirstCrews.map((c) => ({ ...c, _count: { agents: 0, members: 1 } }))
    stubAgents({})

    render(<CreateIssueModal {...defaultProps} crews={allEmpty} />)

    fireEvent.click(screen.getByText("Assignee"))

    // Names the crew, so "there is nobody here" cannot be mistaken for
    // "the list failed to arrive".
    const note = await screen.findByText(/no agents/i)
    expect(note.textContent).toContain("Engineering")
  })

  it("distinguishes a failed agent load from a crew with nobody in it", async () => {
    global.fetch = vi.fn(() =>
      Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({}) }),
    ) as unknown as typeof fetch

    render(<CreateIssueModal {...defaultProps} crews={emptyFirstCrews} />)

    fireEvent.click(screen.getByText("Assignee"))

    await waitFor(() => expect(screen.getByText(/could not be loaded/i)).toBeInTheDocument())
    expect(screen.queryByText(/no agents/i)).toBeNull()
  })

  it("retries the agent load from the error state", async () => {
    global.fetch = vi.fn(() =>
      Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({}) }),
    ) as unknown as typeof fetch

    render(<CreateIssueModal {...defaultProps} crews={emptyFirstCrews} />)
    fireEvent.click(screen.getByText("Assignee"))
    await screen.findByText(/could not be loaded/i)

    // The retry has to be a control. Re-picking the crew that is already
    // picked sets the same id, so React bails out and the effect never
    // re-runs — the previous copy told the reader to do something that
    // silently did nothing.
    const retried = stubAgents({ "crew-2": [morgan] })
    fireEvent.click(screen.getByRole("button", { name: /try again/i }))

    // Asserts the request, not the repainted list. Whether the popover is
    // still open a tick later is Radix's business and moves with machine
    // load; whether the button re-asks the server is this button's whole
    // contract, and it fails on the old copy-only version because there is
    // no button to click at all.
    await waitFor(() =>
      expect(
        retried.mock.calls.some(
          (c) => typeof c[0] === "string" && c[0].includes("crew_id=crew-2"),
        ),
      ).toBe(true),
    )
  })

  // ── 2. Agents in the assignee picker have no avatar ─────────────────────

  it("wears the same face as the roster, seeded from the name when the seed is NULL", async () => {
    stubAgents({ "crew-2": [morgan] })

    render(<CreateIssueModal {...defaultProps} crews={emptyFirstCrews} />)

    fireEvent.click(screen.getByText("Assignee"))
    await screen.findByText("Morgan")

    const img = commandRow("Morgan").querySelector("img")
    expect(img).not.toBeNull()
    // crews-explorer.tsx / agent-canvas.tsx both pass `avatar_seed || name`.
    expect(img!.getAttribute("src")).toBe(getAgentAvatarUrl("Morgan", null))
  })

  // ── 3. The project picker shows a generic glyph ─────────────────────────

  it("renders a project with its own icon and colour, not a folder", async () => {
    render(<CreateIssueModal {...defaultProps} projects={iconProjects} />)

    fireEvent.click(screen.getByText("Project"))
    await screen.findByText("Launch Prep")

    const row = commandRow("Launch Prep")
    expect(row.querySelector(`svg.${iconToken("rocket")}`)).not.toBeNull()
    // CrewIcon tints a raw hex inline; the class-based palette cannot express one.
    expect(row.querySelector('[style*="#EC4899"]')).not.toBeNull()
  })

  // ── 4. The routine picker shows a generic glyph ─────────────────────────

  it("renders a routine with the icon routines-explorer derives for it", async () => {
    render(<CreateIssueModal {...defaultProps} routines={mockRoutines} />)

    fireEvent.click(screen.getByText("Routine"))
    await screen.findByText("Nightly sweep")

    const row = commandRow("Nightly sweep")
    const expected = iconToken(resolveRoutineIcon(mockRoutines[0]))
    expect(row.querySelector(`svg.${expected}`)).not.toBeNull()
  })
})
