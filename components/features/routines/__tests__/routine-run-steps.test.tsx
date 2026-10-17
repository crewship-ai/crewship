// RT-RUN step list + inspector (routines.md R13, RT-06): Output / Input /
// Log / Details for the selected step; on a phone the inspector is its own
// screen with Back.

import * as React from "react"
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, within, cleanup, waitFor } from "@testing-library/react"

const h = vi.hoisted(() => ({ fetcher: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetcher, broadcastSessionExpired: vi.fn() }))

import { RoutineRunSteps } from "../routine-run-steps"

const json = (body: unknown) => ({ ok: true, status: 200, json: async () => body })

const steps = [
  { id: "fetch", type: "http", http: { method: "GET", url: "https://example.com/prices" } },
  { id: "compare", type: "agent_run", agent_slug: "scout", prompt: "Compare {{ steps.fetch.output }} with last week" },
  { id: "approve", type: "wait", wait: { kind: "approval", approval_prompt: "Publish?" } },
] as never[]

const exec = (step_id: string, status: string, started: string, ended: string, over: Record<string, unknown> = {}) => ({
  id: `e-${step_id}`,
  parent_execution_id: "",
  step_id,
  execution_path: step_id,
  attempt: 1,
  kind: "step",
  status,
  agent_slug: "",
  model: "",
  started_at: started,
  ended_at: ended,
  error: "",
  output_bytes: 10,
  ...over,
})
const rows = [
  exec("fetch", "completed", "2026-10-17T08:00:00Z", "2026-10-17T08:00:02Z"),
  exec("compare", "completed", "2026-10-17T08:00:02Z", "2026-10-17T08:02:53Z", { agent_slug: "scout", model: "claude-sonnet", attempt: 2 }),
  exec("approve", "waiting", "2026-10-17T08:02:53Z", ""),
]
const byStep = new Map(rows.map((r) => [r.step_id, { rows: [r], latest: r, attempts: r.attempt, durationMs: undefined }]))

function renderSteps(over: Partial<React.ComponentProps<typeof RoutineRunSteps>> = {}) {
  return render(
    <RoutineRunSteps
      workspaceId="ws"
      runId="run_1"
      steps={steps}
      executions={byStep as never}
      outputs={{ fetch: "200 OK · 14 pages", compare: '{ "changes": 3 }' }}
      runInputs={{ sites: 14 }}
      currentStepId="approve"
      limits={{ costCap: 2, concurrency: "1 at a time" }}
      {...over}
    />,
  )
}

beforeEach(() => {
  h.fetcher.mockReset()
  h.fetcher.mockResolvedValue(
    json([
      { ts: "2026-10-17T08:00:01Z", level: "info", message: "GET example.com/prices" },
      { ts: "2026-10-17T08:01:00Z", level: "info", message: "Scout read 14 snapshots" },
      { ts: "2026-10-17T08:05:00Z", level: "info", message: "Waiting for a person" },
    ]),
  )
})
afterEach(cleanup)

describe("<RoutineRunSteps>", () => {
  it("lists the steps with their state and opens the current one in the inspector", () => {
    renderSteps()
    const list = screen.getByRole("list", { name: "Steps of this run" })
    const items = within(list).getAllByRole("button")
    expect(items.map((b) => b.textContent)).toEqual([
      expect.stringMatching(/fetch.*Completed/i),
      expect.stringMatching(/compare.*Completed/i),
      expect.stringMatching(/approve.*Waiting/i),
    ])
    expect(items[2]).toHaveAttribute("aria-current", "true")
    const inspector = screen.getByRole("region", { name: "Step inspector" })
    const tabs = within(inspector).getByRole("radiogroup", { name: "Inspect" })
    expect(Array.from(tabs.querySelectorAll('[role="radio"]')).map((r) => r.textContent)).toEqual(["Output", "Input", "Log", "Details"])
  })

  it("Output shows what the step produced; a step without one says so", () => {
    renderSteps()
    fireEvent.click(screen.getByRole("button", { name: /compare/i }))
    expect(screen.getByRole("region", { name: "Step inspector" })).toHaveTextContent('"changes": 3')
    fireEvent.click(screen.getByRole("button", { name: /approve/i }))
    expect(screen.getByRole("region", { name: "Step inspector" })).toHaveTextContent("No output recorded for this step")
  })

  it("Input shows the run's inputs and what the step was asked to do", () => {
    renderSteps()
    fireEvent.click(screen.getByRole("button", { name: /compare/i }))
    const inspector = screen.getByRole("region", { name: "Step inspector" })
    fireEvent.click(within(inspector).getByRole("radio", { name: "Input" }))
    expect(inspector).toHaveTextContent('"sites": 14')
    expect(inspector).toHaveTextContent("Compare {{ steps.fetch.output }} with last week")
  })

  it("Log reads the run log once and keeps the lines recorded while the step ran", async () => {
    renderSteps()
    fireEvent.click(screen.getByRole("button", { name: /compare/i }))
    const inspector = screen.getByRole("region", { name: "Step inspector" })
    fireEvent.click(within(inspector).getByRole("radio", { name: "Log" }))
    expect(await within(inspector).findByText(/Scout read 14 snapshots/)).toBeInTheDocument()
    expect(inspector).not.toHaveTextContent("GET example.com/prices")
    expect(inspector).not.toHaveTextContent("Waiting for a person")
    expect(h.fetcher).toHaveBeenCalledWith("/api/v1/workspaces/ws/pipeline-runs/run_1/logs?limit=1000", expect.anything())
    fireEvent.click(screen.getByRole("button", { name: /fetch/i }))
    await waitFor(() => expect(inspector).toHaveTextContent("GET example.com/prices"))
    expect(h.fetcher).toHaveBeenCalledTimes(1)
  })

  it("Details names the step, its agent and model, its attempts and the run's limits", () => {
    renderSteps()
    fireEvent.click(screen.getByRole("button", { name: /compare/i }))
    const inspector = screen.getByRole("region", { name: "Step inspector" })
    fireEvent.click(within(inspector).getByRole("radio", { name: "Details" }))
    expect(inspector).toHaveTextContent("compare")
    expect(inspector).toHaveTextContent("scout")
    expect(inspector).toHaveTextContent("claude-sonnet")
    expect(inspector).toHaveTextContent(/Attempts\s*2/)
    expect(inspector).toHaveTextContent("$2.00")
    expect(inspector).toHaveTextContent("1 at a time")
  })

  it("on a phone the inspector is its own screen with Back to the steps", () => {
    renderSteps({ phone: true, currentStepId: undefined })
    expect(screen.queryByRole("region", { name: "Step inspector" })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /compare/i }))
    expect(screen.getByRole("region", { name: "Step inspector" })).toBeInTheDocument()
    expect(screen.queryByRole("list", { name: "Steps of this run" })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Back to steps" }))
    expect(screen.getByRole("list", { name: "Steps of this run" })).toBeInTheDocument()
    expect(screen.queryByRole("region", { name: "Step inspector" })).not.toBeInTheDocument()
  })
})
