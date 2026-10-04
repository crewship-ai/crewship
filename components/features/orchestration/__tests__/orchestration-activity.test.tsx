import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { now, ago, task, mission } from "./mission-fixtures"
import { OrchestrationActivity } from "../orchestration-activity"

function row(title: string) {
  const element = screen.getByText(title).closest(".items-stretch")
  if (!(element instanceof HTMLElement)) throw new Error(`Missing event row: ${title}`)
  return element
}
beforeEach(() => { vi.useFakeTimers({ toFake: ["Date"] }); vi.setSystemTime(now) })
afterEach(() => { cleanup(); vi.useRealTimers() })

it("distinguishes an empty feed from an empty filter result", () => {
  const view = render(<OrchestrationActivity missions={[]} />)
  expect(screen.getByText("No activity yet")).toBeVisible()
  expect(screen.queryByRole("button", { name: "Failed" })).not.toBeInTheDocument()
  view.rerender(<OrchestrationActivity missions={[mission("running")]} />)
  fireEvent.click(screen.getByRole("button", { name: "Failed" }))
  expect(screen.getByText("No matching events")).toBeVisible()
  expect(screen.queryByText("No activity yet")).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "All" }))
  expect(screen.getByText("Mission running")).toBeVisible()
})

it("orders completed, started and created events by their applicable timestamp and omits pending tasks", () => {
  render(<OrchestrationActivity missions={[mission("m", { updated_at: ago(90), tasks: [
    task("pending", { status: "PENDING" }),
    task("completed", { status: "COMPLETED", completed_at: ago(2), started_at: ago(500), created_at: ago(1000) }),
    task("started", { started_at: ago(4), created_at: ago(600) }),
    task("created", { created_at: ago(8) }),
  ] })]} />)
  expect(screen.queryByText("Task pending")).not.toBeInTheDocument()
  const titles = Array.from(document.querySelectorAll(".items-stretch .font-medium.truncate")).map((node) => node.textContent)
  expect(titles).toEqual(["Task completed", "Task started", "Task created", "Mission m"])
  expect(within(row("Task completed")).getByText("2s ago")).toBeVisible()
  expect(within(row("Task started")).getByText("4s ago")).toBeVisible()
  expect(within(row("Task created")).getByText("8s ago")).toBeVisible()
  expect(within(row("Mission m")).getByText("1m ago")).toBeVisible()
})

it("groups activity across today, yesterday and earlier dates", () => {
  render(<OrchestrationActivity missions={[
    mission("fresh", { updated_at: ago(30) }), mission("today", { updated_at: ago(7200) }),
    mission("yesterday", { updated_at: ago(86400) }), mission("earlier", { updated_at: ago(259200) }),
  ]} />)
  for (const label of ["Just now", "Today", "Yesterday", "Earlier", "30s ago", "2h ago", "1d ago", "3d ago"]) expect(screen.getByText(label)).toBeVisible()
})

it("renders assignment, iteration and usage without inventing missing values", () => {
  render(<OrchestrationActivity missions={[mission("m", { total_token_count: 1500, total_estimated_cost: 1.234, tasks: [
    task("loop", { iteration: 2, max_iterations: 3, token_count: 42, estimated_cost: 0.01 }),
    task("unassigned", { agent_slug: null, iteration: 1, max_iterations: 1, token_count: 0, estimated_cost: 0, status: "AWAITING_APPROVAL" }),
  ] })]} highlightSlugs={new Set(["sam"])} />)
  expect(row("Mission m")).toHaveTextContent("1.5k tok")
  expect(row("Mission m")).toHaveTextContent("$1.23")
  expect(row("Task loop")).toHaveTextContent("@sam (iter 2/3)")
  expect(row("Task loop")).toHaveTextContent("42 tok")
  expect(row("Task loop")).toHaveTextContent("$0.01")
  expect(row("Task loop")).not.toHaveClass("opacity-20")
  expect(row("Task unassigned")).toHaveTextContent("@unassigned")
  expect(row("Task unassigned")).toHaveClass("opacity-20")
  expect(row("Task unassigned")).not.toHaveTextContent("iter")
  expect(row("Task unassigned")).not.toHaveTextContent("tok")
  expect(row("Task unassigned")).not.toHaveTextContent("$")
  expect(row("Mission m")).not.toHaveClass("opacity-20")
})

it("Done includes canonical DONE missions and legacy COMPLETED tasks", () => {
  render(<OrchestrationActivity missions={[mission("done", { status: "DONE", tasks: [task("done", { status: "COMPLETED" }), task("failed", { status: "FAILED" })] }), mission("legacy", { status: "COMPLETED" }), mission("running")]} />)
  fireEvent.click(screen.getByRole("button", { name: "Done" }))
  expect(screen.getByText("Mission done")).toBeVisible()
  expect(screen.getByText("Mission legacy")).toBeVisible()
  expect(within(row("Mission done")).getByText("DONE")).toHaveClass("text-success")
  expect(screen.getByText("Task done")).toBeVisible()
  expect(screen.queryByText("Mission running")).not.toBeInTheDocument()
  expect(screen.queryByText("Task failed")).not.toBeInTheDocument()
})

it("combines agent and status filters and allows resetting both", async () => {
  render(<OrchestrationActivity missions={[mission("m", { tasks: [task("sam", { status: "FAILED" }), task("alex", { agent_slug: "alex", status: "FAILED" }), task("blocked", { status: "BLOCKED" })] })]} />)
  fireEvent.click(screen.getByRole("button", { name: "Failed" }))
  const trigger = screen.getByRole("combobox")
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false, pointerType: "mouse" })
  fireEvent.click(await screen.findByRole("option", { name: "@sam" }))
  expect(screen.getByText("Task sam")).toBeVisible()
  expect(screen.queryByText("Task alex")).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Blocked" }))
  expect(screen.getByText("Task blocked")).toBeVisible()
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false, pointerType: "mouse" })
  fireEvent.click(await screen.findByRole("option", { name: "All agents" }))
  fireEvent.click(screen.getByRole("button", { name: "Running" }))
  expect(screen.getByText("Mission m")).toBeVisible()
})

it("limits rendering to the latest 100 events without mutating the caller's ordering", () => {
  const missions = Array.from({ length: 105 }, (_, i) => mission(String(i), { updated_at: ago(105 - i) }))
  render(<OrchestrationActivity missions={missions} />)
  expect(document.querySelectorAll(".items-stretch")).toHaveLength(100)
  expect(screen.getByText("Mission 104")).toBeVisible()
  expect(screen.getByText("Mission 5")).toBeVisible()
  expect(screen.queryByText("Mission 4")).not.toBeInTheDocument()
  expect(missions[0].id).toBe("0")
})
