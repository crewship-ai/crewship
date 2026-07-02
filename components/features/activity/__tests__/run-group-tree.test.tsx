import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"
import { RunGroupTree } from "../rail/run-group-tree"
import type { RunGroup } from "@/lib/activity/run-filters"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"

// next/link → plain anchor so href assertions are trivial in jsdom.
vi.mock("next/link", () => ({
  default: ({ href, children, ...rest }: any) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}))

function mkRun(partial: Partial<PipelineRun>): PipelineRun {
  return {
    id: "run_cmr3b05zh000db371933a",
    pipeline_id: "prn_1",
    pipeline_slug: "classify-ticket",
    pipeline_name: "Classify support ticket",
    status: "completed",
    mode: "live",
    started_at: "2026-07-02T09:00:00Z",
    ended_at: "2026-07-02T09:00:05Z",
    current_step_id: "",
    step_outputs: null,
    cost_usd: 0,
    duration_ms: 5000,
    triggered_via: "schedule",
    triggered_by_id: "",
    invoking_crew_id: "",
    invoking_agent_id: "",
    invoking_user_id: "",
    error_message: "",
    failed_at_step: "",
    issue_identifier: "",
    ...partial,
  }
}

function issueGroup(runs: PipelineRun[]): RunGroup {
  return { key: "noissue", label: "Without an issue", kind: "all", totalRuns: runs.length, runs }
}

function routineGroup(name: string, runs: PipelineRun[]): RunGroup {
  return { key: "rg", label: name, kind: "routine", totalRuns: runs.length, runs }
}

describe("RunGroupTree — run rows carry routine context", () => {
  it("shows the routine name on a run row when the parent group isn't a routine", () => {
    render(
      <RunGroupTree
        groups={[issueGroup([mkRun({})])]}
        selectedRunId={null}
        onSelectRun={() => {}}
      />,
    )
    // The superordinate routine is visible on the row itself, not just a raw ID.
    expect(screen.getByText("Classify support ticket")).toBeInTheDocument()
  })

  it("links each run row through to its routine definition", () => {
    render(
      <RunGroupTree
        groups={[issueGroup([mkRun({ pipeline_slug: "consistency-sweep", pipeline_name: "Consistency sweep" })])]}
        selectedRunId={null}
        onSelectRun={() => {}}
      />,
    )
    const link = screen.getByRole("link", { name: /consistency sweep/i })
    expect(link).toHaveAttribute("href", "/routines?slug=consistency-sweep")
  })

  it("renders no routine link when the run has no slug", () => {
    render(
      <RunGroupTree
        groups={[issueGroup([mkRun({ pipeline_slug: "", pipeline_name: "" })])]}
        selectedRunId={null}
        onSelectRun={() => {}}
      />,
    )
    expect(screen.queryByRole("link")).not.toBeInTheDocument()
  })

  it("does NOT repeat the routine name on rows inside a routine group", () => {
    render(
      <RunGroupTree
        groups={[routineGroup("Consistency sweep", [mkRun({ pipeline_slug: "consistency-sweep", pipeline_name: "Consistency sweep" })])]}
        selectedRunId={null}
        onSelectRun={() => {}}
      />,
    )
    // The routine name appears once — in the group header — not again on the
    // row (showRoutine=false). The row falls back to the short run id.
    expect(screen.getAllByText("Consistency sweep")).toHaveLength(1)
    expect(screen.getByText("run_cmr3b0")).toBeInTheDocument()
    // The deep-link is still present on the row.
    expect(screen.getByRole("link", { name: /consistency sweep/i })).toHaveAttribute(
      "href",
      "/routines?slug=consistency-sweep",
    )
  })
})
