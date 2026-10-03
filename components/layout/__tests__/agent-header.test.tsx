import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor } from "@testing-library/react"
import { useState } from "react"

// Regression for #2864: the phone Stop button swallowed a 502 from
// POST /api/v1/agents/{id}/stop, so an unconfirmed stop looked like success.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

const toastError = vi.fn()
vi.mock("sonner", () => ({ toast: { error: (...a: unknown[]) => toastError(...a), success: vi.fn() } }))

vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => true }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))

type Agent = { id: string; name: string; status: string; agent_role: string }

// Real state behind the mocked context so the badge reflects setAgent.
vi.mock("@/hooks/use-agent-detail", () => ({
  useAgentDetail: () => {
    const [agent, setAgent] = useState<Agent | null>({
      id: "agent-1", name: "Mia", status: "RUNNING", agent_role: "AGENT",
    })
    return { agent, loading: false, setAgent }
  },
}))

vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => null }))

import { AgentHeader } from "../agent-header"

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })
}

describe("AgentHeader Stop (phone)", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    toastError.mockReset()
  })
  afterEach(cleanup)

  it("shows an error and keeps RUNNING when the runtime did not confirm the stop", async () => {
    apiFetch.mockResolvedValue(json(502, { error: "runtime stop not confirmed" }))
    render(<AgentHeader agentId="agent-1" />)
    fireEvent.click(screen.getByRole("button", { name: /stop/i }))
    await waitFor(() => expect(toastError).toHaveBeenCalledTimes(1))
    expect(JSON.stringify(toastError.mock.calls[0])).toMatch(/didn't confirm the stop/)
    expect(screen.getByText("RUNNING")).toBeTruthy()
    expect(screen.queryByText("STOPPED")).toBeNull()
  })

  it("shows an error when the request never reaches the server", async () => {
    apiFetch.mockRejectedValue(new TypeError("Failed to fetch"))
    render(<AgentHeader agentId="agent-1" />)
    fireEvent.click(screen.getByRole("button", { name: /stop/i }))
    await waitFor(() => expect(toastError).toHaveBeenCalledTimes(1))
    expect(screen.getByText("RUNNING")).toBeTruthy()
  })

  it("shows STOPPED once the server confirms", async () => {
    apiFetch.mockResolvedValue(json(200, { id: "agent-1", status: "STOPPED" }))
    render(<AgentHeader agentId="agent-1" />)
    fireEvent.click(screen.getByRole("button", { name: /stop/i }))
    await waitFor(() => expect(screen.getByText("STOPPED")).toBeTruthy())
    expect(toastError).not.toHaveBeenCalled()
  })
})
