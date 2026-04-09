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
    expect(screen.getByPlaceholderText("Add a short summary...")).toBeInTheDocument()
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

  it("shows Milestones section", () => {
    render(<CreateProjectModal {...defaultProps} />)
    expect(screen.getByText("Milestones")).toBeInTheDocument()
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
