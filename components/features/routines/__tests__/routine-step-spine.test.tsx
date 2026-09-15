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
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

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
  it("shows DAG dependencies on closed rows without claiming execution positions", () => {
    render(
      <RoutineStepSpine
        definition={{
          steps: [
            { id: "join", type: "transform", needs: ["left", "right"] },
            { id: "left", type: "transform" },
            { id: "right", type: "transform" },
          ],
        }}
      />,
    )
    const summary = document.querySelector('[data-step-id="join"] > summary')!
    expect(summary).toHaveTextContent("after Transform data, Transform data")
    expect(screen.getAllByText(/Phases come from declared dependencies/).length).toBeGreaterThan(0)
    expect(summary.querySelector("[data-step-kind]")).not.toHaveTextContent("1")
    // Two phases: the two independent transforms first, the join after them.
    expect(screen.getByTestId("routine-phase-1")).toHaveTextContent("First · 2 in parallel")
    expect(screen.getByTestId("routine-phase-2")).toHaveTextContent("Then")
  })

  it("opens a failed step beyond the initial limit and respects a manual close during polling", async () => {
    const long = {
      steps: Array.from({ length: 16 }, (_, index) => ({
        id: `step_${index}`,
        name: `Action ${index + 1}`,
        type: "transform",
      })),
    }
    const record = runRecord({ failedStepId: "step_15" })
    const view = render(
      <RoutineStepSpine definition={long} record={record} initialLimit={12} />,
    )
    const failed = document.querySelector(
      'details[data-step-id="step_15"]',
    ) as HTMLDetailsElement
    await waitFor(() => expect(failed.open).toBe(true))
    expect(failed.querySelector("summary")).toHaveTextContent("Action 16")
    expect(document.querySelector('[data-step-id="step_13"]')).toBeNull()
    failed.open = false
    fireEvent(failed, new Event("toggle"))
    view.rerender(
      <RoutineStepSpine definition={long} record={{ ...record }} initialLimit={12} />,
    )
    await waitFor(() => expect(failed.open).toBe(false))
  })
  it("describes the saved recipe without claiming any state", () => {
    render(<RoutineStepSpine definition={definition} />)
    expect(screen.getByText("How it works")).toBeInTheDocument()
    expect(document.querySelector('[data-step-id="probe"] > summary')).toHaveTextContent(
      "Run script probe",
    )
    expect(document.querySelector('[data-step-id="triage"] > summary')).toHaveTextContent(
      "Ask morgan",
    )
    expect(screen.getByTestId("routine-step-only")).toHaveTextContent("Only when steps.triage.ok")
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

  it("folds helper transforms, nests a foreach, draws hooks and chips on a complex recipe", () => {
    render(
      <RoutineStepSpine
        slug="docs-drift-audit"
        definition={{
          hooks: {
            before_all: { id: "clean", type: "code", name: "Clean the scratch folder" },
            on_failure: { id: "tell", type: "notify", name: "Tell #docs the audit did not finish", notify: { to: "workspace" } },
          },
          steps: [
            { id: "scan", type: "script", name: "Scan the repository", script: { path: "scripts/docs_audit.sh" }, timeout_seconds: 600 },
            { id: "a", type: "transform", name: "Panel state", needs: ["scan"] },
            { id: "b", type: "transform", name: "Panel label", needs: ["scan"] },
            { id: "c", type: "transform", name: "Commit label", needs: ["scan"] },
            { id: "pairs", type: "transform", name: "Doc pairs", needs: ["scan"] },
            {
              id: "judge_each", type: "foreach", name: "Judge each candidate", needs: ["pairs"],
              foreach: { items: "{{ steps.pairs.output }}", parallelism: 3, steps: [
                { id: "fetch", type: "http", name: "Fetch the doc page", http: { url: "https://github.com" }, retry: { max_attempts: 3 } },
                { id: "judge", type: "agent_run", name: "Decide: real drift?", agent_slug: "jordan", outcomes: { grader_agent_slug: "vale", criteria: [{ name: "a", rule: "a" }, { name: "b", rule: "b" }, { name: "c", rule: "c" }] } },
              ] },
            },
            { id: "post", type: "notify", name: "Post the summary", notify: { to: "workspace" }, needs: ["judge_each"], if: "steps.total.output > 0" },
          ],
        }}
      />,
    )
    expect(screen.getByText(/7 steps \(9 with nested\) · 2 hooks/)).toBeInTheDocument()
    expect(screen.getByTestId("routine-phase-before_all")).toHaveTextContent("Before the run")
    expect(screen.getByTestId("routine-phase-on_failure")).toHaveTextContent("If the run fails")
    expect(document.querySelector('[data-step-id="clean"]')).toHaveAttribute("data-hook", "before_all")
    expect(screen.getByTestId("routine-phase-2")).toHaveTextContent("Then · 4 in parallel")
    expect(screen.getByTestId("routine-fold-2")).toHaveTextContent("4 data preparations from Scan the repository")
    expect(document.querySelector('[data-step-id="a"]')).toBeNull()
    expect(document.querySelector('[data-step-id="fetch"]')).toHaveAttribute("data-nested", "true")
    expect(screen.getByTestId("routine-step-loop")).toHaveTextContent("3 at a time · 2 steps per item")
    const scan = document.querySelector('[data-step-id="scan"] > summary')!
    expect(scan).toHaveTextContent("⏱ 10 min")
    expect(scan.querySelector('[data-testid="routine-step-file"]')).toHaveTextContent("docs_audit.sh")
    expect(document.querySelector('[data-step-id="fetch"] > summary')).toHaveTextContent("3 attempts")
    expect(document.querySelector('[data-step-id="judge"] > summary')).toHaveTextContent("✓ 3 rules · vale")
    expect(document.querySelector('[data-step-id="post"] > summary')).toHaveTextContent("after Judge each candidate")
    expect(screen.getByTestId("routine-step-only")).toHaveTextContent("Only when steps.total.output > 0")
    expect(screen.queryByText(/Show all/)).not.toBeInTheDocument()
  })

  it("caps a long recipe at nine rows and shows the rest on request", () => {
    render(
      <RoutineStepSpine
        definition={{ steps: Array.from({ length: 14 }, (_, i) => ({ id: `s${i}`, type: "http", name: `Call ${i}`, http: { url: "https://x" } })) }}
      />,
    )
    expect(document.querySelectorAll("details[data-step-id]")).toHaveLength(9)
    fireEvent.click(screen.getByRole("button", { name: "Show all 14 rows" }))
    expect(document.querySelectorAll("details[data-step-id]")).toHaveLength(14)
  })

  it("opens a big phased recipe on the grouped map and drills into a phase and group", () => {
    const services = ["Billing", "Ledger", "Auth", "Search", "Mailer", "Reports", "Webhooks", "Pages"]
    const steps: Record<string, unknown>[] = [
      { id: "build", type: "http", name: "Fetch the build", http: { url: "https://ci" } },
      { id: "matrix", type: "transform", name: "Prepare the matrix", needs: ["build"] },
    ]
    for (const svc of services)
      for (let i = 0; i < 12; i += 1)
        steps.push({ id: `${svc.toLowerCase()}_${i}`, type: "script", name: `Check ${i} on ${svc}`, script: { path: `checks/${svc.toLowerCase()}/${i}.sh` }, needs: ["matrix"] })
    steps.push({ id: "post", type: "notify", name: "Post the matrix", notify: { to: "workspace" }, needs: steps.slice(2).map((s) => s.id) })
    render(<RoutineStepSpine definition={{ steps }} />)
    expect(screen.getByRole("button", { name: "map", exact: true })).toHaveAttribute("aria-pressed", "true")
    const map = screen.getByTestId("routine-grouped-map")
    expect(map).toHaveTextContent("Then · 96")
    expect(screen.getByTestId("routine-map-node-name:Billing")).toHaveTextContent("×12")
    fireEvent.click(screen.getByTestId("routine-map-node-name:Billing"))
    expect(screen.getByRole("button", { name: "list", exact: true })).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByTestId("routine-phase-3")).toHaveTextContent("Then · 96 steps in parallel")
    expect(screen.getByTestId("routine-phase-3")).toHaveTextContent("Script ×96 · 8 groups")
    expect(screen.getByTestId("routine-group-group:3:name:Billing")).toHaveTextContent("Billing · 12 steps")
    expect(document.querySelectorAll('details[data-step-id^="billing_"]')).toHaveLength(12)
    expect(document.querySelectorAll('details[data-step-id^="ledger_"]')).toHaveLength(0)
    fireEvent.click(screen.getByTestId("routine-phase-3"))
    expect(screen.queryByTestId("routine-group-group:3:name:Billing")).toBeNull()
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
    expect(open[0].textContent).toContain("Ask morgan")
  })
})
