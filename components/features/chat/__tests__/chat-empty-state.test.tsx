import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { ChatEmptyState, skillPrompt } from "../chat-empty-state"

vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: ({ seed }: { seed: string }) => <span data-testid="avatar">{seed}</span>,
}))

const fetchMock = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => fetchMock(...args) }))

const riley = {
  id: "ag-1", name: "Riley", slug: "riley", status: "IDLE", role_title: "Platform Engineer",
  crew: { name: "Ops", slug: "ops" }, _count: { skills: 2, credentials: 1 },
}

function skillsResponse(rows: unknown) {
  return Promise.resolve({ ok: true, json: () => Promise.resolve(rows) })
}

afterEach(() => fetchMock.mockReset())

describe("ChatEmptyState", () => {
  it("shows what the agent can do, from its own skills, each with a way in", async () => {
    fetchMock.mockReturnValue(skillsResponse([
      { id: "as-1", enabled: true, skill: { slug: "grafana", display_name: "Grafana dashboards", description: "Build or update a dashboard" } },
      { id: "as-2", enabled: false, skill: { slug: "off", display_name: "Disabled one" } },
    ]))
    const onPick = vi.fn()
    render(<ChatEmptyState agent={riley} workspaceId="ws-1" onPick={onPick} />)
    expect(screen.getByText("What Riley can do")).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText("Grafana dashboards")).toBeInTheDocument())
    // A disabled skill is not something the agent can do.
    expect(screen.queryByText("Disabled one")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Try it" }))
    expect(onPick).toHaveBeenCalledWith(skillPrompt({ slug: "grafana", display_name: "Grafana dashboards" }))
    expect(fetchMock.mock.calls[0][0]).toContain("/api/v1/agents/ag-1/skills?workspace_id=ws-1")
  })

  it("says so in one line when the agent has no skills, and links to adding one", async () => {
    fetchMock.mockReturnValue(skillsResponse([]))
    render(<ChatEmptyState agent={riley} workspaceId="ws-1" onPick={() => {}} />)
    await waitFor(() => expect(screen.getByText(/has no skills yet/)).toBeInTheDocument())
    expect(screen.getByRole("link", { name: /Add a skill/ }).getAttribute("href")).toBe("/crews?agent=riley&tab=skills")
  })

  it("treats a routine step as a transcript, not a conversation to start", () => {
    render(<ChatEmptyState agent={riley} workspaceId="ws-1" onPick={() => {}} kind="routine" />)
    expect(screen.getByText("This step has not written anything yet.")).toBeInTheDocument()
    expect(screen.queryByText(/What Riley can do/)).not.toBeInTheDocument()
    expect(screen.getByRole("link", { name: /Message Riley/ }).getAttribute("href")).toBe("/chat/riley")
    // No skills request for a transcript.
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
