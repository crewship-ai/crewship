import { describe, it, expect } from "vitest"
import { render, screen, within, fireEvent } from "@testing-library/react"

import { IssueRunsCard, issueRunLinks, issueRunsEmptyCopy, type IssueRun } from "@/components/features/issues/issue-runs-card"
import type { Mission } from "@/lib/types/mission"

// The first leg of the one timeline: an issue lists EVERY run, and each row
// opens the run, the agent and the issue's journal. The rail used to show
// runs[0] and discard the rest, with a name nobody could follow.

const issue = { id: "m_eng1", identifier: "ENG-1", status: "IN_PROGRESS" } as unknown as Mission

const runs: IssueRun[] = [
  { id: "asg_a", run_id: "run_aaaa", trace_id: "run_aaaa", status: "RUNNING", agent_id: "ag_1", agent_slug: "robin", agent_name: "Robin", task: "Build the landing page", started_at: new Date().toISOString(), duration_ms: 0 },
  { id: "asg_b", run_id: "run_bbbb", trace_id: "run_bbbb", status: "COMPLETED", agent_id: "ag_2", agent_slug: "sam", agent_name: "Sam", task: "Write the copy", duration_ms: 168000, result_summary: "three bullets, FAQ has four questions" },
  { id: "asg_c", status: "PENDING", agent_id: "ag_3", agent_slug: "alex", agent_name: "Alex", task: "Merge", duration_ms: 0 },
]

describe("issueRunLinks", () => {
  it("builds every link through entityHref", () => {
    const l = issueRunLinks(issue, runs[0])
    expect(l.run).toBe("/activity?run=run_aaaa")
    expect(l.agent).toBe("/crews?agent=robin")
    expect(l.journal).toBe("/journal?mission_id=ENG-1")
    expect(l.activity).toBe("/activity?mission=m_eng1")
  })

  it("has no run link for an assignment that never ran", () => {
    expect(issueRunLinks(issue, runs[2]).run).toBeNull()
  })

  it("falls back to the id when the issue has no identifier", () => {
    expect(issueRunLinks({ id: "m_x", identifier: null }).journal).toBe("/journal?mission_id=m_x")
  })
})

describe("IssueRunsCard", () => {
  it("lists every run with its status as a word and links the run, the agent and the journal", () => {
    render(<IssueRunsCard issue={issue} runs={runs} />)
    expect(screen.getAllByTestId("issue-run-row")).toHaveLength(3)
    expect(screen.getByText("Running")).toBeInTheDocument()
    expect(screen.getByText("Outcome not reported")).toBeInTheDocument()
    expect(screen.queryByText("Done")).not.toBeInTheDocument()
    expect(screen.getByText("Pending")).toBeInTheDocument()

    const openRun = screen.getAllByRole("link", { name: /open run/i })
    expect(openRun.map((a) => a.getAttribute("href"))).toEqual(["/activity?run=run_aaaa", "/activity?run=run_bbbb"])
    expect(screen.getByRole("link", { name: "Robin" })).toHaveAttribute("href", "/crews?agent=robin")
    expect(screen.getByRole("link", { name: /journal for ENG-1/i })).toHaveAttribute("href", "/journal?mission_id=ENG-1")
    expect(screen.getByRole("link", { name: /all runs in activity/i })).toHaveAttribute("href", "/activity?mission=m_eng1")
    expect(screen.getByText("no run")).toBeInTheDocument()
  })

  it("says what will appear and how when nothing has run", () => {
    render(<IssueRunsCard issue={{ ...issue, status: "TODO" } as Mission} runs={[]} />)
    expect(screen.getByText(issueRunsEmptyCopy({ ...issue, status: "TODO" }))).toBeInTheDocument()
    expect(screen.queryByRole("link", { name: /all runs/i })).toBeNull()
  })

  it.each([
    ["FAILED", "Failed", "danger"],
    ["NEEDS_HUMAN", "Needs human input", "warn"],
    ["PARTIAL", "Partial result", "warn"],
    ["SUCCEEDED", "Reported success", "success"],
    ["WORK_CREATED", "Work created", "blue"],
    ["NO_CHANGE", "No changes", "muted"],
    ["", "Outcome not reported", "muted"],
    ["future_value", "Outcome not reported", "muted"],
  ])("shows the work result for a completed process: %s", (outcome, label, tone) => {
    render(<IssueRunsCard issue={issue} runs={[{ ...runs[1], outcome }]} />)
    expect(screen.getByText(label)).toHaveAttribute("data-tone", tone)
    expect(screen.queryByText("Done")).not.toBeInTheDocument()
  })

  it.each(["RUNNING", "QUEUED", "FAILED", "CANCELLED"])("does not let a success report override process state %s", (status) => {
    render(<IssueRunsCard issue={issue} runs={[{ ...runs[1], status, outcome: "SUCCEEDED" }]} />)
    expect(screen.queryByText("Reported success")).not.toBeInTheDocument()
  })

  it("keeps failure and the reported summary together, with the full summary available", () => {
    const summary = "Partial findings. ".repeat(80) + "Final detail that must remain readable."
    render(<IssueRunsCard issue={issue} runs={[{ ...runs[1], outcome: "FAILED", error_message: "no outcome reported", result_summary: summary }]} />)
    const row = screen.getByTestId("issue-run-row")
    expect(within(row).getByText("Failed")).toBeInTheDocument()
    expect(within(row).getByText("no outcome reported")).toBeInTheDocument()
    fireEvent.click(within(row).getByText("Reported summary"))
    expect(within(row).getByText(summary)).toBeInTheDocument()
  })

  it("does not use prompt scaffolding as a task title", () => {
    render(<IssueRunsCard issue={{ ...issue, title: "Review the report" }} runs={[{ ...runs[1], task: "[MISSION]\nInstructions: internal execution prompt", source: "mention" }]} />)
    expect(screen.getByText("Review the report")).toBeInTheDocument()
    expect(screen.getByText(/From a message/)).toBeInTheDocument()
    expect(screen.queryByText(/internal execution prompt/)).not.toBeInTheDocument()
  })

  it("does not infer a deliverable from a successful report", () => {
    render(<IssueRunsCard issue={issue} runs={[{ ...runs[1], outcome: "SUCCEEDED", result_summary: "" }]} />)
    expect(screen.getByText("Reported success")).toBeInTheDocument()
    expect(screen.getByText("No summary recorded.")).toBeInTheDocument()
  })

  it("does not ask a human-only issue to start an agent", () => {
    render(<IssueRunsCard issue={{ ...issue, status: "TODO", assignee_type: "user", assignee_id: "u1", owner: { id: "u1", name: "Marta" } }} runs={[]} />)
    expect(screen.getByText(/human owner; an agent delegate is optional/)).toBeInTheDocument()
    expect(screen.queryByText(/Start work/)).not.toBeInTheDocument()
  })

  it("can start delegated work while preserving its human owner", () => {
    render(<IssueRunsCard issue={{ ...issue, status: "TODO", owner: { id: "u1", name: "Marta" }, delegate: { id: "a1", name: "Robin" } }} runs={[]} />)
    expect(screen.getByText(/Start work to hand this issue to its agent/)).toBeInTheDocument()
  })
})
