import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, within } from "@testing-library/react"
import { EmptyRoster } from "../empty-roster"

const crews = [{ id: "crew_ops", slug: "ops", name: "Ops" }]

function agent(over: Record<string, unknown>) {
  return {
    id: "a1",
    name: "Agent One",
    slug: "agent-one",
    status: "IDLE",
    role_title: null,
    agent_role: "AGENT",
    crew_id: "crew_ops",
    ...over,
  } as never
}

describe("EmptyRoster — ephemeral surfacing", () => {
  afterEach(() => cleanup())

  it("tags ephemeral agents with an EPHEMERAL badge and shows pending-review status", () => {
    render(
      <EmptyRoster
        agents={[
          agent({ id: "perm", name: "Permanent One", slug: "perm" }),
          agent({
            id: "eph",
            name: "Hired One",
            slug: "hired-one",
            status: "PENDING_REVIEW",
            ephemeral: true,
            expires_at: "2099-01-01T00:00:00Z",
          }),
        ]}
        crews={crews}
        onAgentSelect={vi.fn()}
      />,
    )

    // One ephemeral agent → exactly one badge.
    expect(screen.getAllByText("EPHEMERAL")).toHaveLength(1)
    expect(screen.getByText("Pending review")).toBeTruthy()
  })

  it("dims an expired (ghost) agent and offers Rehire that fires the rehire event", () => {
    const dispatch = vi.spyOn(window, "dispatchEvent")
    render(
      <EmptyRoster
        agents={[
          agent({
            id: "ghost",
            name: "Ghost One",
            slug: "ghost-one",
            status: "RUNNING", // server may still say RUNNING; expired_at wins
            ephemeral: true,
            expired_at: "2026-06-25T10:00:00Z",
          }),
        ]}
        crews={crews}
        onAgentSelect={vi.fn()}
      />,
    )

    // Ghost row is dimmed via data-expired and shows Expired, not Running.
    const ghostRow = document.querySelector('[data-expired="true"]')
    expect(ghostRow).not.toBeNull()
    expect(within(ghostRow as HTMLElement).getByText(/expired/i)).toBeTruthy()
    expect(screen.queryByText("Running")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: /rehire/i }))
    const evt = dispatch.mock.calls.map((c) => c[0]).find((e) => e.type === "agent.rehire.request") as CustomEvent
    expect(evt).toBeTruthy()
    expect(evt.detail).toMatchObject({ agentId: "ghost", agentName: "Ghost One" })
    dispatch.mockRestore()
  })

  it("selects the agent when a row is clicked", () => {
    const onSelect = vi.fn()
    render(
      <EmptyRoster agents={[agent({})]} crews={crews} onAgentSelect={onSelect} />,
    )
    fireEvent.click(screen.getByText("Agent One"))
    expect(onSelect).toHaveBeenCalledWith("agent-one")
  })
})
