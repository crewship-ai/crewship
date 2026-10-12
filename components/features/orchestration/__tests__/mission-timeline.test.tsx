import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { MissionTimeline } from "../mission-timeline"
import { now, ago, task, mission } from "./mission-fixtures"

beforeEach(() => { vi.useFakeTimers({ toFake: ["Date"] }); vi.setSystemTime(now) })
afterEach(() => { cleanup(); vi.useRealTimers() })

it("shows the empty state until a mission has tasks", () => {
  const view = render(<MissionTimeline missions={[]} />)
  expect(screen.getByText("No timeline data")).toBeVisible()
  view.rerender(<MissionTimeline missions={[mission("empty")]} />)
  expect(screen.getByText("No timeline data")).toBeVisible()
  expect(screen.queryByText("Mission empty")).not.toBeInTheDocument()
})

it("retains the real agent count when a mission is collapsed", () => {
  render(<MissionTimeline missions={[mission("m", { tasks: [task("a"), task("b", { agent_slug: "alex" }), task("c", { agent_slug: null })] })]} />)
  const heading = screen.getByRole("button", { name: /Mission m/ })
  expect(heading).toHaveTextContent("3 agents / 3 tasks")
  fireEvent.click(heading)
  expect(heading).toHaveTextContent("3 agents / 3 tasks")
  fireEvent.click(heading)
  expect(heading).toHaveTextContent("3 agents / 3 tasks")
})

it("announces expansion and preserves independent mission sections", () => {
  render(<MissionTimeline missions={[mission("first", { tasks: [task("a")] }), mission("second", { tasks: [task("b", { agent_slug: "robin" })] })]} />)
  const first = screen.getByRole("button", { name: /Mission first/ })
  expect(first).toHaveAttribute("aria-expanded", "true")
  fireEvent.click(first)
  expect(first).toHaveAttribute("aria-expanded", "false")
  expect(screen.queryByText("@sam")).not.toBeInTheDocument()
  expect(screen.getByText("@robin")).toBeVisible()
  fireEvent.click(first)
  expect(screen.getByText("@sam")).toBeVisible()
})

it("groups tasks by agent, sorts task order, and distinguishes active, done and idle lanes", () => {
  const tasks = [
    task("later", { task_order: 2, started_at: ago(200) }),
    task("first", { task_order: 1, started_at: ago(180), agent_slug: "sam" }),
    task("done", { agent_slug: "robin", status: "COMPLETED", started_at: ago(150), completed_at: ago(20) }),
    task("idle", { agent_slug: null, status: "PENDING" }),
  ]
  render(<MissionTimeline missions={[mission("m", { tasks, created_at: ago(300) })]} highlightSlugs={new Set(["sam"])} />)
  for (const text of ["active", "done", "idle", "@sam", "@robin", "@unassigned"]) expect(screen.getByText(text)).toBeVisible()
  const samLane = screen.getByText("@sam").closest('[style*="min-width: 900"]')
  const robinLane = screen.getByText("@robin").closest('[style*="min-width: 900"]')
  expect(samLane).not.toHaveClass("opacity-20")
  expect(robinLane).toHaveClass("opacity-20")
  expect(within(samLane as HTMLElement).getAllByText(/^Task /).map((node) => node.textContent)).toEqual(["Task first", "Task later"])
  expect(tasks.map((item) => item.id)).toEqual(["later", "first", "done", "idle"])
})

it("provides keyboard access to detailed timing and usage for a task", async () => {
  render(<MissionTimeline missions={[mission("m", { created_at: ago(120), tasks: [task("report", { status: "COMPLETED", started_at: ago(100), completed_at: ago(10), duration_ms: 90000, token_count: 1234, estimated_cost: 0.1234 })] })]} />)
  const bar = screen.getByRole("group", { name: "Task: Task report" })
  expect(bar).toHaveAttribute("tabindex", "0")
  fireEvent.focus(bar)
  const tooltip = await screen.findByRole("tooltip")
  for (const label of ["Status", "COMPLETED", "Start", "End", "Duration", "Tokens", "Cost", "$0.1234"]) expect(tooltip).toHaveTextContent(label)
  expect(tooltip).toHaveTextContent((1234).toLocaleString())
})

it("handles tasks without timing/usage and derives completed duration when it is not supplied", async () => {
  render(<MissionTimeline missions={[mission("m", { tasks: [
    task("waiting", { status: "AWAITING_APPROVAL", agent_slug: null }),
    task("finished", { status: "COMPLETED", started_at: ago(20), completed_at: ago(10) }),
  ] })]} />)
  fireEvent.focus(screen.getByRole("group", { name: "Task: Task waiting" }))
  let tooltip = await screen.findByRole("tooltip")
  expect(tooltip).toHaveTextContent("AWAITING_APPROVAL")
  for (const label of ["Start", "End", "Duration", "Tokens", "Cost"]) expect(tooltip).not.toHaveTextContent(label)
  fireEvent.blur(screen.getByRole("group", { name: "Task: Task waiting" }))
  fireEvent.focus(screen.getByRole("group", { name: "Task: Task finished" }))
  tooltip = await screen.findByRole("tooltip")
  expect(tooltip).toHaveTextContent("Duration")
  expect(tooltip).toHaveTextContent("10s")
})

it("keeps chart positions finite for simultaneous events and extends to future completion", () => {
  const view = render(<MissionTimeline missions={[mission("same", { created_at: ago(0), updated_at: ago(0), tasks: [task("same", { created_at: ago(0), status: "PENDING" })] })]} />)
  let bar = screen.getByRole("group", { name: "Task: Task same" })
  expect(Number.parseFloat(bar.style.left)).toBeGreaterThanOrEqual(0)
  expect(Number.parseFloat(bar.style.width)).toBeGreaterThan(0)
  view.rerender(<MissionTimeline missions={[mission("future", { updated_at: ago(-120), tasks: [task("future", { started_at: ago(100), completed_at: ago(-200), status: "COMPLETED" })] })]} />)
  bar = screen.getByRole("group", { name: "Task: Task future" })
  expect(Number.parseFloat(bar.style.left) + Number.parseFloat(bar.style.width)).toBeLessThanOrEqual(100)
})

it("renders only the ten most recently updated missions with tasks", () => {
  const missions = Array.from({ length: 12 }, (_, i) => mission(String(i), { updated_at: ago(12 - i), tasks: [task(String(i))] }))
  render(<MissionTimeline missions={missions} />)
  const headings = screen.getAllByRole("button", { name: /Mission \d+/ })
  expect(headings).toHaveLength(10)
  expect(headings[0]).toHaveTextContent("Mission 11")
  expect(headings[9]).toHaveTextContent("Mission 2")
  expect(screen.queryByText("Mission 1")).not.toBeInTheDocument()
  expect(missions[0].id).toBe("0")
})
