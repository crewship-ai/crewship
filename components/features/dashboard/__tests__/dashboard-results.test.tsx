import { describe, it, expect, vi } from "vitest"
import { render, screen, cleanup, fireEvent, within } from "@testing-library/react"
import { afterEach } from "vitest"
import { DashboardResults } from "../dashboard-results"
import { RunningNow } from "../dashboard-overview"
import { fleetAgentStatus } from "../fleet-board"
import type { AgentSummary } from "@/app/(dashboard)/dashboard-types"
import type { Mission } from "@/lib/types/mission"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"

vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: ({ alt }: { alt: string }) => <span>{alt}</span> }))
afterEach(cleanup)
const base = { review: [], inProgress: [], completed: [], activeAgentRuns: [], activeRoutineRuns: [], recentRoutineRuns: [], agents: [], crews: [], workspaceId: "ws", loading: false, error: false, routineError: null, routineLoading: false, onRetry: vi.fn() }

describe("client dashboard results", () => {
  it("opens the exact review issue and excludes failed routine outputs", () => {
    render(<DashboardResults {...base} review={[{ id: "1", identifier: "ENG-6", title: "Review design", status: "REVIEW", updated_at: new Date().toISOString() } as Mission]} recentRoutineRuns={[{ id: "failed", status: "failed", pipeline_name: "Broken routine" } as PipelineRun, { id: "ok", status: "completed", pipeline_slug: "digest", pipeline_name: "Workspace digest", ended_at: new Date().toISOString() } as PipelineRun]} />)
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
  it("keeps a running execution distinct from its in-progress issue", () => {
    const issue = { id: "issue-1", identifier: "ENG-1", title: "Prepare release", status: "IN_PROGRESS", updated_at: new Date().toISOString() } as Mission
    render(<DashboardResults {...base} inProgress={[issue]} activeAgentRuns={[{
      id: "run-1", kind: "agent", status: "RUNNING", mission_id: "issue-1",
      mission_identifier: "ENG-1", agent_id: "agent-1", agent_name: "Builder",
      created_at: new Date().toISOString(),
    }]} />)
    expect(within(screen.getByRole("region", { name: "Running now" })).getByText("Prepare release")).toBeTruthy()
    expect(within(screen.getByRole("region", { name: "In progress" })).getByText("Prepare release")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: /^Running/ }))
    expect(screen.queryByRole("region", { name: "In progress" })).toBeNull()
  })
})

// A routine on a five-minute schedule filled every row of "Finished recently"
// with the same name (dev3, coolify-ingest ×8). One row per routine, with how
// many runs it stands for and a link to the newest.
describe("finished routine results are grouped per routine", () => {
  const at = (minAgo: number) => new Date(Date.now() - minAgo * 60_000).toISOString()
  const run = (id: string, slug: string, minAgo: number, status = "completed") =>
    ({ id, status, pipeline_slug: slug, pipeline_name: slug, ended_at: at(minAgo) }) as PipelineRun

  it("folds runs of one routine into a single group led by the newest run", async () => {
    const { groupRoutineResults } = await import("../dashboard-results")
    const groups = groupRoutineResults([run("a", "ingest", 4), run("b", "ingest", 9), run("c", "digest", 12), run("d", "ingest", 14)])
    expect(groups.map((g) => [g.latest.id, g.count])).toEqual([["a", 3], ["c", 1]])
  })

  it("ignores runs that did not complete and caps the list at eight routines", async () => {
    const { groupRoutineResults } = await import("../dashboard-results")
    const many = Array.from({ length: 10 }, (_, i) => run(`r${i}`, `routine-${i}`, i))
    expect(groupRoutineResults([run("x", "ingest", 1, "failed"), ...many])).toHaveLength(8)
    expect(groupRoutineResults([run("x", "ingest", 1, "failed")])).toEqual([])
  })

  it("renders one row per routine with its run count", () => {
    render(<DashboardResults {...base} recentRoutineRuns={[run("a", "coolify-ingest", 4), run("b", "coolify-ingest", 9), run("c", "coolify-ingest", 14)]} />)
    const rows = screen.getAllByRole("link", { name: /coolify-ingest/ })
    expect(rows).toHaveLength(1)
    expect(rows[0].getAttribute("href")).toBe("/activity?run=a&pipeline=coolify-ingest")
    expect(rows[0].textContent).toContain("×3")
  })
})
