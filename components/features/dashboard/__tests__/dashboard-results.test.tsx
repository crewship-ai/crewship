import { describe, it, expect, vi } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"
import { afterEach } from "vitest"
import { DashboardResults } from "../dashboard-results"
import { RunningNow } from "../dashboard-overview"
import { fleetAgentStatus } from "../fleet-board"
import type { AgentSummary } from "@/app/(dashboard)/dashboard-types"
import type { Mission } from "@/lib/types/mission"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"

vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: ({ alt }: { alt: string }) => <span>{alt}</span> }))
afterEach(cleanup)
const base = { review: [], completed: [], runs: [], agents: [], crews: [], workspaceId: "ws", loading: false, error: false, routineError: null, routineLoading: false, onRetry: vi.fn() }

describe("client dashboard results", () => {
  it("opens the exact review issue and excludes failed routine outputs", () => {
    render(<DashboardResults {...base} review={[{ id: "1", identifier: "ENG-6", title: "Review design", status: "REVIEW", updated_at: new Date().toISOString() } as Mission]} runs={[{ id: "failed", status: "failed", pipeline_name: "Broken routine" } as PipelineRun, { id: "ok", status: "completed", pipeline_slug: "digest", pipeline_name: "Workspace digest", ended_at: new Date().toISOString() } as PipelineRun]} />)
    expect(screen.getByRole("link", { name: /Review design/ }).getAttribute("href")).toBe("/issues/ENG-6")
    expect(screen.getByRole("link", { name: /Workspace digest/ }).getAttribute("href")).toBe("/activity?run=ok&pipeline=digest")
    expect(screen.queryByText("Broken routine")).toBeNull()
  })
  it("shows the human owner instead of their delegated agent", () => {
    render(<DashboardResults {...base} agents={[{ id: "agent", name: "Delegate", slug: "delegate" } as AgentSummary]} review={[{ id: "owned", title: "Human-owned work", status: "REVIEW", owner: { id: "human", name: "Alice" }, delegate: { id: "agent", name: "Delegate" }, lead_agent_id: "agent", assignee_type: "agent", assignee_id: "agent", updated_at: new Date().toISOString() } as Mission]} />)
    expect(screen.getByRole("link", { name: /Human-owned work/ }).textContent).toContain("Alice")
    expect(screen.queryByText(/Delegate/)).toBeNull()
  })
  it("does not call failed or unavailable agents ready", () => {
    expect(fleetAgentStatus([{ status: "ERROR" }])).toBe("1 in error")
    expect(fleetAgentStatus([{ status: "RUNNING" }, { status: "ERROR" }, { status: "IDLE" }])).toBe("1 running · 1 ready · 1 in error")
    expect(fleetAgentStatus([])).toBe("no agents yet")
  })
  it("does not describe a failed fetch as an empty workspace", () => {
    render(<DashboardResults {...base} error />)
    expect(screen.getByRole("status").textContent).toContain("could not refresh")
    expect(screen.queryByText(/Finished work will appear/)).toBeNull()
    screen.getByRole("button", { name: "Retry" }).click()
    expect(base.onRetry).toHaveBeenCalledOnce()
  })
  it("separates approval and queue from running routines", () => {
    render(<RunningNow agents={[]} crews={[]} runs={[{ id: "waiting", status: "waiting" }, { id: "queued", status: "queued" }] as PipelineRun[]} />)
    expect(screen.getByText("No routines are running right now.")).toBeTruthy()
    expect(screen.getByText(/1 awaiting approval/)).toBeTruthy()
    expect(screen.getByText("1 queued")).toBeTruthy()
    expect(screen.queryByText(/2 active/)).toBeNull()
  })
  it("does not assert idle when activity could not load", () => {
    render(<RunningNow agents={[]} crews={[]} runs={[]} error="offline" />)
    expect(screen.queryByText("No routines are running right now.")).toBeNull()
    expect(screen.getByText(/Could not refresh routine activity/)).toBeTruthy()
  })
})
