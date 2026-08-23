import { describe, it, expect, vi, beforeEach, afterAll } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { CreateIssueModal } from "../create-issue-modal"

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
