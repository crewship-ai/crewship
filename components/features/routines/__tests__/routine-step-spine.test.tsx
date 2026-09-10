// The one step list, in both of its modes.
//
// The claims that must not drift:
//
//   recipe mode says what a step will do and never invents a state;
//   run mode reports only what the record actually holds — a live current
//   step, a stored execution, or a finished step that stored no response —
//   and stays silent on the ordinary ones, because a column of ten identical
//   labels tells the reader nothing about which row to look at;
//   an unloadable step-output blob stays distinguishable from an empty one;
//   and selecting a node on the map returns to the list with that step open,
//   rather than opening a second detail panel beside the graph.

import * as React from "react"
import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"

import { RoutineStepSpine, type StepSpineRecord } from "../routine-step-spine"
import type { StepExecutionSummary, RunExecution } from "@/hooks/use-run-executions"

vi.mock("@/hooks/use-workspace-agent-directory", () => ({
  useWorkspaceAgentDirectory: () => ({ agents: [], error: false }),
}))
vi.mock("next/link", () => ({
  default: ({ children, href }: { children: React.ReactNode; href: string }) => (
    <a href={href}>{children}</a>
  ),
}))

const definition = {
  steps: [
    { id: "probe", type: "script", script: { path: "scripts/probe.py" } },
    { id: "triage", type: "agent_run", agent_slug: "morgan", prompt: "Read the probe output" },
    { id: "post", type: "notify", if: "steps.triage.ok" },
  ],
}

const execution = (over: Partial<RunExecution>): StepExecutionSummary => {
  const row: RunExecution = {
    id: "x1",
    parent_execution_id: "",
    step_id: "probe",
    execution_path: "/probe",
    attempt: 1,
    kind: "step",
    status: "completed",
    agent_slug: "",
    model: "",
    started_at: "2026-09-08T10:00:00Z",
    ended_at: "2026-09-08T10:00:02Z",
    error: "",
    output_bytes: 12,
    ...over,
  }
  return {
    rows: [row],
    latest: row,
    attempts: row.attempt,
    durationMs: 2000,
    error: row.error || undefined,
  }
}

const runRecord = (
  over: Partial<NonNullable<React.ComponentProps<typeof RoutineStepSpine>["record"]>> = {},
) => ({
  lookup: (): StepSpineRecord => ({ hasOutput: true, output: "recorded" }),
  outputsAvailable: true,
  active: false,
  ...over,
})

describe("routine step spine", () => {
  it("describes the saved recipe without claiming any state", () => {
    render(<RoutineStepSpine definition={definition} />)
    expect(screen.getByText("What this routine does")).toBeInTheDocument()
    expect(screen.getByText("Probe")).toBeInTheDocument()
    expect(screen.getAllByText(/Run a script/).length).toBeGreaterThan(0)
    expect(screen.getByText("Only if")).toBeInTheDocument()
    expect(screen.queryByText(/No response recorded/)).not.toBeInTheDocument()
    expect(screen.queryByText("Done")).not.toBeInTheDocument()
  })

  it("reports a recorded execution and stays silent on the ordinary steps", () => {
    render(
      <RoutineStepSpine
        definition={definition}
        record={runRecord({
          lookup: (stepId: string) =>
            stepId === "probe"
              ? {
                  execution: execution({ status: "failed", error: "exit 7" }),
                  hasOutput: true,
                  output: "boom",
                }
              : { hasOutput: true, output: "ok" },
        })}
      />,
    )
    expect(screen.getByText("What happened, step by step")).toBeInTheDocument()
    expect(screen.getByText("Failed")).toBeInTheDocument()
    expect(screen.getByText("2.0s")).toBeInTheDocument()
    // The two steps that behaved carry no label at all.
    expect(screen.queryByText("Done")).not.toBeInTheDocument()
    expect(screen.queryByText(/No response recorded/)).not.toBeInTheDocument()
  })

  it("flags a finished step that stored nothing, and never calls it skipped", () => {
    render(
      <RoutineStepSpine
        definition={definition}
        record={runRecord({ lookup: (stepId: string) => ({ hasOutput: stepId !== "post" }) })}
      />,
    )
    expect(screen.getAllByText("No response recorded")).toHaveLength(1)
    expect(screen.queryByText(/Skipped/)).not.toBeInTheDocument()
  })

  it("marks the current step of a live run and only while it is live", () => {
    const record = runRecord({ lookup: () => ({ hasOutput: false }), currentStepId: "triage" })
    const { rerender } = render(
      <RoutineStepSpine definition={definition} record={{ ...record, active: true }} />,
    )
    expect(screen.getByText("Current step")).toBeInTheDocument()
    rerender(<RoutineStepSpine definition={definition} record={{ ...record, active: false }} />)
    expect(screen.queryByText("Current step")).not.toBeInTheDocument()
  })

  it("keeps an unloadable step-output blob distinct from an empty response", () => {
    render(
      <RoutineStepSpine
        definition={definition}
        record={runRecord({ outputsAvailable: false, lookup: () => ({ hasOutput: false }) })}
      />,
    )
    fireEvent.click(screen.getAllByText("Details")[0])
    expect(screen.getAllByText("Step responses could not be loaded.").length).toBeGreaterThan(0)
    expect(screen.queryByText(/No response was recorded for this step/)).not.toBeInTheDocument()
  })

  it("returns from the map to the list with the selected step open", () => {
    render(
      <RoutineStepSpine
        definition={definition}
        map={({ openStep }) => (
          <button type="button" onClick={() => openStep("triage")}>
            pick triage
          </button>
        )}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "map", exact: true }))
    fireEvent.click(screen.getByRole("button", { name: "pick triage" }))
    expect(screen.queryByRole("button", { name: "pick triage" })).not.toBeInTheDocument()
    const open = document.querySelectorAll("details[open]")
    expect(open).toHaveLength(1)
    expect(open[0].textContent).toContain("Triage")
  })
})
