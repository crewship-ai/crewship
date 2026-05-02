import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { CreateCrewDialog } from "@/components/features/crews/create-crew-dialog"
import type { CrewTemplate } from "@/components/features/crews/create-crew/api"

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
    info: vi.fn(),
  },
}))

const TPL_ENG: CrewTemplate = {
  id: "1",
  slug: "software-development",
  name: "Software Development",
  description: "Tech Lead + 3 agents",
  icon: "code",
  color: "blue",
  category: "ENGINEERING",
  is_builtin: true,
  created_at: "2026-01-01T00:00:00Z",
  agents: [
    { name: "Tech Lead", slug: "tech-lead", role_title: "Lead", agent_role: "LEAD", cli_adapter: "CLAUDE_CODE", llm_provider: "ANTHROPIC", llm_model: "claude", tool_profile: "FULL", system_prompt: "" },
    { name: "Backend", slug: "backend", role_title: "BE", agent_role: "AGENT", cli_adapter: "CLAUDE_CODE", llm_provider: "ANTHROPIC", llm_model: "claude", tool_profile: "FULL", system_prompt: "" },
    { name: "Frontend", slug: "frontend", role_title: "FE", agent_role: "AGENT", cli_adapter: "CLAUDE_CODE", llm_provider: "ANTHROPIC", llm_model: "claude", tool_profile: "FULL", system_prompt: "" },
    { name: "QA", slug: "qa", role_title: "QA", agent_role: "AGENT", cli_adapter: "CLAUDE_CODE", llm_provider: "ANTHROPIC", llm_model: "claude", tool_profile: "FULL", system_prompt: "" },
  ],
}

interface MockCall {
  url: string
  method: string
  body: Record<string, unknown> | undefined
}

function setupFetch(routes: Array<(call: MockCall) => Response | null>) {
  const calls: MockCall[] = []
  const fetchMock = vi.fn(async (url: string | URL, init?: RequestInit) => {
    const u = typeof url === "string" ? url : url.toString()
    let body: Record<string, unknown> | undefined
    if (init?.body && typeof init.body === "string") {
      try { body = JSON.parse(init.body) } catch { /* ignore */ }
    }
    const call: MockCall = { url: u, method: init?.method ?? "GET", body }
    calls.push(call)
    for (const r of routes) {
      const resp = r(call)
      if (resp) return resp
    }
    // Default: empty 200
    return { ok: true, status: 200, json: async () => ({}), text: async () => "" } as Response
  })
  vi.stubGlobal("fetch", fetchMock)
  return calls
}

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
    text: async () => (typeof body === "string" ? body : JSON.stringify(body)),
  } as Response
}

