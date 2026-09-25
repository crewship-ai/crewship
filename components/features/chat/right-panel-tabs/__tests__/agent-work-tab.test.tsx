import { beforeEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, within } from "@testing-library/react"
import { AgentWorkTab } from "../agent-work-tab"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

const issues = [
  { id: "backlog", identifier: "COPY-3", title: "Plan launch", status: "BACKLOG", updated_at: "2026-09-20T00:00:00Z" },
  { id: "working", identifier: "COPY-1", title: "Review headline", status: "IN_PROGRESS", updated_at: "2026-09-24T00:00:00Z" },
  { id: "review", identifier: "COPY-2", title: "Approve summary", status: "REVIEW", updated_at: "2026-09-23T00:00:00Z" },
]
const routines = [
  { id: "mine", slug: "copy-review", name: "Copy review", author_agent_id: "agent-1", invocation_count: 4, step_count: 3, last_invocation_status: "completed", last_invoked_at: "2026-09-22T00:00:00Z" },
  { id: "other", slug: "other", name: "Other agent routine", author_agent_id: "agent-2", invocation_count: 2 },
]

beforeEach(() => {
  apiFetch.mockReset()
  apiFetch.mockImplementation(async (url: string) => ({ ok: true, json: async () => url.includes("/issues?") ? issues : routines }))
})

describe("AgentWorkTab", () => {
  it("groups assigned issues by status and switches to only this agent's routine metadata", async () => {
    render(<AgentWorkTab agentId="agent-1" workspaceId="ws-1" />)
    const working = await screen.findByRole("region", { name: "In Progress issues" })
    const review = screen.getByRole("region", { name: "In Review issues" })
    const backlog = screen.getByRole("region", { name: "Backlog issues" })
    expect(backlog.compareDocumentPosition(working) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(working.compareDocumentPosition(review) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(within(working).getByRole("link", { name: /COPY-1/ })).toHaveAttribute("href", "/issues/COPY-1")
    expect(screen.queryByRole("link", { name: /Copy review/ })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Refresh agent work/ })).not.toBeInTheDocument()
    expect(screen.queryByRole("link", { name: /View all/ })).not.toBeInTheDocument()

    fireEvent.click(within(backlog).getByRole("button", { name: /Backlog/ }))
    expect(within(backlog).getByRole("button", { name: /Backlog/ })).toHaveAttribute("aria-expanded", "false")
    expect(within(backlog).queryByRole("link", { name: /COPY-3/ })).not.toBeInTheDocument()
    expect(within(working).getByRole("link", { name: /COPY-1/ })).toBeInTheDocument()
    fireEvent.click(within(backlog).getByRole("button", { name: /Backlog/ }))
    expect(within(backlog).getByRole("link", { name: /COPY-3/ })).toBeInTheDocument()

    fireEvent.click(screen.getByRole("tab", { name: /Routines/ }))
    expect(screen.getByRole("tab", { name: /Routines/ })).toHaveAttribute("aria-selected", "true")
    const routine = screen.getByRole("link", { name: "Routine Copy review" })
    expect(routine).toHaveAttribute("href", "/routines?routine=copy-review")
    expect(routine).toHaveTextContent("Completed")
    expect(routine).toHaveTextContent("4 runs")
    expect(routine).toHaveTextContent("3 steps")
    expect(screen.queryByText("Other agent routine")).not.toBeInTheDocument()
    expect(screen.queryByRole("link", { name: /COPY-1/ })).not.toBeInTheDocument()
    expect(apiFetch.mock.calls.some(([url]) => String(url).includes("assignee_id=agent-1"))).toBe(true)
  })

  it("keeps failed fetches visible and retries only on demand", async () => {
    apiFetch.mockImplementation(async (url: string) => url.includes("/issues?") ? { ok: false, json: async () => [] } : { ok: true, json: async () => routines })
    render(<AgentWorkTab agentId="agent-1" workspaceId="ws-1" />)
    expect(await screen.findByRole("alert")).toHaveTextContent("Work could not be loaded")
    apiFetch.mockImplementation(async (url: string) => ({ ok: true, json: async () => url.includes("/issues?") ? issues : routines }))
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    expect(await screen.findByRole("link", { name: /COPY-1/ })).toBeInTheDocument()
  })
})
