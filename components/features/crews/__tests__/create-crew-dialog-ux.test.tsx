// =============================================================================
// UX / a11y / keyboard tests for the Create-crew wizard.
//
// Lives next to create-crew-dialog.test.tsx (which holds the happy-path
// state-machine tests) so failures are easy to triage by file: if THIS file
// fails, the keyboard / accessibility / progressive disclosure has regressed.
// =============================================================================

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react"
import { CreateCrewDialog } from "@/components/features/crews/create-crew-dialog"
import type { CrewTemplate } from "@/components/features/crews/create-crew/api"

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
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
  ],
}

interface MockCall { url: string; method: string; body: Record<string, unknown> | undefined }

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

beforeEach(() => { /* fresh stub per test */ })
afterEach(() => { vi.unstubAllGlobals(); vi.clearAllMocks() })

// =============================================================================
// Keyboard shortcuts
// =============================================================================

describe("<CreateCrewDialog> — keyboard", () => {
  it("⌘+Enter advances when the current step is valid", async () => {
    setupFetch([(c) => c.url.includes("/crew-templates") ? jsonResponse([TPL_ENG]) : null])
    renderDialog()

    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "Eng" } })

    // Press Cmd+Enter
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", metaKey: true }))
    })

    await waitFor(() => {
      expect(screen.getAllByText(/step 2 of 4/i).length).toBeGreaterThanOrEqual(1)
    })
  })

  it("Ctrl+Enter advances on non-Mac (same shortcut)", async () => {
    setupFetch([(c) => c.url.includes("/crew-templates") ? jsonResponse([TPL_ENG]) : null])
    renderDialog()

    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "Eng" } })

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true }))
    })

    await waitFor(() => {
      expect(screen.getAllByText(/step 2 of 4/i).length).toBeGreaterThanOrEqual(1)
    })
  })

  it("⌘+Enter does NOT advance when the step is invalid", () => {
    setupFetch([])
    renderDialog()
    // Step 1 is invalid (empty name) — Cmd+Enter should be a no-op.
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", metaKey: true }))
    })
    expect(screen.getAllByText(/step 1 of 4/i).length).toBeGreaterThanOrEqual(1)
  })

  it("⌘+Enter on Step 5 triggers submit", async () => {
    const { toast } = await import("sonner")
    const calls = setupFetch([
      (c) => c.url.includes("/crew-templates") && c.method === "GET" ? jsonResponse([TPL_ENG]) : null,
      (c) => c.url.includes("/api/v1/crews") && c.method === "POST"
        ? jsonResponse({ id: "x", slug: "x", name: "X" }, 201) : null,
    ])

    renderDialog()
    // Walk to Step 5
    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "Eng" } })
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
    await waitFor(() => screen.getByRole("button", { name: /Empty crew/ }))
    fireEvent.click(screen.getByRole("button", { name: /Empty crew/ }))
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
    await waitFor(() => screen.getByText("Container resources"))
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
    await waitFor(() => screen.getByRole("button", { name: /Skip to defaults/ }))
    fireEvent.click(screen.getByRole("button", { name: /Skip to defaults/ }))
    await waitFor(() => screen.getByRole("button", { name: /Create crew/ }))

    // ⌘+Enter on Review submits
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", metaKey: true }))
    })

    await waitFor(() => {
      expect(toast.success).toHaveBeenCalled()
    })
    expect(calls.find((c) => c.url.includes("/api/v1/crews") && c.method === "POST")).toBeDefined()
  })

  it("plain Enter does NOT advance — only Cmd/Ctrl+Enter", () => {
    setupFetch([])
    renderDialog()
    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "Eng" } })

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter" }))
    })

    expect(screen.getAllByText(/step 1 of 4/i).length).toBeGreaterThanOrEqual(1)
  })
})

// =============================================================================
// Step strip — accessibility + jump-back navigation
// =============================================================================

