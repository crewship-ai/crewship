import { fireEvent, render, screen, within } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import { DashboardResults } from "../dashboard-results"
import type { Mission } from "@/lib/types/mission"

describe("dashboard live work", () => {
  it("keeps a running execution distinct from its in-progress issue", () => {
    const issue = {
      id: "issue-1", identifier: "ENG-1", title: "Prepare release",
      status: "IN_PROGRESS", updated_at: "2026-09-25T10:00:00Z",
      crew_id: null, lead_agent_id: null, assignee_id: null,
    } as Mission
    render(<DashboardResults
      review={[]} inProgress={[issue]} completed={[]}
      activeAgentRuns={[{
        id: "run-1", kind: "agent", status: "RUNNING", mission_id: "issue-1",
        mission_identifier: "ENG-1", agent_id: "agent-1", agent_name: "Builder",
        created_at: "2026-09-25T10:00:00Z",
      }]}
      activeRoutineRuns={[]} recentRoutineRuns={[]}
      agents={[]} crews={[]} workspaceId="ws-1"
      loading={false} error={false} routineError={null} routineLoading={false}
      onRetry={() => {}}
    />)

    expect(within(screen.getByRole("region", { name: "Running now" })).getByText("Prepare release")).toBeTruthy()
    expect(within(screen.getByRole("region", { name: "In progress" })).getByText("Prepare release")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: /^Running/ }))
    expect(screen.getByRole("region", { name: "Running now" })).toBeTruthy()
    expect(screen.queryByRole("region", { name: "In progress" })).toBeNull()
  })
})
