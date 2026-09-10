import { fireEvent, render, screen, within } from "@testing-library/react"
import { expect, it, vi } from "vitest"
import type { Pipeline } from "@/hooks/use-pipelines"
import { RoutinesWorkspace } from "../routines-workspace"

vi.mock("@/hooks/use-issue-detail", () => ({ useUrlSelection: () => [null, vi.fn()] }))
vi.mock("@/hooks/use-active-routine-runs", () => ({
  useActiveRoutineRuns: () => ({ bySlug: new Map() }),
  isAwaitingApproval: () => false,
}))
vi.mock("@/hooks/use-pipeline-runs", () => ({ usePipelineRuns: vi.fn() }))
vi.mock("../routines-overview", () => ({ RoutinesOverview: () => <div>Health dashboard</div> }))
vi.mock("../routine-calendar", () => ({ RoutineCalendar: () => <div>Calendar</div> }))

it("opens on the searchable main list with purposes and counts, and selects the recipe", () => {
  const select = vi.fn()
  const rows = [
    {
      id: "one",
      slug: "report",
      name: "Weekly report",
      description: "Summarize service health",
      step_count: 4,
    },
    {
      id: "two",
      slug: "other",
      name: "Other routine",
      description: "File a receipt",
      step_count: 1,
    },
  ] as Pipeline[]
  render(
    <RoutinesWorkspace
      workspaceId="ws"
      routines={rows}
      loading={false}
      error={null}
      onSelect={select}
      search="service"
      filters={{ status: "all", invocations: "all", authorAgentId: null, showEphemeral: false }}
    />,
  )
  const list = screen.getByRole("region", { name: "Routine list" })
  expect(within(list).getByText("Summarize service health")).toBeVisible()
  expect(within(list).getByText("4 steps")).toBeVisible()
  expect(within(list).queryByText("Other routine")).not.toBeInTheDocument()
  expect(screen.queryByText("Health dashboard")).not.toBeInTheDocument()
  fireEvent.click(within(list).getByRole("button", { name: /Weekly report/ }))
  expect(select).toHaveBeenCalledWith("report")
})
