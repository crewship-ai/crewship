import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { CreateProjectModal } from "../create-project-modal"

// Mock sonner toast
vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
    info: vi.fn(),
  },
}))

// Mock TiptapEditor since it has complex dependencies
vi.mock("@/components/features/issues/tiptap-editor", () => ({
  TiptapEditor: ({ placeholder, onChange }: { placeholder?: string; onChange: (v: string) => void }) => (
    <textarea
      data-testid="tiptap-editor"
      placeholder={placeholder}
      onChange={(e) => onChange(e.target.value)}
    />
  ),
}))

const mockCrews = [
  { id: "crew-1", name: "Engineering", slug: "engineering", color: "blue", icon: "code" },
]

const mockLabels = [
  { id: "label-1", name: "Bug", color: "red", label_group: null },
]

const defaultProps = {
  open: true,
  onOpenChange: vi.fn(),
  crews: mockCrews,
  labels: mockLabels,
  workspaceId: "ws-1",
  onCreated: vi.fn(),
}

// The exact set of fields POST /api/v1/projects binds, taken from the request
// struct in internal/api/project_handler.go. readJSON() uses a plain
// json.Unmarshal with no DisallowUnknownFields, so anything outside this set is
// accepted with a 201 and then silently dropped — the user sees "Project
// created" and the value is gone. Any key we send that is not here is a lie.
const SERVER_BOUND_FIELDS = [
  "name",
  "description",
  "icon",
  "color",
  "status",
  "priority",
  "lead_type",
  "lead_id",
  "start_date",
  "target_date",
] as const

