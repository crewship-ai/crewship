// Start / Stop / Approve / Request changes / Reopen — the five verbs that
// were spread across issue-sidebar (four of them) and issue-detail-inline
// (all five, plus the reason box). Neither list was complete, so promoting
// either one on its own would have dropped a verb.

import { describe, it, expect, vi } from "vitest"
import { cleanup, render, screen, fireEvent } from "@testing-library/react"

import { IssueWorkflowActions } from "../issue-card-editors"
import type { Mission } from "@/lib/types/mission"

function issue(over: Partial<Mission> = {}): Mission {
  return {
    id: "iss1",
    workspace_id: "ws1",
    crew_id: "crew1",
    lead_agent_id: "",
    lead_agent_name: "",
    lead_agent_slug: "",
    trace_id: "",
    title: "One",
    description: null,
    status: "BACKLOG",
    plan: null,
    workflow_template: null,
    total_token_count: null,
    total_estimated_cost: null,
    created_at: "2026-08-01T12:00:00Z",
    updated_at: "2026-08-01T12:00:00Z",
    completed_at: null,
    task_stats: null,
    tasks: [],
    total_token_budget: null,
    complexity: null,
    pattern: null,
    identifier: "ENG-4",
    ...over,
  }
}

function setup(over: Partial<Mission> = {}) {
  const onAction = vi.fn(async () => {})
  render(<IssueWorkflowActions issue={issue(over)} onAction={onAction} />)
  return onAction
}

describe("IssueWorkflowActions", () => {
  it("offers Start work only once somebody is assigned", () => {
    setup({ status: "TODO" })
    expect(screen.queryByRole("button", { name: /start work/i })).toBeNull()
    cleanup()

    const onAction = setup({ status: "TODO", assignee_id: "agent-robin" })
    fireEvent.click(screen.getByRole("button", { name: /start work/i }))
    expect(onAction).toHaveBeenCalledWith("start", undefined)
  })

  it("offers Stop while the work is running", () => {
    const onAction = setup({ status: "IN_PROGRESS" })
    fireEvent.click(screen.getByRole("button", { name: /^stop$/i }))
    expect(onAction).toHaveBeenCalledWith("stop", undefined)
  })

  it("approves a review outright", () => {
    const onAction = setup({ status: "REVIEW" })
    fireEvent.click(screen.getByRole("button", { name: /^approve$/i }))
    expect(onAction).toHaveBeenCalledWith("approve", undefined)
  })

  it("sends the reason along when changes are requested", () => {
    const onAction = setup({ status: "REVIEW" })
    fireEvent.click(screen.getByRole("button", { name: /request changes/i }))
    fireEvent.change(screen.getByLabelText(/what needs to change/i), {
      target: { value: "The CSV has no header row." },
    })
    fireEvent.click(screen.getByRole("button", { name: /^send$/i }))
    expect(onAction).toHaveBeenCalledWith("request_changes", "The CSV has no header row.")
  })

  it("reopens a closed issue", () => {
    const onAction = setup({ status: "DONE" })
    fireEvent.click(screen.getByRole("button", { name: /reopen/i }))
    expect(onAction).toHaveBeenCalledWith("reopen", undefined)
  })

  it("shows nothing to press while a transition is in flight", () => {
    render(<IssueWorkflowActions issue={issue({ status: "IN_PROGRESS" })} onAction={vi.fn()} busy />)
    expect(screen.getByRole("button", { name: /^stop$/i })).toBeDisabled()
  })
})
