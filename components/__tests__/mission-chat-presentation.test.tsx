import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"
import { MissionHeader } from "@/components/features/missions/mission-header"
import { MissionStatusBadge, TaskStatusBadge } from "@/components/features/missions/mission-status-badge"
import { ThinkingAvatar } from "@/components/features/chat/messages/thinking-avatar"
import type { Mission, MissionStatus, MissionTaskStatus } from "@/lib/types/mission"

const mission: Mission = {
  id: "mission", workspace_id: "workspace", crew_id: "crew", lead_agent_id: "agent", lead_agent_name: "Researcher", lead_agent_slug: "researcher",
  trace_id: "trace-visible", title: "Review release", description: "Check the release evidence", status: "DONE", plan: null, workflow_template: null,
  total_token_count: null, total_estimated_cost: 1.25, created_at: "2026-10-03T00:00:00Z", updated_at: "2026-10-03T00:01:00Z", completed_at: "2026-10-03T00:01:00Z",
  task_stats: null, tasks: [], total_token_budget: null, complexity: null, pattern: null,
}

describe("mission summary", () => {
  it("shows the title, description, lead, trace and formatted cost", () => {
    render(<MissionHeader mission={mission} />)
    expect(screen.getByRole("heading", { name: "Review release" })).toBeInTheDocument()
    expect(screen.getByText("Check the release evidence")).toBeInTheDocument()
    expect(screen.getByText("Lead: @researcher")).toBeInTheDocument()
    expect(screen.getByText("trace-visible")).toBeInTheDocument()
    expect(screen.getByText(/Cost:.*1\.25/)).toBeInTheDocument()
    expect(screen.getByText("Done")).toBeInTheDocument()
  })

  it("omits an absent description and displays a running mission", () => {
    render(<MissionHeader mission={{ ...mission, description: null, completed_at: null, status: "IN_PROGRESS", total_estimated_cost: null }} />)
    expect(screen.queryByText("Check the release evidence")).not.toBeInTheDocument()
    expect(screen.getByText("In Progress")).toBeInTheDocument()
    expect(screen.getByText(/Duration:/)).toBeInTheDocument()
  })

  it.each<[MissionStatus, string]>([
    ["BACKLOG", "Backlog"], ["TODO", "Todo"], ["PLANNING", "Planning"], ["IN_PROGRESS", "In Progress"], ["REVIEW", "Review"],
    ["COMPLETED", "Completed"], ["DONE", "Done"], ["FAILED", "Failed"], ["CANCELLED", "Cancelled"], ["DUPLICATE", "Duplicate"],
  ])("renders the mission state %s as readable text", (status, label) => {
    render(<MissionStatusBadge status={status} />)
    expect(screen.getByText(label)).toBeInTheDocument()
  })

  it.each<[MissionTaskStatus, string]>([
    ["PENDING", "Pending"], ["BLOCKED", "Blocked"], ["IN_PROGRESS", "Working"], ["COMPLETED", "Completed"],
    ["FAILED", "Failed"], ["SKIPPED", "Skipped"], ["AWAITING_APPROVAL", "Awaiting Approval"],
  ])("renders the task state %s as readable text", (status, label) => {
    render(<TaskStatusBadge status={status} />)
    expect(screen.getByText(label)).toBeInTheDocument()
  })
})

describe("thinking avatar", () => {
  it("keeps an inaccessible decorative placeholder when there is no agent", () => {
    const { container } = render(<ThinkingAvatar agent={null} active={false} className="chat-gutter" />)
    expect(container.firstElementChild).toHaveAttribute("aria-hidden", "true")
    expect(container.firstElementChild).toHaveClass("chat-gutter")
    expect(screen.queryByTestId("thinking-avatar")).not.toBeInTheDocument()
  })

  it.each([undefined, "custom-seed"])("starts and stops the thinking indicator with seed %s", (avatarSeed) => {
    const agent = { id: "agent", name: "Researcher", avatarSeed }
    const { rerender } = render(<ThinkingAvatar agent={agent} active className="chat-gutter" />)
    const avatar = screen.getByTestId("thinking-avatar")
    expect(avatar).toHaveAttribute("data-active", "true")
    expect(avatar.querySelector(".thinking-ring")).toHaveAttribute("aria-hidden", "true")
    rerender(<ThinkingAvatar agent={agent} active={false} />)
    expect(avatar).toHaveAttribute("data-active", "false")
    expect(avatar.querySelector(".thinking-ring")).toBeNull()
  })
})
