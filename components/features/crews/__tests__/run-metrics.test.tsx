import { render, screen } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { RunMetrics } from "../workspace-overview"
import { useWorkspaceResource } from "../memory-workspace"

vi.mock("../memory-workspace", () => ({ useWorkspaceResource: vi.fn() }))
vi.mock("@/components/features/dashboard/status-donut", () => ({ StatusDonut: () => <div>Outcome chart</div> }))

const resource = vi.mocked(useWorkspaceResource)
function setData(rows?: { key: string; total: number }[]) {
  resource.mockReturnValue({ key: "runs", data: { totals: { succeeded: 26, failed: 5, running: 0 }, truncated: false, by_trigger: rows } })
}

describe("RunMetrics", () => {
  beforeEach(() => vi.clearAllMocks())
  it("shows scoped outcomes beside actual trigger counts, grouping overflow without losing runs", () => {
    setData([{ key: "CHAT", total: 20 }, { key: "API", total: 5 }, { key: "MANUAL", total: 3 }, { key: "CRON", total: 2 }, { key: "WEBHOOK", total: 1 }, { key: "ASSIGNMENT", total: 1 }])
    render(<RunMetrics workspaceId="ws-1" agentId="a1" />)
    expect(resource.mock.calls[0][0]).toContain("agent_id=a1")
    expect(screen.getByText("Run outcomes")).toBeInTheDocument()
    expect(screen.getByText("How runs start")).toBeInTheDocument()
    expect(screen.getByRole("meter", { name: "Chat" })).toHaveAttribute("aria-valuenow", "20")
    expect(screen.getByRole("meter", { name: "Chat" })).toHaveAttribute("aria-valuemax", "32")
    expect(screen.getByRole("meter", { name: "Other" })).toHaveAttribute("aria-valuenow", "2")
    expect(screen.getAllByRole("meter")).toHaveLength(5)
    expect(screen.getByText("Includes cancelled runs")).toBeInTheDocument()
    expect(screen.getByText("Excludes cancelled runs")).toBeInTheDocument()
  })
  it("does not invent trigger counts when the breakdown is unavailable", () => {
    setData()
    render(<RunMetrics workspaceId="ws-1" crewId="c1" />)
    expect(resource.mock.calls[0][0]).toContain("crew_id=c1")
    expect(screen.getByText("Run sources are not available yet.")).toBeInTheDocument()
    expect(screen.queryByRole("meter")).not.toBeInTheDocument()
  })
  // Three tiles reading 0 said "nothing ran" three times at 32px. A quiet
  // week is one line.
  it("collapses to one line when nothing ran in the window", () => {
    resource.mockReturnValue({ key: "runs", data: { totals: { succeeded: 0, failed: 0, running: 0 }, truncated: false } })
    render(<RunMetrics workspaceId="ws-1" agentId="a1" />)
    expect(screen.queryByText("Completed runs")).not.toBeInTheDocument()
    expect(document.querySelector("[data-slot=inline-empty]")).toHaveTextContent(/No runs in the last 7 days/)
  })
  it("keeps the tiles while the totals are still loading", () => {
    resource.mockReturnValue({ key: "runs", data: undefined })
    render(<RunMetrics workspaceId="ws-1" agentId="a1" />)
    expect(screen.getByText("Completed runs")).toBeInTheDocument()
  })
})