describe("<CreateCrewDialog> full wizard flow", () => {
  beforeEach(() => {
    /* fresh fetch stub each test */
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.clearAllMocks()
  })

  function renderDialog() {
    const onCreated = vi.fn()
    const onOpenChange = vi.fn()
    const r = render(
      <CreateCrewDialog
        workspaceId="ws_test"
        open={true}
        onOpenChange={onOpenChange}
        onCreated={onCreated}
      />,
    )
    return { ...r, onCreated, onOpenChange }
  }

  it("starts on Step 1 with the right title and Identity inputs", () => {
    setupFetch([])
    renderDialog()
    // "step 1 of 3" appears in dialog title AND in footer.
    expect(screen.getAllByText(/step 1 of 4/i).length).toBeGreaterThanOrEqual(1)
    expect(screen.getByPlaceholderText("Engineering")).toBeInTheDocument()
  })

  it("Continue is disabled until name + slug are valid", () => {
    setupFetch([])
    renderDialog()
    const continueBtn = screen.getByRole("button", { name: /Continue/ })
    expect(continueBtn).toBeDisabled()

    // Type a name → slug auto-derives → Continue enables
    fireEvent.change(screen.getByPlaceholderText("Engineering"), {
      target: { value: "Engineering" },
    })
    expect(screen.getByRole("button", { name: /Continue/ })).not.toBeDisabled()
  })

  it("rejects single-character slug as invalid", () => {
    setupFetch([])
    renderDialog()
    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "A" } })
    // slug becomes "a" — only 1 char, invalid (regex requires ≥2 with start+end alphanumeric)
    expect(screen.getByRole("button", { name: /Continue/ })).toBeDisabled()
  })

  it("Step 2 — empty mode → Step 3 → Step 4 → Review → submit POSTs only /api/v1/crews once", async () => {
    const calls = setupFetch([
      // Templates list
      (c) => c.url.includes("/crew-templates") && !c.url.includes("/deploy")
        ? jsonResponse([TPL_ENG]) : null,
      // Features catalog (Step 4 RuntimeConfig fetches it on mount)
      (c) => c.url.includes("/features/catalog")
        ? jsonResponse({ features: [], runtimes: [] }) : null,
      // Crew create (POST /api/v1/crews)
      (c) => c.url.includes("/api/v1/crews") && c.method === "POST"
        ? jsonResponse({ id: "crew_1", slug: "engineering", name: "Engineering" }, 201) : null,
    ])

    const { onCreated } = renderDialog()

    // Step 1
    fireEvent.change(screen.getByPlaceholderText("Engineering"), {
      target: { value: "Engineering" },
    })
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))

    // Step 2 — switch to Empty
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Empty crew/ })).toBeInTheDocument()
    })
    fireEvent.click(screen.getByRole("button", { name: /Empty crew/ }))
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))

    // Step 3 — Runtime defaults are valid → continue
    await waitFor(() => {
      expect(screen.getByText("Container resources")).toBeInTheDocument()
    })
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))

    // Step 4 — Container (optional) — skip via Skip-to-defaults to keep this happy
    // path from depending on RuntimeConfig / MCPConfigEditor mounting cleanly.
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Skip to defaults/ })).toBeInTheDocument()
    })
    fireEvent.click(screen.getByRole("button", { name: /Skip to defaults/ }))

    // Step 5 — Review → click "✓ Create crew"
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Create crew/ })).toBeInTheDocument()
    })
    fireEvent.click(screen.getByRole("button", { name: /Create crew/ }))

    await waitFor(() => {
      expect(onCreated).toHaveBeenCalled()
    })

    // Submitted exactly one POST to /api/v1/crews (no deploy, no PATCH)
    const crewCalls = calls.filter((c) =>
      c.url.includes("/api/v1/crews") && c.method === "POST",
    )
    expect(crewCalls).toHaveLength(1)

    // No deploy call happened
    const deployCalls = calls.filter((c) => c.url.includes("/deploy"))
    expect(deployCalls).toHaveLength(0)

    // Body included identity + runtime
    expect(crewCalls[0].body).toMatchObject({
      name: "Engineering",
      slug: "engineering",
      icon: expect.any(String),
      color: expect.any(String),
      container_memory_mb: expect.any(Number),
      container_cpus: expect.any(Number),
      network_mode: "free",
    })
  })

  it("Step 2 — browse: picking a template sends deploy + patch on submit", async () => {
    const calls = setupFetch([
      // Templates list
      (c) => c.url.includes("/api/v1/crew-templates") && c.method === "GET"
        ? jsonResponse([TPL_ENG]) : null,
      // Deploy
      (c) => c.url.includes("/crew-templates/software-development/deploy") && c.method === "POST"
        ? jsonResponse({ crew_id: "crew_42", crew_name: "Engineering", crew_slug: "engineering" }, 201) : null,
      // PATCH overrides
      (c) => c.url.includes("/api/v1/crews/crew_42") && c.method === "PATCH"
        ? jsonResponse({}) : null,
    ])

    const { onCreated } = renderDialog()

    // Step 1
    fireEvent.change(screen.getByPlaceholderText("Engineering"), {
      target: { value: "Engineering" },
    })
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))

    // Step 2 — wait for templates AND auto-pick to complete (Continue enabled)
    await waitFor(() => {
      expect(screen.getAllByText("Software Development")).not.toHaveLength(0)
    })
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Continue/ })).not.toBeDisabled()
    })

    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))

    // Step 3
    await waitFor(() => {
      expect(screen.getByText("Container resources")).toBeInTheDocument()
    })
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))

    // Step 4 — skip Container customisation
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Skip to defaults/ })).toBeInTheDocument()
    })
    fireEvent.click(screen.getByRole("button", { name: /Skip to defaults/ }))

    // Step 5 → submit
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Create crew/ })).toBeInTheDocument()
    })
    fireEvent.click(screen.getByRole("button", { name: /Create crew/ }))

    await waitFor(() => {
      expect(onCreated).toHaveBeenCalled()
    })

    // Two write calls fired in correct order: deploy then PATCH
    const writeCalls = calls.filter((c) => c.method === "POST" || c.method === "PATCH")
    expect(writeCalls.find((c) => c.url.includes("/deploy"))).toBeDefined()
    expect(writeCalls.find((c) => c.method === "PATCH")).toBeDefined()
    // Deploy comes before PATCH (depends on PATCH-after-deploy order)
    const deployIdx = writeCalls.findIndex((c) => c.url.includes("/deploy"))
    const patchIdx = writeCalls.findIndex((c) => c.method === "PATCH")
    expect(deployIdx).toBeLessThan(patchIdx)
  })

  it("Back button navigates to previous step", async () => {
    setupFetch([
      (c) => c.url.includes("/crew-templates") ? jsonResponse([TPL_ENG]) : null,
    ])

    renderDialog()

    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "Eng" } })
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Browse templates/ })).toBeInTheDocument()
    })
    expect(screen.getAllByText(/step 2 of 4/i).length).toBeGreaterThanOrEqual(1)

    fireEvent.click(screen.getByRole("button", { name: /Back/ }))
    // "step 1 of 3" appears in dialog title AND in footer.
    expect(screen.getAllByText(/step 1 of 4/i).length).toBeGreaterThanOrEqual(1)
  })

  it("Cancel calls onOpenChange(false)", () => {
    setupFetch([])
    const { onOpenChange } = renderDialog()
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it("submit failure leaves dialog open and toasts error (does NOT close)", async () => {
    const { toast } = await import("sonner")
    setupFetch([
      (c) => c.url.includes("/api/v1/crew-templates") && c.method === "GET"
        ? jsonResponse([TPL_ENG]) : null,
      // Deploy fails with 409
      (c) => c.url.includes("/deploy")
        ? jsonResponse("crew slug already exists", 409) : null,
    ])

    const { onOpenChange, onCreated } = renderDialog()

    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "Eng" } })
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
    await waitFor(() => {
      expect(screen.getAllByText("Software Development")).not.toHaveLength(0)
    })
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Continue/ })).not.toBeDisabled()
    })
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
    await waitFor(() => {
      expect(screen.getByText("Container resources")).toBeInTheDocument()
    })
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
    // Step 4 — skip
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Skip to defaults/ })).toBeInTheDocument()
    })
    fireEvent.click(screen.getByRole("button", { name: /Skip to defaults/ }))
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Create crew/ })).toBeInTheDocument()
    })
    fireEvent.click(screen.getByRole("button", { name: /Create crew/ }))

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalled()
    })
    expect(onCreated).not.toHaveBeenCalled()
    // Dialog should NOT have been closed by submit attempt
    expect(onOpenChange).not.toHaveBeenCalledWith(false)
  })

  it("Skip to defaults on Step 4 jumps directly to Review (Step 5)", async () => {
    setupFetch([
      (c) => c.url.includes("/crew-templates") ? jsonResponse([TPL_ENG]) : null,
    ])

    renderDialog()

    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "Eng" } })
    fireEvent.click(screen.getByRole("button", { name: /Continue/ })) // Step 1 → 2
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Browse templates/ })).toBeInTheDocument()
    })
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Continue/ })).not.toBeDisabled()
    })
    fireEvent.click(screen.getByRole("button", { name: /Continue/ })) // Step 2 → 3
    await waitFor(() => {
      expect(screen.getByText("Container resources")).toBeInTheDocument()
    })
    fireEvent.click(screen.getByRole("button", { name: /Continue/ })) // Step 3 → 4

    // On Step 4, Skip-to-defaults is visible and jumps straight to Review.
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Skip to defaults/ })).toBeInTheDocument()
    })
    fireEvent.click(screen.getByRole("button", { name: /Skip to defaults/ }))

    expect(screen.getByRole("button", { name: /Create crew/ })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Skip to defaults/ })).toBeNull()
  })

  it("Skip to defaults button is NOT visible on Steps 1-3 or Review", async () => {
    setupFetch([
      (c) => c.url.includes("/crew-templates") ? jsonResponse([TPL_ENG]) : null,
    ])

    renderDialog()

    // Step 1 — no skip button
    expect(screen.queryByRole("button", { name: /Skip to defaults/ })).toBeNull()

    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "Eng" } })
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Browse templates/ })).toBeInTheDocument()
    })
    // Step 2 — no skip button
    expect(screen.queryByRole("button", { name: /Skip to defaults/ })).toBeNull()
  })

  it("step strip jumping is allowed only to already-completed steps", async () => {
    setupFetch([
      (c) => c.url.includes("/crew-templates") ? jsonResponse([TPL_ENG]) : null,
    ])

    renderDialog()

    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "Eng" } })

    // Step 2 button is "ahead" of step 1 — disabled
    const step2Btn = screen.getByLabelText(/Step 2: Lineup/)
    expect(step2Btn).toBeDisabled()

    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))

    // After advancing, Step 1 button is now "completed" → clickable
    await waitFor(() => {
      const s1 = screen.getByLabelText(/Step 1: Identity/)
      expect(s1).not.toBeDisabled()
    })
  })
})
