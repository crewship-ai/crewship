import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { CreateAgentDialog } from "../create-agent-dialog"

// Stub next/navigation — the dialog calls router.replace on success.
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn(), push: vi.fn() }),
}))

// Stub sonner toasts so we can assert without rendering them.
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

// Avoid loading the real DiceBear styles (large) — getAgentAvatarUrl is fine
// to call, it just returns a data URI; nothing to mock.

const CREWS = [
  { id: "c1", slug: "engineering", name: "Engineering" },
  { id: "c2", slug: "research", name: "Research" },
]

describe("CreateAgentDialog", () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  function renderDialog(
    overrides: Partial<Parameters<typeof CreateAgentDialog>[0]> = {},
  ) {
    const props = {
      workspaceId: "ws-1",
      open: true,
      onOpenChange: vi.fn(),
      defaultCrewSlug: "engineering",
      crews: CREWS,
      onCreated: vi.fn(),
      ...overrides,
    }
    const utils = render(<CreateAgentDialog {...props} />)
    return { ...utils, props }
  }

  it("renders header + footer with disabled Create when name is empty", () => {
    renderDialog()
    expect(screen.getByText("New agent")).toBeInTheDocument()
    const createBtn = screen.getByRole("button", { name: /create agent/i })
    expect(createBtn).toBeDisabled()
  })

  it("shows the empty-crews banner when crews list is empty", () => {
    renderDialog({ crews: [], defaultCrewSlug: null })
    expect(screen.getByText(/no crews yet/i)).toBeInTheDocument()
  })

  it("does NOT show empty-crews banner when crews are present", () => {
    renderDialog()
    expect(screen.queryByText(/no crews yet/i)).not.toBeInTheDocument()
  })

  it("shows validation hint when name is too short", () => {
    renderDialog()
    const nameInput = screen.getByPlaceholderText("Filip") as HTMLInputElement
    fireEvent.change(nameInput, { target: { value: "X" } })
    expect(screen.getByText(/at least 2 characters/i)).toBeInTheDocument()
  })

  it("auto-derives slug from name", () => {
    renderDialog()
    const nameInput = screen.getByPlaceholderText("Filip") as HTMLInputElement
    fireEvent.change(nameInput, { target: { value: "Filip Test" } })
    const slugInput = screen.getByPlaceholderText("filip") as HTMLInputElement
    expect(slugInput.value).toBe("filip-test")
  })

  it("preserves user-typed slug when manually edited", () => {
    renderDialog()
    const slugInput = screen.getByPlaceholderText("filip") as HTMLInputElement
    fireEvent.change(slugInput, { target: { value: "manual-slug" } })
    const nameInput = screen.getByPlaceholderText("Filip") as HTMLInputElement
    fireEvent.change(nameInput, { target: { value: "Different Name" } })
    expect(slugInput.value).toBe("manual-slug")
  })

  it("submits the right body shape and signals onCreated on success", async () => {
    const fetchSpy = vi.spyOn(global, "fetch").mockResolvedValueOnce(
      new Response(JSON.stringify({ name: "Filip", slug: "filip" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    )

    const { props } = renderDialog()

    // Fill the minimum required fields.
    const nameInput = screen.getByPlaceholderText("Filip") as HTMLInputElement
    fireEvent.change(nameInput, { target: { value: "Filip" } })

    // Submit.
    const createBtn = screen.getByRole("button", { name: /create agent/i })
    expect(createBtn).not.toBeDisabled()
    fireEvent.click(createBtn)

    await waitFor(() => {
      expect(fetchSpy).toHaveBeenCalledTimes(1)
    })

    const [url, init] = fetchSpy.mock.calls[0]
    expect(String(url)).toContain("/api/v1/agents")
    expect(String(url)).toContain("workspace_id=ws-1")
    expect(init?.method).toBe("POST")

    const body = JSON.parse(init?.body as string)
    // The drift-detector list — must exactly match agents_create.go's JSON
    // tags. Same set as in agent-draft.test.ts but verified at the
    // component layer through a real submit.
    expect(Object.keys(body).sort()).toEqual([
      "agent_role",
      "avatar_seed",
      "avatar_style",
      "cli_adapter",
      "crew_id",
      "description",
      "lead_mode",
      "llm_model",
      "llm_provider",
      "memory_enabled",
      "name",
      "role_title",
      "slug",
      "system_prompt",
      "timeout_seconds",
      "tool_profile",
    ])
    expect(body.name).toBe("Filip")
    expect(body.slug).toBe("filip")
    expect(body.crew_id).toBe("c1") // resolved from defaultCrewSlug "engineering"
    expect(body.agent_role).toBe("AGENT")
    expect(body.lead_mode).toBeNull() // not LEAD → null
    expect(props.onCreated).toHaveBeenCalledWith("filip")
    expect(props.onOpenChange).toHaveBeenCalledWith(false)
  })

  it("does NOT submit when validation fails (name too short)", () => {
    const fetchSpy = vi.spyOn(global, "fetch")
    renderDialog()
    const nameInput = screen.getByPlaceholderText("Filip") as HTMLInputElement
    fireEvent.change(nameInput, { target: { value: "X" } })
    const createBtn = screen.getByRole("button", { name: /create agent/i })
    expect(createBtn).toBeDisabled()
    fireEvent.click(createBtn)
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it("does NOT close on backend 4xx — keeps the form so the user can retry", async () => {
    vi.spyOn(global, "fetch").mockResolvedValueOnce(
      new Response("slug taken", { status: 409 }),
    )
    const { props } = renderDialog()
    const nameInput = screen.getByPlaceholderText("Filip") as HTMLInputElement
    fireEvent.change(nameInput, { target: { value: "Filip" } })
    fireEvent.click(screen.getByRole("button", { name: /create agent/i }))
    await waitFor(() => {
      expect(props.onCreated).not.toHaveBeenCalled()
      expect(props.onOpenChange).not.toHaveBeenCalled()
    })
  })
})
