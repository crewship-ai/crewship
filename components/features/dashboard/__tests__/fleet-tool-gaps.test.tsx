import { describe, it, expect, afterEach, vi } from "vitest"
import { render, screen, cleanup, within } from "@testing-library/react"

import { FleetBoard, toolGapCrews, type FleetCard } from "../fleet-board"
import type { FleetHealthRow } from "../dashboard-overview"
import type { CrewSummary } from "@/app/(dashboard)/dashboard-types"

vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => null }))
vi.mock("@/components/ui/crew-icon", () => ({ CrewIcon: () => null }))
afterEach(cleanup)

const services = { total: 0, running: 0, degraded: 0, checked: false }
const card = (name: string, status: string, tone: FleetHealthRow["tone"]): FleetCard => ({
  row: { crew: { id: name, name, slug: name, color: "blue", icon: "code" } as CrewSummary, status, detail: "", tone, agents: 1, runningAgents: 0, services },
  agents: [], spendUsd: null, runSeries: [], runsTotal: 0,
})

// dev3 showed the same orange "Needs tool" on four of five crews. One missing
// credential reads as four alarms. Two or more crews waiting on a tool become
// one line with one action; the rows keep the word, quietly.
describe("crews waiting on a tool", () => {
  it("lists the crews whose only problem is a missing tool", () => {
    const cards = [card("Coolify", "Needs tool", "warn"), card("Ops", "Agent error", "danger"), card("Quality", "Needs tool", "warn")]
    expect(toolGapCrews(cards)).toEqual(["Coolify", "Quality"])
  })

  it("folds two or more into one call to action and quiets the rows", () => {
    render(<FleetBoard workspaceId="ws" cards={[card("Coolify", "Needs tool", "warn"), card("Engineering", "Needs tool", "warn"), card("Ops", "Ready", "success")]} />)
    const banner = screen.getByRole("link", { name: /2 crews need a tool/ })
    expect(banner.getAttribute("href")).toBe("/credentials")
    const coolify = screen.getAllByTestId("dashboard-fleet-card")[0]
    expect(within(coolify).getByText("Needs tool").closest("[data-tone]")!.getAttribute("data-tone")).toBe("muted")
  })

  it("keeps a single waiting crew as a normal warning row", () => {
    render(<FleetBoard workspaceId="ws" cards={[card("Coolify", "Needs tool", "warn"), card("Ops", "Ready", "success")]} />)
    expect(screen.queryByRole("link", { name: /need a tool/ })).toBeNull()
    expect(screen.getByText("Needs tool").closest("[data-tone]")!.getAttribute("data-tone")).toBe("warn")
  })
})
