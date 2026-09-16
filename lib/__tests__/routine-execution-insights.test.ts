import { expect, it } from "vitest"
import { routineExecutionInsights } from "../routine-execution-insights"
import type { RunExecution } from "@/hooks/use-run-executions"
const row = (patch: Partial<RunExecution>): RunExecution => ({
  id: "a",
  parent_execution_id: "",
  step_id: "extract",
  execution_path: "/extract",
  attempt: 1,
  kind: "agent_run",
  status: "completed",
  agent_slug: "",
  model: "",
  started_at: "2026-09-15T00:00:00Z",
  ended_at: "2026-09-15T00:00:10Z",
  error: "",
  output_bytes: 5,
  ...patch,
})
it("does not double count children, duplicate pages or unfinished durations", () => {
  const result = routineExecutionInsights([
    row({}),
    row({
      id: "child",
      parent_execution_id: "a",
      execution_path: "/extract/agent",
      attempt: 2,
      ended_at: "2026-09-15T00:00:20Z",
    }),
    row({ id: "retry", attempt: 2 }),
    row({ id: "retry", attempt: 2 }),
    row({ id: "active", status: "running", ended_at: "" }),
  ])
  expect(result.longest?.durationMs).toBe(10000)
  expect(result.longest?.row.parent_execution_id).toBe("")
  expect(result.repeatedPaths).toBe(2)
  expect(result.retainedSteps).toBe(1)
})
