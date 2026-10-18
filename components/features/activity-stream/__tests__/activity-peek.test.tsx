import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { ChainSummary } from "@/hooks/use-chains"
import type { JournalEntry } from "@/lib/types/journal"
import { changeFromEntry, workFromChain, type WorkItem } from "@/lib/activity-feed"

const mock = vi.hoisted(() => ({ fetch: vi.fn(), records: vi.fn(), mobile: false }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: mock.fetch }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mock.mobile }))
vi.mock("@/hooks/use-pipeline-run-records", () => ({ usePipelineRunRecords: mock.records }))
vi.mock("@/lib/toast", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

import { ActivityPeek, type ActivityPeekProps } from "../activity-peek"

const NOW = new Date(2026, 9, 15, 8, 16).getTime()

const chain = (over: Partial<ChainSummary> = {}): ChainSummary => ({
  origin: "run_418",
  started_by_kind: "webhook",
  started_by: "GitHub",
  runs: 1,
  max_chain_depth: 1,
  failed_runs: 0,
  failed: false,
  first_activity: new Date(2026, 9, 15, 8, 14).toISOString(),
  last_activity: new Date(2026, 9, 15, 8, 15).toISOString(),
  duration_ms: null,
  running_runs: 1,
  issue_count: 1,
  issues: [{ id: "m142", identifier: "CRW-142", title: "Fix flaky login test", created: true }],
  agent_count: 1,
  agents: [{ id: "a1", name: "Reviewer", assignments: 1 }],
  routine_slug: "review-prs",
  ...over,
})

const records = [
  { id: "run_416", status: "completed", started_at: new Date(2026, 9, 14, 9).toISOString(), duration_ms: 200_000 },
  { id: "run_417", status: "failed", started_at: new Date(2026, 9, 14, 12).toISOString(), duration_ms: 60_000 },
  { id: "run_418", status: "running", started_at: new Date(2026, 9, 15, 8, 14).toISOString(), duration_ms: 0 },
]

function props(item: ActivityPeekProps["item"], over: Partial<ActivityPeekProps> = {}): ActivityPeekProps {
  return {
    workspaceId: "ws",
    item,
    onClose: vi.fn(),
    now: NOW,
    role: "ADMIN",
    routineName: (s) => (s === "review-prs" ? "Review pull requests" : undefined),
    onOpenFull: vi.fn(),
    onPickRun: vi.fn(),
    onShowRoutine: vi.fn(),
    onShowIssue: vi.fn(),
    onOpenRecord: vi.fn(),
    onChanged: vi.fn(),
    ...over,
  }
}

const work = (over: Partial<ChainSummary> = {}): WorkItem => workFromChain(chain(over), { routineName: () => "Review pull requests" })

beforeEach(() => {
  vi.clearAllMocks()
  mock.mobile = false
  mock.records.mockReturnValue({ records })
  mock.fetch.mockResolvedValue(new Response("{}", { status: 200 }))
})
afterEach(() => cleanup())

describe("ActivityPeek · work", () => {
  it("routine run Peek shows How it connects, Details, Earlier runs, Stop when running", async () => {
    const p = props(work())
    render(<ActivityPeek {...p} />)
    const peek = await screen.findByTestId("activity-peek-work")
    expect(peek.querySelector("[data-slot=status-pill]")).toHaveTextContent("Running")
    // How it connects: the webhook → the run → the agent → the issue it created.
    const how = within(peek).getByRole("heading", { name: "How it connects" }).parentElement!
    const steps = within(how).getAllByRole("listitem").map((li) => li.textContent)
    expect(steps[0]).toContain("GitHub")
    expect(steps[1]).toContain("Review pull requests")
    expect(steps[2]).toContain("Reviewer worked on it")
    expect(steps[3]).toContain("CRW-142 Fix flaky login test")
    expect(steps[3]).toContain("created by this work")
    // Details.
    expect(within(peek).getByText("Done by")).toBeVisible()
    expect(within(peek).getByText("still running")).toBeVisible()
    // Earlier runs: three bars, 1 of 2 finished completed.
    expect(within(peek).getByRole("heading", { name: "Earlier runs of Review pull requests" })).toBeVisible()
    expect(within(peek).getByRole("list", { name: "Earlier runs" }).children).toHaveLength(3)
    expect(within(peek).getByText(/50 % completed/)).toBeVisible()
    // Stop for an admin while it runs; no Run again.
    expect(screen.getByRole("button", { name: "Stop" })).toBeVisible()
    expect(screen.queryByRole("button", { name: "Run again" })).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Stop" }))
    fireEvent.click(await screen.findByRole("button", { name: "Stop run" }))
    await waitFor(() => expect(mock.fetch).toHaveBeenCalledWith("/api/v1/workspaces/ws/pipelines/runs/run_418/cancel", { method: "POST" }))
    expect(p.onChanged).toHaveBeenCalled()
  })

  // RT-07: a routine run in Activity opens the routine run peek: version and
  // cost included, Open run goes to the page; Activity's own sections stay.
  it("a routine run opens the routine run peek with version and cost", async () => {
    mock.records.mockReturnValue({
      records: records.map((r) => (r.id === "run_418" ? { ...r, pipeline_version: 4, cost_usd: 0.31 } : r)),
    })
    const p = props(work())
    render(<ActivityPeek {...p} />)
    const peek = await screen.findByTestId("run-peek")
    expect(within(peek).getByText("Version").nextElementSibling).toHaveTextContent("v4")
    expect(within(peek).getByText("Cost").nextElementSibling).toHaveTextContent("$0.31")
    // Activity's own sections and actions live inside the same peek.
    expect(within(peek).getByRole("heading", { name: "How it connects" })).toBeVisible()
    expect(screen.getByRole("button", { name: "Stop" })).toBeVisible()
    fireEvent.click(screen.getByRole("button", { name: "Open run" }))
    expect(p.onOpenFull).toHaveBeenCalledWith(expect.objectContaining({ id: "run_418" }))
  })

  it("hides Stop from a member, whom the server would refuse", async () => {
    render(<ActivityPeek {...props(work(), { role: "MEMBER" })} />)
    await screen.findByTestId("activity-peek-work")
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull()
  })

  it("offers Run again on a finished run to a manager", async () => {
    render(<ActivityPeek {...props(work({ running_runs: 0, duration_ms: 90_000 }), { role: "MANAGER" })} />)
    await screen.findByTestId("activity-peek-work")
    expect(screen.getByRole("button", { name: "Run again" })).toBeVisible()
  })

  it("Open run opens the full page; All runs narrows to the routine", async () => {
    const p = props(work())
    render(<ActivityPeek {...p} />)
    await screen.findByTestId("activity-peek-work")
    fireEvent.click(screen.getByRole("button", { name: "Open run" }))
    expect(p.onOpenFull).toHaveBeenCalledWith(expect.objectContaining({ id: "run_418" }))
    fireEvent.click(screen.getByRole("button", { name: /All runs/ }))
    expect(p.onShowRoutine).toHaveBeenCalledWith("review-prs")
  })

  it("assignment chain renders as ordered How it connects with status icons", async () => {
    const item = work({ kind: "assignment", task: "Fix flaky login test", routine_slug: undefined, started_by_kind: "issue", started_by: "CRW-142" })
    render(<ActivityPeek {...props(item)} />)
    const peek = await screen.findByTestId("activity-peek-work")
    expect(screen.getByRole("button", { name: "Open work" })).toBeVisible()
    const how = within(peek).getByRole("heading", { name: "How it connects" }).parentElement!
    expect(within(how).getAllByRole("img", { name: "Running" })).toHaveLength(1)
    // No routine: no Earlier runs and no Stop (agent work is stopped from its page).
    expect(within(peek).queryByText(/Earlier runs/)).toBeNull()
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull()
  })
})

describe("ActivityPeek · change", () => {
  it("change Peek shows before → after and Open full record", async () => {
    const entry: JournalEntry = {
      id: "e1",
      workspace_id: "ws",
      ts: new Date(2026, 9, 15, 6, 58).toISOString(),
      entry_type: "mission.status_change",
      severity: "info",
      actor_type: "user",
      summary: "CRW-148 moved to In review",
      mission_id: "m148",
      payload: { from: "In progress", to: "In review" },
    }
    const item = changeFromEntry(entry, { issueLabel: () => "CRW-148 Rotate staging certificates" })
    const p = props(item)
    render(<ActivityPeek {...p} />)
    const peek = await screen.findByTestId("activity-peek-change")
    expect(within(peek).getByText("Status changed")).toBeVisible()
    expect(within(peek).getByText("In progress")).toHaveClass("line-through")
    expect(within(peek).getByText("In review")).toBeVisible()
    fireEvent.click(screen.getByRole("button", { name: "Open full record" }))
    expect(p.onOpenRecord).toHaveBeenCalledWith(item)
    fireEvent.click(screen.getByRole("button", { name: "Everything about this issue" }))
    expect(p.onShowIssue).toHaveBeenCalledWith("m148")
  })
})