describe("<CreateCrewDialog> — step strip", () => {
  it("step strip is announced as a navigation landmark", () => {
    setupFetch([])
    renderDialog()
    expect(screen.getByRole("navigation", { name: /Wizard progress/i })).toBeInTheDocument()
  })

  it("active step button has aria-current='step'", () => {
    setupFetch([])
    renderDialog()
    const step1 = screen.getByLabelText(/Step 1: Identity/)
    expect(step1).toHaveAttribute("aria-current", "step")

    const step2 = screen.getByLabelText(/Step 2: Lineup/)
    expect(step2).not.toHaveAttribute("aria-current")
  })

  it("clicking a completed step jumps back to it", async () => {
    setupFetch([(c) => c.url.includes("/crew-templates") ? jsonResponse([TPL_ENG]) : null])
    renderDialog()

    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "Eng" } })
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))

    await waitFor(() => {
      expect(screen.getAllByText(/step 2 of 4/i).length).toBeGreaterThanOrEqual(1)
    })

    // Now Step 1 is completed → click jumps back
    fireEvent.click(screen.getByLabelText(/Step 1: Identity/))
    expect(screen.getAllByText(/step 1 of 4/i).length).toBeGreaterThanOrEqual(1)
  })

  it("clicking the active or future step is a no-op", async () => {
    setupFetch([(c) => c.url.includes("/crew-templates") ? jsonResponse([TPL_ENG]) : null])
    renderDialog()

    // Future step (Step 3) is disabled
    const step3 = screen.getByLabelText(/Step 3: Runtime/)
    expect(step3).toBeDisabled()

    // Active step (Step 1) is also disabled (cursor-default, no jump)
    const step1 = screen.getByLabelText(/Step 1: Identity/)
    expect(step1).toBeDisabled()
  })
})

// =============================================================================
// Submit loading state
// =============================================================================

describe("<CreateCrewDialog> — loading state during submit", () => {
  it("Create button shows 'Creating…' and a spinner while submit is in flight", async () => {
    let resolveCreate: ((res: Response) => void) | null = null
    const pending = new Promise<Response>((resolve) => { resolveCreate = resolve })
    setupFetch([
      (c) => c.url.includes("/crew-templates") ? jsonResponse([TPL_ENG]) : null,
      (c) => c.url.includes("/api/v1/crews") && c.method === "POST" ? (pending as unknown as Response) : null,
    ])

    renderDialog()

    // Walk to Review fast
    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "Eng" } })
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
    await waitFor(() => screen.getByRole("button", { name: /Empty crew/ }))
    fireEvent.click(screen.getByRole("button", { name: /Empty crew/ }))
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
    await waitFor(() => screen.getByText("Container resources"))
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
    await waitFor(() => screen.getByRole("button", { name: /Skip to defaults/ }))
    fireEvent.click(screen.getByRole("button", { name: /Skip to defaults/ }))
    await waitFor(() => screen.getByRole("button", { name: /Create crew/ }))

    fireEvent.click(screen.getByRole("button", { name: /Create crew/ }))

    // While the POST is pending, button text changes
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Creating…/ })).toBeInTheDocument()
    })

    // Cancel button is disabled during submit (prevents accidental close mid-create)
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled()

    // Unblock the pending request so the test cleans up
    resolveCreate?.(jsonResponse({ id: "x", slug: "x", name: "X" }, 201))
  })
})

// =============================================================================
// Re-open resets state
// =============================================================================

describe("<CreateCrewDialog> — open/close lifecycle", () => {
  it("closing and re-opening resets to a fresh Step 1 with empty form", async () => {
    setupFetch([(c) => c.url.includes("/crew-templates") ? jsonResponse([TPL_ENG]) : null])

    const onOpenChange = vi.fn()
    const { rerender } = render(
      <CreateCrewDialog workspaceId="ws_test" open={true} onOpenChange={onOpenChange} onCreated={vi.fn()} />,
    )

    // Type something + advance to Step 2
    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "Half-typed" } })
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))

    // Close the dialog
    rerender(
      <CreateCrewDialog workspaceId="ws_test" open={false} onOpenChange={onOpenChange} onCreated={vi.fn()} />,
    )

    // Re-open
    rerender(
      <CreateCrewDialog workspaceId="ws_test" open={true} onOpenChange={onOpenChange} onCreated={vi.fn()} />,
    )

    // Should be back on Step 1 with empty Name
    await waitFor(() => {
      expect(screen.getAllByText(/step 1 of 4/i).length).toBeGreaterThanOrEqual(1)
    })
    expect((screen.getByPlaceholderText("Engineering") as HTMLInputElement).value).toBe("")
  })
})