describe("CreateProjectModal", () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve([]),
    })
  })

  it("renders when open", () => {
    render(<CreateProjectModal {...defaultProps} />)
    expect(screen.getByPlaceholderText("Project name")).toBeInTheDocument()
    expect(screen.getByText("Create project")).toBeInTheDocument()
  })

  it("does not render content when closed", () => {
    render(<CreateProjectModal {...defaultProps} open={false} />)
    expect(screen.queryByPlaceholderText("Project name")).not.toBeInTheDocument()
  })

  it("shows header breadcrumb", () => {
    render(<CreateProjectModal {...defaultProps} />)
    // Context and title are one h2 — two headings side by side would compute an
    // accessible name with no gap in it. Assert the whole string, separator
    // included: asserting each word alone passes even when they concatenate.
    expect(screen.getByRole("heading", { level: 2 })).toHaveTextContent("Projects › New project")
    // Not "CRE". A project is workspace-scoped; the three-letter key is the
    // issue modal's crew-slug prefix and means nothing here.
    expect(screen.queryByText("CRE")).not.toBeInTheDocument()
  })

  // The shell, not a bespoke 720px dialog: one overlay, four widths, and the
  // bottom-sheet geometry on a phone — none of which this modal had.
  it("mounts the shared CreateSurface shell", () => {
    const { baseElement } = render(<CreateProjectModal {...defaultProps} />)
    const content = baseElement.querySelector('[data-slot="dialog-content"]')
    expect(content).not.toBeNull()
    const classes = content!.getAttribute("class") ?? ""
    expect(classes).toContain("group/surface") // SHELL_BASE
    expect(classes).toContain("sm:max-w-[640px]") // size="md"
    expect(classes).toContain("max-sm:rounded-t-2xl") // bottom sheet
    expect(classes).not.toContain("sm:max-w-[720px]") // the old bespoke width
  })

  it("keeps the primary action in the shell footer and posts the same body", async () => {
    const mockFetch = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([]) })
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve({ id: "proj-1" }) })
    global.fetch = mockFetch

    const { baseElement } = render(<CreateProjectModal {...defaultProps} />)
    fireEvent.change(screen.getByPlaceholderText("Project name"), { target: { value: "Alpha" } })

    const primary = screen.getByText("Create project")
    // The shell's Button, inside the shell's dialog — not a hand-rolled
    // `h-7 px-3 rounded-md bg-primary` eighth variant of the confirm button.
    expect(primary).toHaveAttribute("data-slot", "button")
    expect(baseElement.querySelector('[data-slot="dialog-content"]')!.contains(primary)).toBe(true)

    fireEvent.click(primary)

    await waitFor(() => {
      const createCall = mockFetch.mock.calls.find(
        (call: [string, RequestInit?]) => typeof call[1] === "object" && call[1]?.method === "POST"
      )
      expect(createCall).toBeDefined()
      expect(createCall![0]).toContain("/api/v1/projects?workspace_id=ws-1")
      // JSON.stringify drops the undefined optionals, so this is the whole body.
      expect(JSON.parse(createCall![1]!.body as string)).toEqual({
        name: "Alpha",
        icon: "rocket",
        color: "blue",
        status: "backlog",
        priority: "none",
      })
    })
  })

  // The pills moved into the shell's pill row and are now CreateSurfacePill,
  // which Radix anchors its popover to via `asChild`. If that ref did not land
  // on the button the popover would not open at all.
  // Status is a chip row in the body now, not a pill hiding a popover: every
  // option is on screen and picking one is a single click. The assertion that
  // matters is unchanged — the pick sticks.
  it("applies a status pick from the chip row", async () => {
    render(<CreateProjectModal {...defaultProps} />)
    const backlog = screen.getByRole("radio", { name: /Backlog/ })
    expect(backlog).toHaveAttribute("aria-checked", "true")

    fireEvent.click(screen.getByRole("radio", { name: /Planned/ }))
    await waitFor(() => {
      expect(screen.getByRole("radio", { name: /Planned/ })).toHaveAttribute("aria-checked", "true")
      expect(screen.getByRole("radio", { name: /Backlog/ })).toHaveAttribute("aria-checked", "false")
    })
  })

  // A toast has faded by the time you look up. CreateSurfaceRefusal parks the
  // refusal between the body and the footer, outside the scrollport.
  it("shows a server refusal in a band, not only a toast", async () => {
    const { toast } = await import("sonner")
    global.fetch = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([]) })
      .mockResolvedValueOnce({
        ok: false,
        json: () => Promise.resolve({ detail: "Name already taken" }),
      })

    render(<CreateProjectModal {...defaultProps} />)
    fireEvent.change(screen.getByPlaceholderText("Project name"), { target: { value: "Test" } })
    fireEvent.click(screen.getByText("Create project"))

    expect(await screen.findByRole("alert")).toHaveTextContent("Name already taken")
    expect(toast.error).toHaveBeenCalledWith("Name already taken")
  })

  it("shows Backlog status pill by default", () => {
    render(<CreateProjectModal {...defaultProps} />)
    expect(screen.getByText("Backlog")).toBeInTheDocument()
  })

  it("shows No priority pill by default", () => {
    render(<CreateProjectModal {...defaultProps} />)
    expect(screen.getByText("No priority")).toBeInTheDocument()
  })

  it("shows Lead pill", () => {
    render(<CreateProjectModal {...defaultProps} />)
    expect(screen.getByText("Lead")).toBeInTheDocument()
  })

  // Both dates are date inputs under Planning. As pills they were a popover
  // each, so the two fields most likely to be filled together needed four
  // clicks and never appeared side by side.
  it("shows Start and Target as date fields, not pills", () => {
    render(<CreateProjectModal {...defaultProps} />)
    const start = screen.getByLabelText(/Start date/i)
    const target = screen.getByLabelText(/Target date/i)
    expect(start).toHaveAttribute("type", "date")
    expect(target).toHaveAttribute("type", "date")
  })

  // Milestones are created by POST /api/v1/projects/{projectId}/milestones,
  // which 404s unless the project already exists — so nothing in a *create*
  // modal can call it. The section here had an add button with no onClick at
  // all: a control that does nothing.
  it("does not offer a Milestones control it cannot back", () => {
    render(<CreateProjectModal {...defaultProps} />)
    // The section exists and says why it is empty — what must not exist is a
    // control. The old one was an add button with no onClick at all.
    expect(screen.getByText("Milestones")).toBeInTheDocument()
    expect(screen.getByText(/Milestones cannot be created here/)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "+" })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /add milestone/i })).not.toBeInTheDocument()
  })

  // No `summary` column on projects and no `summary` field on the create
  // request struct — typing here produced a success toast and nothing else.
  it("does not offer a summary field the server drops", () => {
    render(<CreateProjectModal {...defaultProps} />)
    expect(screen.queryByPlaceholderText("Add a short summary...")).not.toBeInTheDocument()
    expect(screen.queryByLabelText("Project summary")).not.toBeInTheDocument()
  })

  // No project_labels table exists anywhere in the repo and the create request
  // struct has no labels field, so the picker's selections went nowhere.
  it("does not offer a Labels picker the server drops", () => {
    render(<CreateProjectModal {...defaultProps} />)
    expect(screen.queryByText("Labels")).not.toBeInTheDocument()
    expect(screen.queryByText("Bug")).not.toBeInTheDocument()
  })

  it("shows Cancel and Create project buttons", () => {
    render(<CreateProjectModal {...defaultProps} />)
    expect(screen.getByText("Cancel")).toBeInTheDocument()
    expect(screen.getByText("Create project")).toBeInTheDocument()
  })

  it("disables Create button when name is empty", () => {
    render(<CreateProjectModal {...defaultProps} />)
    const button = screen.getByText("Create project")
    expect(button).toBeDisabled()
  })

  it("enables Create button when name is filled", () => {
    render(<CreateProjectModal {...defaultProps} />)
    fireEvent.change(screen.getByPlaceholderText("Project name"), { target: { value: "My Project" } })
    const button = screen.getByText("Create project")
    expect(button).not.toBeDisabled()
  })

  it("submits project with correct payload", async () => {
    const mockFetch = vi.fn()
      // First call: fetch agents
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([]) })
      // Second call: create project
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve({ id: "proj-1" }) })
    global.fetch = mockFetch

    render(<CreateProjectModal {...defaultProps} />)

    fireEvent.change(screen.getByPlaceholderText("Project name"), { target: { value: "Alpha" } })

    fireEvent.click(screen.getByText("Create project"))

    await waitFor(() => {
      const createCall = mockFetch.mock.calls.find(
        (call: [string, RequestInit?]) => typeof call[1] === "object" && call[1]?.method === "POST"
      )
      expect(createCall).toBeDefined()
      expect(createCall![0]).toContain("/api/v1/projects")
      const body = JSON.parse(createCall![1]!.body as string)
      expect(body.name).toBe("Alpha")
      expect(body.status).toBe("backlog")
      expect(body.priority).toBe("none")
      expect(body.icon).toBe("rocket")
      expect(body.color).toBe("blue")
    })
  })

  it("submits no field the server would silently drop", async () => {
    const mockFetch = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([]) })
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve({ id: "proj-1" }) })
    global.fetch = mockFetch

    render(<CreateProjectModal {...defaultProps} />)

    fireEvent.change(screen.getByPlaceholderText("Project name"), { target: { value: "Alpha" } })

    // Fill in every optional control the modal still exposes. Anything the user
    // can type into or pick has to survive the round trip; a field only stays
    // out of the payload here because it is not offered at all.
    const summaryInput = screen.queryByPlaceholderText("Add a short summary...")
    if (summaryInput) {
      fireEvent.change(summaryInput, { target: { value: "A short summary" } })
    }
    const labelsPill = screen.queryByText("Labels")
    if (labelsPill) {
      fireEvent.click(labelsPill)
      fireEvent.click(await screen.findByText("Bug"))
    }

    fireEvent.click(screen.getByText("Create project"))

    await waitFor(() => {
      const createCall = mockFetch.mock.calls.find(
        (call: [string, RequestInit?]) => typeof call[1] === "object" && call[1]?.method === "POST"
      )
      expect(createCall).toBeDefined()
      const body = JSON.parse(createCall![1]!.body as string)
      const dropped = Object.keys(body).filter(
        (k) => !(SERVER_BOUND_FIELDS as readonly string[]).includes(k)
      )
      expect(dropped).toEqual([])
    })
  })

  it("calls onCreated and closes after successful submit", async () => {
    const onCreated = vi.fn()
    const onOpenChange = vi.fn()
    global.fetch = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([]) })
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve({ id: "proj-1" }) })

    render(
      <CreateProjectModal {...defaultProps} onCreated={onCreated} onOpenChange={onOpenChange} />
    )

    fireEvent.change(screen.getByPlaceholderText("Project name"), { target: { value: "Test" } })
    fireEvent.click(screen.getByText("Create project"))

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
        json: () => Promise.resolve({ detail: "Name already taken" }),
      })

    render(<CreateProjectModal {...defaultProps} />)

    fireEvent.change(screen.getByPlaceholderText("Project name"), { target: { value: "Test" } })
    fireEvent.click(screen.getByText("Create project"))

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith("Name already taken")
    })
  })

  it("closes when Cancel button clicked", () => {
    const onOpenChange = vi.fn()
    render(<CreateProjectModal {...defaultProps} onOpenChange={onOpenChange} />)
    fireEvent.click(screen.getByText("Cancel"))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it("shows description editor with placeholder", () => {
    render(<CreateProjectModal {...defaultProps} />)
    expect(screen.getByTestId("tiptap-editor")).toBeInTheDocument()
  })

  it("renders icon picker button (crew icon)", () => {
    render(<CreateProjectModal {...defaultProps} />)
    // The icon button shows the CrewIcon component
    const iconButtons = screen.getAllByRole("button")
    // First button in the body area is the icon picker
    expect(iconButtons.length).toBeGreaterThan(0)
  })
})

