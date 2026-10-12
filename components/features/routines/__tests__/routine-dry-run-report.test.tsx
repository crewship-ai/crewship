import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { RoutineDryRunReport, type DryRunResult } from "@/components/features/routines/routine-dry-run-report"

afterEach(cleanup)
const base: DryRunResult = { run_id: "preview", status: "dry_run_ok", would_execute: [] }
it("identifies a preview as unexecuted intent and lets the reviewer dismiss it", () => {
  const close = vi.fn()
  render(<RoutineDryRunReport result={base} onClose={close} />)
  expect(screen.getByText("Plan preview — does not run agents or prove success.")).toBeInTheDocument()
  expect(screen.getByText("0 steps")).toBeInTheDocument()
  expect(screen.getByText(/No steps to execute/)).toBeInTheDocument()
  expect(screen.getByText("This routine declares no external resources.")).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Dismiss dry-run report" }))
  expect(close).toHaveBeenCalledOnce()
})
it("uses an explicit zero total and singular count without replacing them with estimates", () => {
  render(<RoutineDryRunReport result={{ ...base, cost_usd: 0, would_execute: [{ step_id: "agent", step_type: "agent_run", estimated_cost_usd: 0.25, would_call_agent: "assistant", tier_adapter: "openai", tier_model: "gpt-5" }] }} onClose={vi.fn()} />)
  expect(screen.getByText("1 step")).toBeInTheDocument()
  expect(screen.getByText("~$0.0000 est.")).toBeInTheDocument()
  expect(screen.getByText("~$0.2500")).toBeInTheDocument()
  expect(screen.getByText("assistant")).toBeInTheDocument()
  expect(screen.getByTitle("Resolved execution tier (adapter:model)")).toHaveTextContent("openai:gpt-5")
})
it("sums only known estimates and preserves each planned step and optional target", () => {
  const steps = ["call_pipeline", "http", "code", "wait", "transform", "future"].map((step_type, i) => ({ step_id: `step-${i}`, step_type, ...(i === 0 ? { would_call_pipeline: "child", estimated_cost_usd: 0.125, tier_model: "model-only" } : {}), ...(i === 1 ? { tier_adapter: "adapter-only", estimated_cost_usd: 0 } : {}) }))
  render(<RoutineDryRunReport result={{ ...base, would_execute: steps, manifest: { agents: ["worker"], integrations: [], egress: [], credentials: [], routines: [], datastores: [], tools: [], has_http: false, has_code: false } }} onClose={vi.fn()} />)
  expect(screen.getByText("6 steps")).toBeInTheDocument()
  expect(screen.getByText("~$0.1250 est.")).toBeInTheDocument()
  expect(screen.getByText("child")).toBeInTheDocument()
  expect(screen.getByText("@worker")).toBeInTheDocument()
  const rows = screen.getAllByRole("listitem")
  expect(rows).toHaveLength(6)
  steps.forEach((step, i) => {
    expect(within(rows[i]).getByText(step.step_id)).toBeInTheDocument()
    expect(within(rows[i]).getByText(String(i + 1).padStart(2, "0"))).toBeInTheDocument()
  })
  expect(screen.getByText("—:model-only")).toBeInTheDocument()
  expect(screen.getByText("adapter-only")).toBeInTheDocument()
})
it("handles a legacy wire response with no per-step plan", () => {
  const result = JSON.parse('{"run_id":"legacy","status":"dry_run_ok"}')
  render(<RoutineDryRunReport result={result} onClose={vi.fn()} />)
  expect(screen.getByText("0 steps")).toBeInTheDocument()
  expect(screen.getByText("~$0.0000 est.")).toBeInTheDocument()
})
