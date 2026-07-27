import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { ConnectionsSection } from "../connections-section"

// Both crew-connections mutations (POST create, DELETE disconnect) are
// `roleCreate` server-side (MANAGER+, internal/api/router_crews.go). This
// section has no role prop, so it reads useAbilities().role — steer it per
// test the same way crews-containers-section.test.tsx does, rather than
// standing up workspace/session plumbing.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

let role = "MANAGER"
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({
    abilities: { can: () => true },
    role,
    capabilities: null,
    hasCapability: () => false,
    loading: false,
  }),
}))

function jsonResponse(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body }
}

// Radix Select opens on pointerdown (not click), and jsdom needs the
// explicit down/up pair before the portal content mounts.
function openSelect(trigger: HTMLElement) {
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false, pointerId: 1 })
  fireEvent.pointerUp(trigger, { button: 0, pointerId: 1 })
  fireEvent.click(trigger)
}

const CREWS = [
  { id: "c1", name: "Alpha Crew", slug: "alpha", color: "blue" },
  { id: "c2", name: "Bravo Crew", slug: "bravo", color: "green" },
]

const CONNECTIONS = [
  {
    id: "conn1",
    from_crew_id: "c1",
    from_crew_name: "Alpha Crew",
    from_crew_slug: "alpha",
    to_crew_id: "c2",
    to_crew_name: "Bravo Crew",
    to_crew_slug: "bravo",
    direction: "bidirectional",
    status: "active",
    created_at: new Date().toISOString(),
  },
]

function renderSection() {
  return render(<ConnectionsSection workspaceId="ws1" />)
}

describe("ConnectionsSection — role-tiered controls", () => {
  beforeEach(() => {
    cleanup()
    role = "MANAGER"
    apiFetch.mockReset()
    apiFetch.mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/crew-connections")) return jsonResponse(CONNECTIONS)
      if (url.startsWith("/api/v1/crews")) return jsonResponse(CREWS)
      return jsonResponse(null, 404)
    })
  })

  it("lets a MANAGER create a connection", async () => {
    apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "POST") return jsonResponse({ id: "conn2" })
      if (url.startsWith("/api/v1/crew-connections")) return jsonResponse(CONNECTIONS)
      if (url.startsWith("/api/v1/crews")) return jsonResponse(CREWS)
      return jsonResponse(null, 404)
    })
    renderSection()

    await screen.findByText("Active connections")
    // From/To selects + Connect button are all present for a MANAGER.
    const selects = screen.getAllByRole("combobox")
    expect(selects.length).toBeGreaterThanOrEqual(2)

    openSelect(selects[0])
    fireEvent.click(await screen.findByRole("option", { name: "Alpha Crew" }))
    openSelect(screen.getAllByRole("combobox")[1])
    fireEvent.click(await screen.findByRole("option", { name: "Bravo Crew" }))

    // Exact match: "Disconnect …" also contains "connect" and would
    // otherwise match too (it's the existing connection's row action).
    const connectButton = screen.getByRole("button", { name: /^connect$/i })
    expect(connectButton).toBeEnabled()
    fireEvent.click(connectButton)

    await waitFor(() => {
      expect(apiFetch).toHaveBeenCalledWith(
        "/api/v1/crew-connections?workspace_id=ws1",
        expect.objectContaining({ method: "POST" }),
      )
    })
  })

  it("shows a MEMBER the connection list but no create or disconnect controls", async () => {
    role = "MEMBER"
    renderSection()

    // The connection graph — the read surface — stays visible.
    expect(await screen.findByText("Alpha Crew")).toBeInTheDocument()
    expect(screen.getByText("Bravo Crew")).toBeInTheDocument()

    // No create form at all: it has no read-only content of its own, so
    // there is nothing worth leaving on screen below MANAGER.
    expect(screen.queryByText("Create connection")).toBeNull()
    expect(screen.queryByRole("button", { name: /connect/i })).toBeNull()
    expect(screen.queryByRole("button", { name: /disconnect/i })).toBeNull()

    // Explains the gap, once, quietly — no alert styling.
    expect(
      screen.getByText(/only managers and admins can create or remove crew connections/i),
    ).toBeInTheDocument()
  })

  it("never sends a mutating request for a MEMBER caller — there is no control that could", async () => {
    role = "MEMBER"
    renderSection()
    await screen.findByText("Alpha Crew")

    expect(apiFetch.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method)).toHaveLength(0)
  })
})
