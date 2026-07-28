import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { CrewAuditSection } from "../crew-audit-section"

// Characterisation tests for the settings-shell restyle (visual only —
// data fetching / behaviour must be byte-identical before and after).
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

function jsonResponse(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body }
}

const LOGS = [
  {
    id: "log-1",
    action: "agent.created",
    entity_type: "Agent",
    entity_id: "agent-123",
    metadata: null,
    ip_address: "10.0.0.1",
    user_agent: "crewship-cli/1.0",
    user: { id: "u1", email: "pavel@example.com", full_name: "Pavel Srba" },
    created_at: "2026-07-20T10:00:00Z",
  },
]

describe("CrewAuditSection", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
  })
  afterEach(() => cleanup())

  it("renders audit log rows returned from the server", async () => {
    apiFetch.mockResolvedValue(
      jsonResponse({ data: LOGS, pagination: { page: 1, limit: 50, total: 1, total_pages: 1 } }),
    )
    render(<CrewAuditSection workspaceId="ws-1" />)

    expect(await screen.findByText("Pavel Srba")).toBeTruthy()
    expect(screen.getByText("agent.created")).toBeTruthy()

    const [url] = apiFetch.mock.calls[0] as [string]
    expect(url).toContain("workspace_id=ws-1")
  })

  it("renders the empty state when there is no activity", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ data: [], pagination: { page: 1, limit: 50, total: 0, total_pages: 1 } }))
    render(<CrewAuditSection workspaceId="ws-1" />)

    expect(await screen.findByText(/No activity yet/i)).toBeTruthy()
  })

  it("renders an error state on a failed fetch", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ error: "boom" }, 500))
    render(<CrewAuditSection workspaceId="ws-1" />)

    expect(await screen.findByText(/Failed to load audit logs \(500\)/i)).toBeTruthy()
    expect(await screen.findByRole("button", { name: /retry/i })).toBeTruthy()
  })

  it("surfaces a refresh failure after a prior successful load", async () => {
    apiFetch.mockResolvedValueOnce(
      jsonResponse({ data: LOGS, pagination: { page: 1, limit: 50, total: 1, total_pages: 1 } }),
    )
    render(<CrewAuditSection workspaceId="ws-1" />)
    await screen.findByText("Pavel Srba")

    apiFetch.mockResolvedValueOnce(jsonResponse({ error: "boom" }, 500))
    const refreshBtn = screen.getByRole("button", { name: /refresh audit log/i })
    fireEvent.click(refreshBtn)

    // A failed refresh keeps the rows you already had and says they're
    // stale. It used to replace them with a full-page error box while the
    // banner above promised the opposite, and printed the message twice.
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent(/Failed to load audit logs \(500\)/i))
    expect(screen.getAllByText(/Failed to load audit logs \(500\)/i)).toHaveLength(1)
    expect(screen.getByText("Pavel Srba")).toBeTruthy()
    // The Retry button belongs to the full error box, which should be gone.
    expect(screen.queryByRole("button", { name: /retry/i })).toBeNull()
  })
})
