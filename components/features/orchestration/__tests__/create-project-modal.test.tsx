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
    expect(screen.getByText("New project")).toBeInTheDocument()
    expect(screen.getByText("CRE")).toBeInTheDocument()
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

  it("shows Start and Target date pills", () => {
    render(<CreateProjectModal {...defaultProps} />)
    expect(screen.getByText("Start")).toBeInTheDocument()
    expect(screen.getByText("Target")).toBeInTheDocument()
  })

  // Milestones are created by POST /api/v1/projects/{projectId}/milestones,
  // which 404s unless the project already exists — so nothing in a *create*
  // modal can call it. The section here had an add button with no onClick at
  // all: a control that does nothing.
  it("does not offer a Milestones section it cannot back", () => {
    render(<CreateProjectModal {...defaultProps} />)
    expect(screen.queryByText("Milestones")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "+" })).not.toBeInTheDocument()
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
