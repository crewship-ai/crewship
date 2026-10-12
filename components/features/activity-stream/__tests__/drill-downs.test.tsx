/**
 * The drill-downs, at the level only they can be wrong at.
 *
 * These are the pages a lens row leads to, and every one of them is handed a
 * reference by the shell and has to find the thing again. That handover is the
 * part that can be wrong — a page that renders beautifully from the wrong key
 * shows "nothing reached it" over an issue with four workflows on it, which is
 * indistinguishable from the truth.
 */

import * as React from "react"
import { fireEvent, render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import type { ChainSummary } from "@/hooks/use-chains"

// RunActivityTimeline and the run-records hook own their own tests and their
// own requests; drawing them here would assert those files.
vi.mock("@/components/features/activity/run-activity-timeline", () => ({
  RunActivityTimeline: () => <div data-testid="run-timeline" />,
}))
vi.mock("@/hooks/use-pipeline-run-records", () => ({
  usePipelineRunRecords: () => ({ records: [], loading: false, error: null, legacy: false, refresh: vi.fn() }),
}))

// The issue's own timeline is read by issue through its hook (#2983); the
// page's job is to render what the hook returns, whatever the chains hold.
const timeline = vi.hoisted(() => ({ state: null as null | Record<string, unknown>, loadOlder: vi.fn() }))
vi.mock("@/hooks/use-issue-timeline", () => ({
  humanAction: (a: string) => a,
  useIssueTimeline: (_ws: string, issueId: string) =>
    timeline.state ?? {
      issue: issueId === "msn_1" ? { id: "msn_1", identifier: "ENG-7", title: "Ship the thing", crew_id: "c1", status: "TODO" } : { id: issueId, identifier: null, title: "A workspace that does not use identifiers", crew_id: "c1" },
      error: null,
      items: [],
      loading: false,
      canLoadOlder: false,
      busy: false,
      loadOlder: timeline.loadOlder,
    },
}))

import { AgentDrillDown, IssueDrillDown } from "../drill-downs"

const chain = (over: Partial<ChainSummary> = {}): ChainSummary => ({
  origin: "run_a",
  started_by_kind: "automation",
  started_by: "on issue closed",
  runs: 1,
  max_chain_depth: 0,
  failed_runs: 0,
  failed: false,
  first_activity: "2026-08-10T10:00:00.000Z",
  last_activity: "2026-08-10T10:00:01.000Z",
  duration_ms: 1000,
  issue_count: 0,
  agent_count: 0,
  ...over,
})

describe("IssueDrillDown", () => {
  // The shell holds a mission ID and a DISPLAY LABEL. It used to hand over the
  // label under a prop named `identifier`, which works only while every issue
  // has an identifier: without one the label is the issue's TITLE, so nothing
  // matched, the page read "Nothing reached it in this window" over an issue
  // with a workflow on it, and the deep link pointed at
  // /issues/<url-encoded title>, which cannot resolve.
  const withIdentifier = chain({
    origin: "run_a",
    issue_count: 1,
    issues: [{ id: "msn_1", identifier: "ENG-7", title: "Ship the thing", created: true }],
  })
  const withoutIdentifier = chain({
    origin: "run_b",
    issue_count: 1,
    issues: [{ id: "msn_2", title: "A workspace that does not use identifiers" }],
  })

  it("finds the chains that touched the issue by its id", () => {
    render(
      <IssueDrillDown
        workspaceId="ws_1"
        issueId="msn_2"
        label="A workspace that does not use identifiers"
        chains={[withoutIdentifier]}
        onOpenWorkflow={vi.fn()}
      />,
    )
    expect(screen.queryByText(/Nothing reached it/i)).toBeNull()
    expect(screen.getByText(/1 workflow/i)).toBeTruthy()
  })

  it("deep-links by identifier when there is one", () => {
    render(
      <IssueDrillDown
        workspaceId="ws_1"
        issueId="msn_1"
        label="ENG-7"
        chains={[withIdentifier]}
        onOpenWorkflow={vi.fn()}
      />,
    )
    const link = screen.getByRole("link", { name: /open issue/i })
    expect(link.getAttribute("href")).toBe("/issues/ENG-7")
  })

  it("offers no deep link it cannot honour", () => {
    // /issues/[identifier] resolves an IDENTIFIER. An issue without one has no
    // reachable URL, and a link to a page that 404s is worse than no link.
    render(
      <IssueDrillDown
        workspaceId="ws_1"
        issueId="msn_2"
        label="A workspace that does not use identifiers"
        chains={[withoutIdentifier]}
        onOpenWorkflow={vi.fn()}
      />,
    )
    expect(screen.queryByRole("link", { name: /open issue/i })).toBeNull()
  })

  it("opens the workflow that reached it", () => {
    const onOpenWorkflow = vi.fn()
    render(
      <IssueDrillDown
        workspaceId="ws_1"
        issueId="msn_1"
        label="ENG-7"
        chains={[withIdentifier]}
        onOpenWorkflow={onOpenWorkflow}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: /created/i }))
    expect(onOpenWorkflow).toHaveBeenCalledWith("run_a")
  })
})

