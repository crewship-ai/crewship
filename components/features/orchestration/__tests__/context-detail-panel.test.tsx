import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import type { AgentSummary, CrewConnection, CrewSummary } from "@/lib/types/orchestration"
import { ContextDetailPanel, type DetailContext } from "../context-detail-panel"
import { ago, mission, task } from "./mission-fixtures"

afterEach(cleanup)
function tab(name: string) { fireEvent.mouseDown(screen.getByRole("tab", { name }), { button: 0, ctrlKey: false }) }
function taskContext(t = task("one"), allTasks = [t]): DetailContext { return { type: "task", task: t, allTasks, mission: mission("m", { tasks: allTasks }) } }
const c: CrewSummary = { id: "crew", name: "Quality", slug: "quality", color: "blue", icon: null }
function agent(id: string, crew_id: string): AgentSummary { return { id, name: id, slug: id, crew_id, avatar_seed: null, avatar_style: null, role_title: null, agent_role: null, crew: null } }
function connection(id: string, from: string, to: string, direction: CrewConnection["direction"]): CrewConnection { return { id, from_crew_id: from, from_crew_name: from, from_crew_slug: from, to_crew_id: to, to_crew_name: to, to_crew_slug: to, direction, status: "active", created_at: ago(10) } }

describe("Context detail panel", () => {
  it("explains empty selections in every tab and closes on request", () => {
    const onClose = vi.fn(); render(<ContextDetailPanel context={{ type: "none" }} onClose={onClose} />)
    expect(screen.getByText("Select a node to view details")).toBeInTheDocument()
    tab("Logs"); expect(screen.getByText("Select a task or mission to view logs")).toBeInTheDocument()
    tab("Trace"); expect(screen.getByText("Select a mission or task to view trace")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Close details" })); expect(onClose).toHaveBeenCalledOnce()
  })
  it("shows task budgets, evaluation and dependencies and dispatches retry and skip", () => {
    const onTaskAction = vi.fn()
    const t = task("one", { status: "FAILED", agent_name: "Sam Example", complexity: "COMPLEX", iteration: 2, max_iterations: 3, tokens_used: 2500, token_budget: 2000, estimated_cost: 0.125, confidence: 0.92, evaluation_status: "FAILED", duration_ms: 1500, depends_on: '["before",12,"missing"]', result_summary: "Useful result", error_message: "Worker refused" })
    render(<ContextDetailPanel context={taskContext(t, [t, task("before", { status: "COMPLETED" }), task("after", { depends_on: '["one"]' })])} onTaskAction={onTaskAction} />)
    expect(screen.getByText("Sam Example")).toBeInTheDocument()
    expect(screen.getByText("Iter 2/3")).toBeInTheDocument()
    expect(screen.getByText("2.5k / 2.0k (100%)")).toBeInTheDocument()
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "100")
    expect(screen.getByText("$0.1250")).toBeInTheDocument(); expect(screen.getByText("92%")).toBeInTheDocument(); expect(screen.getByText("1.5s")).toBeInTheDocument()
    expect(screen.getByText("Task before")).toBeInTheDocument(); expect(screen.getByText("Task after")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Result summary" })); expect(screen.getByText("Useful result")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Error" })); expect(screen.getByText("Worker refused")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Error" })); expect(screen.queryByText("Worker refused")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Retry" })); fireEvent.click(screen.getByRole("button", { name: "Skip" }))
    expect(onTaskAction.mock.calls).toEqual([["retry", "one", "m"], ["skip", "one", "m"]])
  })
  it.each(["PENDING", "BLOCKED", "COMPLETED"] as const)("limits actions for %s and accepts legacy malformed dependencies", status => {
    const t = task("one", { status, depends_on: status === "PENDING" ? "{" : "{}", agent_slug: null, evaluation_status: status === "COMPLETED" ? "PASSED" : "PENDING", token_budget: 0, tokens_used: 10, iteration: 1, max_iterations: 1 })
    render(<ContextDetailPanel context={taskContext(t)} onTaskAction={vi.fn()} />)
    expect(screen.queryByRole("button", { name: "Retry" })).not.toBeInTheDocument()
    if (status === "COMPLETED") { expect(screen.queryByRole("button", { name: "Skip" })).not.toBeInTheDocument(); expect(screen.getByText("Passed")).toBeInTheDocument() }
    else { expect(screen.getByRole("button", { name: "Skip" })).toBeInTheDocument(); expect(screen.getByText("Pending", { exact: true })).toBeInTheDocument() }
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument()
    expect(screen.queryByText("Dependencies")).not.toBeInTheDocument()
  })
  it("presents task log messages including evaluation, handoff and completed usage", () => {
    const t = task("one", { status: "COMPLETED", started_at: ago(10), completed_at: ago(1), token_count: 250, estimated_cost: 0.01, duration_ms: 2000, result_summary: "Produced output", error_message: "Earlier error", evaluation_status: "FAILED", evaluation_notes: "Needs attention", handoff_context: "Continue here" })
    render(<ContextDetailPanel context={taskContext(t)} />); tab("Logs")
    for (const text of ["Task started — assigned to sam", "Produced output", "Earlier error", "Evaluation: Needs attention", "Handoff: Continue here", "Completed in 2.0s — 250 tokens ($0.0100)"]) expect(screen.getByText(text)).toBeInTheDocument()
    expect(screen.getByText("warn")).toBeInTheDocument(); expect(screen.getByText("error")).toBeInTheDocument()
  })
  it("uses task update timestamps and supports missing attribution and zero completed metrics", () => {
    const view = render(<ContextDetailPanel context={taskContext(task("one", { started_at: ago(5), agent_slug: null, result_summary: "Result", error_message: "Error text", handoff_context: "Handoff", evaluation_status: "PASSED", evaluation_notes: "Looks good" }))} />)
    tab("Logs"); expect(screen.getByText("Task started")).toBeInTheDocument(); expect(screen.getByText("Evaluation: Looks good")).toBeInTheDocument()
    view.rerender(<ContextDetailPanel context={taskContext(task("two", { status: "COMPLETED", completed_at: ago(1), duration_ms: 0, token_count: 0 }))} />)
    tab("Logs"); expect(screen.getByText("Completed", { exact: true })).toBeInTheDocument()
  })
  it("shows empty task and mission activity without inventing log entries", () => {
    const view = render(<ContextDetailPanel context={taskContext()} />)
    tab("Logs"); expect(screen.getByText("No log entries yet")).toBeInTheDocument()
    view.rerender(<ContextDetailPanel context={{ type: "mission", mission: mission("empty") }} />)
    expect(screen.getByText("0/0 (0%)")).toBeInTheDocument()
    tab("Logs"); expect(screen.getByText("No tasks in this mission")).toBeInTheDocument()
    tab("Trace"); expect(screen.getByText("No tasks to trace")).toBeInTheDocument()
    view.rerender(<ContextDetailPanel context={{ type: "mission", mission: mission("pending", { tasks: [task("unstarted")] }) }} />)
    tab("Logs"); expect(screen.getByText("No activity yet")).toBeInTheDocument()
  })
  it("counts mission completion and sorts aggregate activity chronologically", () => {
    const tasks = [task("last", { task_order: 2, agent_slug: null, error_message: "Failed late", completed_at: ago(1) }), task("first", { task_order: 1, status: "COMPLETED", started_at: ago(10), completed_at: ago(5), duration_ms: 1500 })]
    render(<ContextDetailPanel context={{ type: "mission", mission: mission("m", { status: "DONE", complexity: "MEDIUM", pattern: "PARALLEL", tasks, total_token_count: 1500, total_estimated_cost: 0.5 }) }} />)
    expect(screen.getByText("1/2 (50%)")).toBeInTheDocument(); expect(screen.getByText("1.5k")).toBeInTheDocument(); expect(screen.getByText("$0.5000")).toBeInTheDocument(); expect(screen.getByText("PARALLEL")).toBeInTheDocument()
    tab("Logs")
    const panel = screen.getByRole("tabpanel")
    expect(panel.textContent).toMatch(/Started: Task first.*Completed: Task first \(1.5s\).*\[\?\] Failed late/s)
    expect(tasks.map(t => t.id)).toEqual(["last", "first"])
  })
  it("records a completed mission task even when its start and summary are absent", () => {
    render(<ContextDetailPanel context={{ type: "mission", mission: mission("m", { tasks: [task("recovered", { status: "COMPLETED", completed_at: ago(1), agent_slug: null })] }) }} />)
    tab("Logs"); expect(screen.getByText("[?] Completed: Task recovered")).toBeInTheDocument()
  })
  it("keeps the selected tab when data for the same entity refreshes, and resets for another entity", () => {
    const view = render(<ContextDetailPanel context={taskContext()} />)
    tab("Logs")
    view.rerender(<ContextDetailPanel context={taskContext(task("one", { result_summary: "Fresh output" }))} />)
    expect(screen.getByRole("tab", { name: "Logs" })).toHaveAttribute("aria-selected", "true")
    expect(screen.getByText("Fresh output")).toBeInTheDocument()
    view.rerender(<ContextDetailPanel context={taskContext(task("two"))} />)
    expect(screen.getByRole("tab", { name: "Detail" })).toHaveAttribute("aria-selected", "true")
  })
  it("shows only the crew's agents and connections with correct direction", () => {
    render(<ContextDetailPanel context={{ type: "crew", crew: c, agents: [agent("Sam", "crew"), agent("Hidden", "other")], connections: [connection("out", "crew", "Outbound", "unidirectional"), connection("in", "Inbound", "crew", "unidirectional"), connection("both", "crew", "Peer", "bidirectional"), connection("hidden", "one", "two", "bidirectional")] }} />)
    for (const text of ["Quality", "crewship-team-quality", "Sam", "Outbound", "Incoming", "Bidirectional"]) expect(screen.getByText(text)).toBeInTheDocument()
    expect(screen.queryByText("Hidden")).not.toBeInTheDocument(); expect(screen.queryByText("two")).not.toBeInTheDocument()
    tab("Logs"); expect(screen.getByText("Select a task or mission to view logs")).toBeInTheDocument()
  })
  it.each([null, "unknown"])("uses server agent counts and handles crew color %s", color => {
    render(<ContextDetailPanel context={{ type: "crew", crew: { ...c, color, _count: { agents: 12 } }, agents: [], connections: [] }} />)
    expect(screen.getByText("12")).toBeInTheDocument(); expect(screen.queryByText("Connections")).not.toBeInTheDocument()
  })
  it("renders a sorted trace with task metrics and approval-state fallback icons", () => {
    const tasks = [task("second", { task_order: 2, status: "AWAITING_APPROVAL", agent_slug: null }), task("first", { task_order: 1, status: "FAILED", duration_ms: 2500, token_count: 1200, error_message: "Trace error" })]
    render(<ContextDetailPanel context={taskContext(tasks[0], tasks)} />); tab("Trace")
    expect(screen.getByRole("tabpanel").textContent).toMatch(/Task first.*Task second/s)
    for (const text of ["2.5s", "1.2k tok", "Trace error"]) expect(screen.getByText(text)).toBeInTheDocument()
    expect(tasks.map(t => t.id)).toEqual(["second", "first"])
  })
})
