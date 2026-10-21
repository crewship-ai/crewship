import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, within } from "@testing-library/react"

import { CrewsPanel } from "@/components/features/crews/crews-panel"

const NOW = Date.parse("2026-10-17T10:00:00Z")

const crews = [
  { id: "c-ops", name: "Ops", slug: "ops", icon: "server", color: "emerald" },
  { id: "c-res", name: "Research", slug: "research", icon: "search", color: "cyan" },
]
const agents = [
  { id: "a1", name: "Opsy", slug: "opsy", status: "IDLE", agent_role: "LEAD", crew_id: "c-ops" },
  { id: "a2", name: "Pager", slug: "pager", status: "IDLE", agent_role: "AGENT", crew_id: "c-ops", ephemeral: true, expires_at: "2026-10-17T10:38:00Z" },
  { id: "a3", name: "Night watch", slug: "nightwatch", status: "IDLE", agent_role: "AGENT", crew_id: "c-ops", ephemeral: true, expired_at: "2026-10-16T22:00:00Z" },
  { id: "a4", name: "Scout", slug: "scout", status: "RUNNING", agent_role: "LEAD", crew_id: "c-res" },
  { id: "a5", name: "Concierge", slug: "concierge", status: "IDLE", agent_role: "AGENT", crew_id: null },
]

function renderPanel(openCrewId: string | null, extra: Partial<React.ComponentProps<typeof CrewsPanel>> = {}) {
  const onCrewSelect = vi.fn()
  const onAgentSelect = vi.fn()
  render(
    <CrewsPanel
      crews={crews}
      agents={agents}
      openCrewId={openCrewId}
      selectedCrewId={openCrewId}
      selectedAgentId={null}
      onCrewSelect={onCrewSelect}
      onAgentSelect={onAgentSelect}
      onNewCrew={vi.fn()}
      {...extra}
    />,
  )
  return { onCrewSelect, onAgentSelect }
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] })
  vi.setSystemTime(NOW)
  window.history.replaceState(null, "", "/crews")
})
afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe("CrewsPanel", () => {
  it("lead shows a star, temporary hires show time left, agents only under the open crew", () => {
    renderPanel("c-ops")
    // Ops is open: its agents are listed, Research's are not.
    expect(screen.getByRole("button", { name: "Opsy" })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Scout" })).toBeNull()
    const opsy = screen.getByRole("button", { name: "Opsy" })
    expect(within(opsy).getByLabelText("Lead")).toBeInTheDocument()
    expect(within(opsy).getByText("LEAD")).toBeInTheDocument()
    expect(within(screen.getByRole("button", { name: "Pager" })).getByText("38 min")).toBeInTheDocument()
    expect(within(screen.getByRole("button", { name: "Night watch" })).getByText("expired")).toBeInTheDocument()
  })

  it("lists Views with counts, the crews by status and the agents without a crew", () => {
    renderPanel(null)
    for (const v of ["All crews", "Needs you", "Working", "Idle", "Temporary hires"]) {
      expect(screen.getByTestId(`crews-view-${({ "All crews": "all", "Needs you": "needs", Working: "working", Idle: "idle", "Temporary hires": "temp" } as const)[v as "Idle"]}`)).toHaveTextContent(v)
    }
    expect(screen.getByTestId("crews-view-temp")).toHaveTextContent("1")
    expect(screen.getByText("Without a crew")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Concierge" })).toBeInTheDocument()
    expect(document.querySelector('[data-group="working"] [data-crew-row="research"]')).not.toBeNull()
  })

  it("a view narrows the crews and is written to ?status=; A–Z to ?group=name", () => {
    renderPanel(null)
    fireEvent.click(screen.getByTestId("crews-view-working"))
    expect(window.location.search).toContain("status=working")
    expect(document.querySelector('[data-crew-row="ops"]')).toBeNull()
    expect(document.querySelector('[data-crew-row="research"]')).not.toBeNull()
    fireEvent.click(screen.getByRole("radio", { name: "A–Z" }))
    expect(window.location.search).toContain("group=name")
  })

  it("keys 1–5 pick a view", () => {
    renderPanel(null)
    fireEvent.keyDown(document.body, { key: "5" })
    expect(window.location.search).toContain("status=temp")
  })

  it("nothing found says so and offers Clear", () => {
    renderPanel(null)
    fireEvent.change(screen.getByPlaceholderText("Search crews and agents…"), { target: { value: "zzz" } })
    expect(screen.getByText("No crews or agents match")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Clear" }))
    expect(document.querySelector('[data-crew-row="ops"]')).not.toBeNull()
  })

  it("picking an agent hands its crew along", () => {
    const { onAgentSelect } = renderPanel("c-ops")
    fireEvent.click(screen.getByRole("button", { name: "Pager" }))
    expect(onAgentSelect).toHaveBeenCalledWith({ slug: "pager", crewSlug: "ops" })
  })
})
