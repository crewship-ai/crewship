import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
vi.mock("@/components/ui/wifi", () => ({ WifiIcon: () => null }))

import { SystemStatusPill } from "../status-pill"

const OK = { total: 7, running: 1, error: 0, idle: 6, queued: 0 }

function json(body: unknown) {
  return Promise.resolve({ ok: true, json: () => Promise.resolve(body) })
}

describe("SystemStatusPill", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
  })

  it("is an icon with no visible label while healthy", () => {
    render(<SystemStatusPill engineStatus="connected" wsStatus="connected" crews={OK} workspaceId="ws-1" />)
    const pill = screen.getByTestId("system-status-pill")
    expect(pill).toHaveAccessibleName("System online")
    // The only text is the screen-reader copy.
    expect(pill.querySelector(".sr-only")?.textContent).toBe("Online")
    expect(pill).toHaveAttribute("data-tone", "success")
  })

  it("shows one message in one tone for a broken agent", () => {
    render(<SystemStatusPill engineStatus="connected" wsStatus="connected" crews={{ ...OK, error: 1, idle: 5 }} workspaceId="ws-1" />)
    const pill = screen.getByTestId("system-status-pill")
    expect(pill).toHaveTextContent("1 agent error")
    expect(pill).not.toHaveTextContent("Online")
    expect(pill).toHaveAttribute("data-tone", "destructive")
  })

  it("names the broken agents in the popover, linking to each", async () => {
    apiFetch.mockImplementation(() => json([
      { id: "a1", slug: "researcher", name: "Researcher", status: "ERROR", crew: { name: "Alpha" } },
      { id: "a2", slug: "writer", name: "Writer", status: "IDLE", crew: { name: "Alpha" } },
    ]))
    render(<SystemStatusPill engineStatus="connected" wsStatus="connected" crews={{ ...OK, error: 1, idle: 5 }} workspaceId="ws-1" />)
    fireEvent.click(screen.getByTestId("system-status-pill"))

    const link = await screen.findByRole("link", { name: /researcher/i })
    expect(link).toHaveAttribute("href", "/crews?agent=researcher")
    expect(screen.queryByRole("link", { name: /writer/i })).not.toBeInTheDocument()
    expect(apiFetch).toHaveBeenCalledWith("/api/v1/agents?workspace_id=ws-1&limit=500")
  })

  it("does not fetch agents when nothing is broken", () => {
    render(<SystemStatusPill engineStatus="connected" wsStatus="connected" crews={OK} workspaceId="ws-1" />)
    fireEvent.click(screen.getByTestId("system-status-pill"))
    expect(screen.getByTestId("system-status-panel")).toHaveTextContent("7 total · 1 running · 6 idle")
    expect(apiFetch).not.toHaveBeenCalled()
  })

  it("does not recite last-known counts while offline", () => {
    render(<SystemStatusPill engineStatus="offline" wsStatus="disconnected" crews={{ ...OK, error: 3 }} workspaceId="ws-1" />)
    fireEvent.click(screen.getByTestId("system-status-pill"))
    const panel = screen.getByTestId("system-status-panel")
    expect(panel).not.toHaveTextContent(/total|agent error/)
    expect(apiFetch).not.toHaveBeenCalled()
  })
})
