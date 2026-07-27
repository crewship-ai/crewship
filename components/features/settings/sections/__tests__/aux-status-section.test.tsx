import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { AuxStatusSection } from "../aux-status-section"

// Characterisation tests for the settings-shell restyle (visual only —
// data fetching / behaviour must be byte-identical before and after).
// Same apiFetch-stub pattern as privacy-section.test.tsx: drive the
// component through its real fetch path rather than mocking its logic.
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-1" }) }))

function jsonResponse(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body }
}

const SLOTS = [
  { slot: "curator", provider: "anthropic", model: "claude-haiku-4-5", timeout_ms: 5000, source: "fallback" as const },
  { slot: "keeper", provider: "anthropic", model: "claude-sonnet-4-5", timeout_ms: 8000, source: "explicit" as const },
]

describe("AuxStatusSection", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
  })
  afterEach(() => cleanup())

  it("renders slot data returned from the server", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ slots: SLOTS }))
    render(<AuxStatusSection />)

    expect(await screen.findByText("curator")).toBeTruthy()
    expect(screen.getByText("keeper")).toBeTruthy()
    expect(screen.getByText("claude-haiku-4-5")).toBeTruthy()
    expect(screen.getByText("5000ms")).toBeTruthy()
    // explicit vs fallback source pills both render.
    expect(screen.getByText(/^explicit$/i)).toBeTruthy()
    expect(screen.getByText(/^fallback$/i)).toBeTruthy()

    // Requests the caller's workspace so the ADMIN+ role check on the
    // backend has something to resolve against.
    const [url] = apiFetch.mock.calls[0] as [string]
    expect(url).toContain("workspace_id=ws-1")
  })

  it("renders the empty state when no slots are configured", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ slots: [] }))
    render(<AuxStatusSection />)

    expect(await screen.findByText(/No auxiliary slots configured/i)).toBeTruthy()
  })

  it("renders an error state on a failed fetch and lets the user retry", async () => {
    apiFetch.mockResolvedValueOnce(jsonResponse({ error: "boom" }, 500))
    render(<AuxStatusSection />)

    expect(await screen.findByText(/Failed \(HTTP 500\)/i)).toBeTruthy()

    apiFetch.mockResolvedValueOnce(jsonResponse({ slots: SLOTS }))
    fireEvent.click(screen.getByRole("button", { name: /refresh/i }))

    await waitFor(() => expect(screen.getByText("curator")).toBeTruthy())
  })

  it("shows a friendly error instead of crashing on a malformed response shape", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ nope: true }))
    render(<AuxStatusSection />)

    expect(await screen.findByText(/Unexpected response shape/i)).toBeTruthy()
  })
})
