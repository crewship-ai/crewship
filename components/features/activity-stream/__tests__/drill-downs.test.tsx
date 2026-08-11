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
