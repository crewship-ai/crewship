import { afterEach, describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"
import { CrewsExplorer } from "@/components/features/crews/crews-explorer"

// The avatar reads stores and may fetch; the explorer's behaviour under test
// is grouping, counting, folding and the no-match state, none of which need
// a face.
vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: ({ seed }: { seed: string }) => <span data-testid="avatar">{seed}</span>,
}))
vi.mock("next/link", () => ({
  default: ({ children, href, ...rest }: { children: React.ReactNode; href: string }) => <a href={href} {...rest}>{children}</a>,
}))

// Filters and view live in the URL; each test starts on a clean one.
afterEach(() => window.history.replaceState(null, "", "/crews"))

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

  it("puts the crew with an error first, then the running one, with a dot-and-word pill", () => {
    renderExplorer()
    const rows = screen.getAllByRole("button", { name: /^(Ops|Engineering|Quality)$/ })
    expect(rows.map((r) => r.getAttribute("aria-label"))).toEqual(["Ops", "Quality", "Engineering"])
    const ops = screen.getByRole("button", { name: "Ops" })
    expect(within(ops).getByText("1 error")).toBeInTheDocument()
  })

  it("counts agents per status and narrows the crews to the picked one", () => {
    renderExplorer({ crewsTotal: 3, agentsTotal: 3 })
    expect(within(screen.getByTestId("crews-bucket-needs")).getByText("1")).toBeInTheDocument()
    expect(within(screen.getByTestId("crews-bucket-working")).getByText("1")).toBeInTheDocument()
    expect(within(screen.getByTestId("crews-bucket-expired")).getByText("0")).toBeInTheDocument()

    fireEvent.click(screen.getByTestId("crews-bucket-needs"))
    expect(screen.getAllByRole("button", { name: /^(Ops|Engineering|Quality)$/ }).map((r) => r.getAttribute("aria-label"))).toEqual(["Ops"])
    // The match shows without opening the crew.
    expect(screen.getByRole("button", { name: "morgan" })).toBeInTheDocument()
    expect(screen.getByTestId("explorer-count")).toHaveTextContent("1 crew · 1 agent match")
    expect(window.location.search).toBe("?status=needs")

    fireEvent.click(screen.getByRole("button", { name: "Remove filter" }))
    expect(screen.getByTestId("explorer-count")).toHaveTextContent("3 crews · 3 agents")
  })

  it("filters by a facet and shows it as a chip", () => {
    renderExplorer({
      agents: [
        agent("morgan", "ops"), { ...agent("alex", "eng"), agent_role: "LEAD" }, agent("jordan", "qa"),
      ],
    })
    fireEvent.click(screen.getByRole("button", { name: /Filter/ }))
    const panel = screen.getByRole("group", { name: "Filter agents" })
    fireEvent.click(within(panel).getByRole("button", { name: /^Lead/ }))
    expect(screen.getAllByRole("button", { name: /^(Ops|Engineering|Quality)$/ }).map((r) => r.getAttribute("aria-label"))).toEqual(["Engineering"])
    expect(screen.getByRole("button", { name: "Remove filter" }).parentElement).toHaveTextContent("Lead")
    expect(window.location.search).toBe("?role=lead")
  })

  it("puts the lead first in a crew", () => {
    renderExplorer({
      crews: [crew("ops", "Ops")],
      agents: [agent("alex", "ops", "ERROR"), agent("riley", "ops"), { ...agent("morgan", "ops"), agent_role: "LEAD" }],
    })
    const names = screen.getAllByRole("button", { name: /^(alex|riley|morgan)$/ }).map((r) => r.getAttribute("aria-label"))
    expect(names).toEqual(["morgan", "alex", "riley"])
    expect(screen.getByLabelText("Lead")).toBeInTheDocument()
  })

  it("lists agents by status when the link asks for it", () => {
    window.history.replaceState(null, "", "/crews?group=status")
    renderExplorer()
    expect(screen.queryByRole("button", { name: "Ops" })).not.toBeInTheDocument()
    // Once as a Status bucket, once as the heading over the agents in it.
    expect(screen.getAllByText("Needs you")).toHaveLength(2)
    const names = screen.getAllByRole("button", { name: /^(morgan|alex|jordan)$/ }).map((r) => r.getAttribute("aria-label"))
    expect(names).toEqual(["morgan", "jordan", "alex"])
  })

  it("keeps the crews in the collapsed rail, with a dot on the one that needs a person", () => {
    const onCrewSelect = vi.fn()
    const onToggleCollapse = vi.fn()
    renderExplorer({ collapsed: true, onCrewSelect, onToggleCollapse })
    fireEvent.click(screen.getByRole("button", { name: "Ops, 1 error" }))
    expect(onCrewSelect).toHaveBeenCalledWith("ops")
    expect(onToggleCollapse).toHaveBeenCalled()
    expect(screen.getByRole("button", { name: "Engineering" })).toBeInTheDocument()
  })

  it("adds an agent to a crew from its row", () => {
    const onAddAgent = vi.fn()
    const onCrewSelect = vi.fn()
    renderExplorer({ onAddAgent, onCrewSelect })
    fireEvent.click(screen.getByRole("button", { name: "Add agent to Engineering" }))
    expect(onAddAgent).toHaveBeenCalledWith("eng")
    expect(onCrewSelect).not.toHaveBeenCalled()
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