describe("IssueDrillDown — the issue's own history (#2983)", () => {
  it("shows the history read by issue even when no loaded chain touched it", () => {
    timeline.state = {
      issue: { id: "msn_9", identifier: "OPS-9", title: "Old issue", crew_id: "c1", status: "DONE" },
      error: null,
      items: [
        { kind: "comment", key: "cm:1", at: "2026-01-02T10:00:00Z", title: "Comment", detail: "Looks good", actor: "Jamie" },
        { kind: "event", key: "ev:1", at: "2026-01-01T10:00:00Z", title: "Status changed", detail: "TODO -> DONE", actor: "Casey" },
      ],
      loading: false,
      canLoadOlder: true,
      busy: false,
      loadOlder: timeline.loadOlder,
    }
    render(<IssueDrillDown workspaceId="ws_1" issueId="msn_9" label="msn_9" chains={[]} onOpenWorkflow={vi.fn()} />)
    const history = screen.getByRole("region", { name: "History" })
    expect(history.textContent).toContain("Looks good")
    expect(history.textContent).toContain("TODO -> DONE")
    // The identifier comes from the issue itself, not from a chain ref.
    expect(screen.getByRole("link", { name: /open issue/i }).getAttribute("href")).toBe("/issues/OPS-9")
    fireEvent.click(screen.getByRole("button", { name: /load older/i }))
    expect(timeline.loadOlder).toHaveBeenCalled()
    timeline.state = null
  })

  it("opens a run from the history", () => {
    timeline.state = {
      issue: { id: "msn_9", identifier: "OPS-9", title: "Old issue", crew_id: "c1" },
      error: null,
      items: [{ kind: "run", key: "run:a1", at: "2026-01-02T10:00:00Z", title: "Fix the bug", action: "completed", runId: "run_77", actor: "Ada" }],
      loading: false,
      canLoadOlder: false,
      busy: false,
      loadOlder: timeline.loadOlder,
    }
    const onOpenRun = vi.fn()
    render(<IssueDrillDown workspaceId="ws_1" issueId="msn_9" label="OPS-9" chains={[]} onOpenWorkflow={vi.fn()} onOpenRun={onOpenRun} />)
    fireEvent.click(screen.getByRole("button", { name: /Fix the bug/ }))
    expect(onOpenRun).toHaveBeenCalledWith("run_77")
    timeline.state = null
  })

  it("says the issue is unavailable rather than showing an empty history", () => {
    timeline.state = { issue: null, error: "This issue is not available.", items: [], loading: false, canLoadOlder: false, busy: false, loadOlder: vi.fn() }
    render(<IssueDrillDown workspaceId="ws_1" issueId="msn_x" label="msn_x" chains={[]} onOpenWorkflow={vi.fn()} />)
    expect(screen.getByText("This issue is not available.")).toBeTruthy()
    timeline.state = null
  })
})

describe("AgentDrillDown", () => {
  it("counts a ref with no assignment count as one piece of work", () => {
    // The rail's row read "×1" for this agent and the strip on the page the row
    // led to read "0 assignments" — one agent, one window, two numbers, because
    // three files each decided the fallback for themselves. assignmentsOf is
    // now the one place that decides it.
    render(
      <AgentDrillDown
        workspaceId="ws_1"
        agentID="agt_1"
        name="Ada"
        chains={[chain({ agent_count: 1, agents: [{ id: "agt_1", name: "Ada", assignments: 0 }] })]}
        onOpenWorkflow={vi.fn()}
      />,
    )
    // The strip's Assignments cell, not the row's "×1".
    expect(screen.getByText("Assignments").parentElement?.textContent).toContain("1")
  })
})
