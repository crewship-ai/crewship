import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import {
  MissionStatusBadge,
  TaskStatusBadge,
} from "@/components/features/missions/mission-status-badge"

describe("MissionStatusBadge", () => {
  it.each([
    ["PLANNING", "Planning"],
    ["IN_PROGRESS", "In Progress"],
    ["REVIEW", "Review"],
    ["COMPLETED", "Completed"],
    ["FAILED", "Failed"],
    ["CANCELLED", "Cancelled"],
    ["BACKLOG", "Backlog"],
    ["TODO", "Todo"],
    ["DONE", "Done"],
    ["DUPLICATE", "Duplicate"],
  ])("status=%s renders label %q", (status, label) => {
    render(<MissionStatusBadge status={status as any} />)
    expect(screen.getByText(label)).toBeInTheDocument()
  })

  it("renders an icon alongside the label", () => {
    const { container } = render(<MissionStatusBadge status={"IN_PROGRESS" as any} />)
    expect(container.querySelector("svg")).toBeTruthy()
  })
})

describe("TaskStatusBadge", () => {
  it.each([
    ["PENDING", "Pending"],
    ["BLOCKED", "Blocked"],
    ["IN_PROGRESS", "Working"],
    ["COMPLETED", "Completed"],
    ["FAILED", "Failed"],
    ["SKIPPED", "Skipped"],
    ["AWAITING_APPROVAL", "Awaiting Approval"],
  ])("status=%s renders label %q", (status, label) => {
    render(<TaskStatusBadge status={status as any} />)
    expect(screen.getByText(label)).toBeInTheDocument()
  })
})