// ── The three things this surface used to do quietly ──────────────────────
describe("CreateProjectModal — what it no longer hides", () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    global.fetch = vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve([]) })
  })

  // The lead fetch was `if (!res.ok || cancelled) return` with a bare
  // `catch {}`, so a 500 rendered exactly like a workspace with no agents in
  // it. Same defect the assignee picker had, same three-state shape.
  it("says the lead list failed rather than showing it as empty", async () => {
    global.fetch = vi.fn(() =>
      Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({}) }),
    ) as unknown as typeof fetch

    render(<CreateProjectModal {...defaultProps} />)
    fireEvent.click(screen.getByText("Lead"))

    await waitFor(() =>
      expect(screen.getByText(/could not be loaded/i)).toBeInTheDocument(),
    )
    expect(screen.queryByText(/no agents/i)).toBeNull()
  })

  it("offers a way back from that failure", async () => {
    global.fetch = vi.fn(() =>
      Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({}) }),
    ) as unknown as typeof fetch

    render(<CreateProjectModal {...defaultProps} />)
    fireEvent.click(screen.getByText("Lead"))
    await waitFor(() => screen.getByText(/could not be loaded/i))

    const retried = vi.fn(() =>
      Promise.resolve({ ok: true, json: () => Promise.resolve([{ id: "a1", name: "Morgan" }]) }),
    )
    global.fetch = retried as unknown as typeof fetch
    fireEvent.click(screen.getByRole("button", { name: /try again/i }))

    // Asserts the re-request, not the repainted list: whether the popover has
    // repainted a tick later is Radix's business and moves with machine load.
    await waitFor(() => expect(retried).toHaveBeenCalled())
  })

  // The reason milestones are not offered here lived only in a code comment:
  // POST /api/v1/projects/{id}/milestones 404s until the project exists.
  it("explains the missing milestones instead of leaving a hole", () => {
    render(<CreateProjectModal {...defaultProps} />)
    expect(
      screen.getByText(/milestones cannot be created here/i),
    ).toBeInTheDocument()
  })

  // The icon picker was a hand-rolled Popover — a second floating layer over a
  // surface that already is one.
  it("picks an icon in the body, not in another overlay", async () => {
    render(<CreateProjectModal {...defaultProps} />)
    fireEvent.click(screen.getByRole("button", { name: /change project icon/i }))

    await waitFor(() =>
      expect(screen.getByPlaceholderText(/search icons/i)).toBeInTheDocument(),
    )
    // One dialog on screen: this surface. The picker is inside it.
    expect(screen.getAllByRole("dialog")).toHaveLength(1)
  })
})

