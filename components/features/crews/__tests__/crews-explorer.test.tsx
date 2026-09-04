import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"
import { CrewsExplorer } from "@/components/features/crews/crews-explorer"

// The avatar reads stores and may fetch; the explorer's behaviour under test
// is grouping, counting, folding and the no-match state, none of which need
// a face.
vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: ({ seed }: { seed: string }) => <span data-testid="avatar">{seed}</span>,
}))

const crew = (id: string, name: string) => ({ id, name, slug: id, color: null, icon: null, _count: { agents: 0 } })
const agent = (id: string, crew_id: string | null, status = "IDLE") => ({
  id, name: id, slug: id, status, role_title: "Engineer", agent_role: "AGENT", crew_id,
})

function renderExplorer(overrides: Partial<React.ComponentProps<typeof CrewsExplorer>> = {}) {
  const props: React.ComponentProps<typeof CrewsExplorer> = {
    crews: [crew("ops", "Ops"), crew("eng", "Engineering"), crew("qa", "Quality")],
    agents: [agent("morgan", "ops", "ERROR"), agent("alex", "eng"), agent("jordan", "qa", "RUNNING")],
    selectedCrewId: null,
    selectedAgentId: null,
    collapsed: false,
    onToggleCollapse: () => {},
    onCrewSelect: () => {},
    onAgentSelect: () => {},
    ...overrides,
  }
  return render(<CrewsExplorer {...props} />)
}

describe("CrewsExplorer", () => {
  it("counts the server's total, not the loaded page", () => {
    renderExplorer({ crewsTotal: 103, agentsTotal: 308 })
    expect(screen.getByTestId("explorer-count")).toHaveTextContent("103 crews · 308 agents")
  })

  it("puts the crew with an error first, under Needs attention, with a dot-and-word pill", () => {
    renderExplorer()
    const rows = screen.getAllByRole("button", { name: /^(Ops|Engineering|Quality)$/ })
    expect(rows.map((r) => r.getAttribute("aria-label"))).toEqual(["Ops", "Quality", "Engineering"])
    const ops = screen.getByRole("button", { name: "Ops" })
    expect(within(ops).getByText("1 error")).toBeInTheDocument()
    expect(screen.getByText("Needs attention")).toBeInTheDocument()
    expect(screen.getByText("Running")).toBeInTheDocument()
    expect(screen.getByText("Idle")).toBeInTheDocument()
  })

  it("says what a search matched, and offers a way out when nothing does", () => {
    renderExplorer({ crewsTotal: 3, agentsTotal: 3 })
    const box = screen.getByLabelText("Search crews, agents…")
    fireEvent.change(box, { target: { value: "quality" } })
    expect(screen.getByTestId("explorer-count")).toHaveTextContent("1 crew · 0 agents match")

    fireEvent.change(box, { target: { value: "zzzz" } })
    expect(screen.getByTestId("explorer-count")).toHaveTextContent("0 crews · 0 agents match")
    expect(screen.getByText(/Nothing matches “zzzz”/)).toBeInTheDocument()
    fireEvent.click(screen.getAllByRole("button", { name: "Clear" })[0])
    expect(screen.getByTestId("explorer-count")).toHaveTextContent("3 crews · 3 agents")
  })

  it("folds idle crews after six and shows them all on ask", () => {
    const crews = Array.from({ length: 10 }, (_, i) => crew(`c${i}`, `Crew ${i}`))
    renderExplorer({ crews, agents: [] })
    expect(screen.getAllByRole("button", { name: /^Crew \d$/ })).toHaveLength(6)
    fireEvent.click(screen.getByRole("button", { name: /4 more crews · idle/ }))
    expect(screen.getAllByRole("button", { name: /^Crew \d$/ })).toHaveLength(10)
  })

  it("folds the attention group too — a host where every crew needs a rebuild is not a wall", () => {
    const crews = Array.from({ length: 10 }, (_, i) => crew(`c${i}`, `Crew ${i}`))
    const provisioningByCrew = new Map(crews.map((c) => [c.id, "needs_provision" as const]))
    renderExplorer({ crews, agents: crews.map((c) => agent(`a-${c.id}`, c.id)), provisioningByCrew })
    expect(screen.getAllByRole("button", { name: /^Crew \d$/ })).toHaveLength(6)
    // Only the visible attention crews open by default.
    expect(screen.getAllByRole("button", { name: /^a-c\d$/ })).toHaveLength(6)
    fireEvent.click(screen.getByRole("button", { name: /4 more crews · need attention/ }))
    expect(screen.getAllByRole("button", { name: /^Crew \d$/ })).toHaveLength(10)
  })

  it("offers to load the rows the page did not bring", () => {
    const onLoadMore = vi.fn()
    renderExplorer({ crewsTotal: 103, agentsTotal: 3, hasMore: true, onLoadMore })
    fireEvent.click(screen.getByRole("button", { name: /100 more crews not loaded/ }))
    expect(onLoadMore).toHaveBeenCalledTimes(1)
  })
})
