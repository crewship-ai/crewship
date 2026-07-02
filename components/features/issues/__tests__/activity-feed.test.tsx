import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"

import { ActivityFeed } from "../activity-feed"
import type { IssueActivity } from "@/lib/types/mission"

function makeActivity(overrides: Partial<IssueActivity> = {}): IssueActivity {
  return {
    id: "act-1",
    mission_id: "iss-1",
    actor_type: "user",
    actor_id: "user-1",
    actor_name: "Pavel",
    action: "status_changed",
    details: "BACKLOG → TODO",
    created_at: new Date().toISOString(),
    ...overrides,
  }
}

describe("<ActivityFeed> — actor_type rendering", () => {
  it("shows an agent chip next to agent actors", () => {
    render(
      <ActivityFeed
        activities={[
          makeActivity({ id: "a1", actor_type: "agent", actor_id: "agent-9", actor_name: "Scout" }),
        ]}
      />,
    )
    expect(screen.getByText("Scout")).toBeInTheDocument()
    expect(screen.getByTestId("activity-actor-agent-chip")).toBeInTheDocument()
  })

  it("shows no agent chip for human actors", () => {
    render(<ActivityFeed activities={[makeActivity()]} />)
    expect(screen.getByText("Pavel")).toBeInTheDocument()
    expect(screen.queryByTestId("activity-actor-agent-chip")).not.toBeInTheDocument()
  })

  it("chips only the agent rows in a mixed feed", () => {
    render(
      <ActivityFeed
        activities={[
          makeActivity({ id: "a1" }),
          makeActivity({ id: "a2", actor_type: "agent", actor_id: "agent-9", actor_name: "Scout" }),
          makeActivity({ id: "a3", actor_type: "system", actor_id: "system", actor_name: undefined }),
        ]}
      />,
    )
    expect(screen.getAllByTestId("activity-actor-agent-chip")).toHaveLength(1)
  })
})