// ── ⌘↵ inside the icon panel ─────────────────────────────────────────────
//
// While the panel is open the body IS the picker and the footer's primary
// reads "Use this icon". The shell routes ⌘↵ to the surface's onSubmit
// regardless, so passing handleSubmit unguarded meant the shortcut created
// the project from a form the user was not looking at. Both sibling wizards
// already guarded it; this one was the exception.
describe("<CreateProjectModal> — the icon panel and ⌘↵", () => {
  it("closes the panel instead of creating the project", async () => {
    const fetchSpy = vi.spyOn(global, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ id: "p1", slug: "x" }), { status: 201 }),
    )
    render(<CreateProjectModal {...defaultProps} />)

    // A name, so the create would otherwise be valid and actually fire.
    fireEvent.change(screen.getByPlaceholderText("Project name"), { target: { value: "Alpha" } })
    fireEvent.click(screen.getByRole("button", { name: /Change project icon/i }))
    await screen.findByPlaceholderText(/Search icons/i)

    fireEvent.keyDown(document.querySelector('[data-slot="dialog-content"]')!, {
      key: "Enter",
      metaKey: true,
    })

    // The lead picker GETs /agents on mount, so assert on the create itself.
    const created = fetchSpy.mock.calls.find(
      ([url, init]) =>
        String(url).includes("/api/v1/projects") && (init as RequestInit | undefined)?.method === "POST",
    )
    expect(created).toBeUndefined()
    // …and the shortcut did what it should: back to the form.
    expect(screen.getByPlaceholderText("Project name")).toBeInTheDocument()
    vi.restoreAllMocks()
  })
})

