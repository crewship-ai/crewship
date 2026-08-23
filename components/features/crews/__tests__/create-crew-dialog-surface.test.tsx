// =============================================================================
// The Create-crew wizard is mounted on the shared CreateSurface shell.
//
// Two things this file guards that the other two create-crew-dialog test files
// do not:
//
//   1. The wizard renders THE SHELL — one Radix DialogContent carrying
//      create-surface.tsx's geometry classes — rather than its own modal.
//   2. The width is FIXED for the surface's whole life. It used to grow
//      680 → 940px between Step 1 and Step 2, which walks the footer out from
//      under the cursor mid-flow.
//
// The submit assertion is here too, because "it looks like the shell" is only
// half the migration: the primary action must still issue exactly the request
// it issued before.
// =============================================================================

import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { CreateCrewDialog } from "@/components/features/crews/create-crew-dialog"
import type { CrewTemplate } from "@/components/features/crews/create-crew/api"

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() },
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
    calls.push({ url: u, method: init?.method ?? "GET", body })
    for (const r of routes) {
      const resp = r(calls[calls.length - 1])
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
    <CreateCrewDialog workspaceId="ws_test" open onOpenChange={onOpenChange} onCreated={onCreated} />,
  )
  return { ...r, onCreated, onOpenChange }
}

function surface(): HTMLElement {
  const el = document.querySelector<HTMLElement>('[data-slot="dialog-content"]')
  if (!el) throw new Error("no dialog content rendered")
  return el
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

describe("<CreateCrewDialog> — CreateSurface shell", () => {
  it("mounts the shared shell, not a bespoke modal", () => {
    setupFetch([])
    renderDialog()

    const el = surface()
    // Geometry that only create-surface.tsx applies.
    expect(el.className).toContain("group/surface")
    expect(el.className).toContain("max-sm:rounded-t-2xl")
    // The shell's own scrollport + footer, not the wizard's.
    expect(el.className).toContain("overflow-hidden")
  })

  it("is size lg and stays that width from Step 1 into Step 2", async () => {
    setupFetch([
      (c) => c.url.includes("/crew-templates") ? jsonResponse([TPL_ENG]) : null,
    ])
    renderDialog()

    expect(surface().className).toContain("sm:max-w-[800px]")

    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "Engineering" } })
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Browse templates/ })).toBeInTheDocument()
    })

    // Step 2 used to jump to 940px.
    expect(surface().className).toContain("sm:max-w-[800px]")
    expect(surface().className).not.toContain("sm:max-w-[940px]")
    expect(surface().className).not.toContain("sm:max-w-[680px]")
  })

  it("the footer primary still POSTs /api/v1/crews with the same body", async () => {
    const calls = setupFetch([
      (c) => c.url.includes("/crew-templates") && !c.url.includes("/deploy")
        ? jsonResponse([TPL_ENG]) : null,
      (c) => c.url.includes("/features/catalog") ? jsonResponse({ features: [], runtimes: [] }) : null,
      (c) => c.url.includes("/api/v1/crews") && c.method === "POST"
        ? jsonResponse({ id: "crew_1", slug: "engineering", name: "Engineering" }, 201) : null,
    ])

    const { onCreated } = renderDialog()

    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "Engineering" } })
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
    await waitFor(() => expect(onCreated).toHaveBeenCalled())

    const posts = calls.filter((c) => c.url.includes("/api/v1/crews") && c.method === "POST")
    expect(posts).toHaveLength(1)
    expect(posts[0].url).toContain("workspace_id=ws_test")
    expect(posts[0].body).toMatchObject({
      name: "Engineering",
      slug: "engineering",
      icon: "code",
      color: "blue",
      container_memory_mb: 4096,
      container_cpus: 2,
      container_ttl_hours: 0,
      network_mode: "restricted",
    })
  })

  it("a server refusal is shown in the surface's refusal band, not only as a toast", async () => {
    const { toast } = await import("sonner")
    setupFetch([
      (c) => c.url.includes("/crew-templates") && !c.url.includes("/deploy")
        ? jsonResponse([TPL_ENG]) : null,
      (c) => c.url.includes("/api/v1/crews") && c.method === "POST"
        ? jsonResponse("crew slug already exists", 409) : null,
    ])

    renderDialog()

    fireEvent.change(screen.getByPlaceholderText("Engineering"), { target: { value: "Engineering" } })
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

    await waitFor(() => {
      const alert = screen.getByRole("alert")
      expect(alert.textContent).toMatch(/crew slug already exists/i)
    })
    // The toast the wizard already had is kept.
    expect(toast.error).toHaveBeenCalled()
  })
})
