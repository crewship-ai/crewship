import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

// Avatar URL generation hits DiceBear — stub it so the test stays offline.
vi.mock("@/lib/agent-avatar", () => ({
  getAgentAvatarUrl: () => "https://example.test/avatar.svg",
}))

import { IssueCard } from "../issue-card"
import type { Mission } from "@/lib/types/mission"

function makeIssue(overrides: Partial<Mission> = {}): Mission {
  return {
    id: "iss-1",
    workspace_id: "ws-1",
    crew_id: "crew-1",
    lead_agent_id: "agent-lead",
    lead_agent_name: "Lead",
    lead_agent_slug: "lead",
    trace_id: "t-1",
    title: "Fix the flaky test",
    description: null,
    status: "TODO",
    plan: null,
    workflow_template: null,
    total_token_count: null,
    total_estimated_cost: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    completed_at: null,
    task_stats: null,
    tasks: [],
    total_token_budget: null,
    complexity: null,
    pattern: null,
    identifier: "ENG-1",
    priority: "none",
    labels: [],
    ...overrides,
  } as Mission
}

describe("<IssueCard> — assignee avatar by type", () => {
  it("renders the agent avatar for an agent assignee", () => {
    render(
      <IssueCard
        issue={makeIssue({ assignee_type: "agent", assignee_id: "agent-9", assignee_name: "Scout" })}
        onClick={() => {}}
      />,
    )
    expect(screen.getByTestId("assignee-avatar-agent")).toBeInTheDocument()
    expect(screen.queryByTestId("assignee-avatar-user")).not.toBeInTheDocument()
  })

  it("renders the user glyph (not an agent avatar) for a human assignee", () => {
    render(
      <IssueCard
        issue={makeIssue({ assignee_type: "user", assignee_id: "user-7", assignee_name: "Pavel" })}
        onClick={() => {}}
      />,
    )
    expect(screen.getByTestId("assignee-avatar-user")).toBeInTheDocument()
    expect(screen.queryByTestId("assignee-avatar-agent")).not.toBeInTheDocument()
  })

  it("renders no avatar when unassigned", () => {
    render(<IssueCard issue={makeIssue()} onClick={() => {}} />)
    expect(screen.queryByTestId("assignee-avatar-agent")).not.toBeInTheDocument()
    expect(screen.queryByTestId("assignee-avatar-user")).not.toBeInTheDocument()
  })
})
