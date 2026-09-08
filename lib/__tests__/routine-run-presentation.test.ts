import { describe, expect, it } from "vitest"
import { routineResultLabel, routineRunPresentation, routineRunExplanation } from "../routine-run-presentation"
import { slashFieldsFromRoutineInputs, routineInputsFromValues } from "../routine-inputs"

describe("routine result and execution identity", () => {
  it("keeps a completed engine run with failed review visibly unsuccessful", () => {
    expect(routineRunPresentation({ status: "completed", outcome: "FAILED" }).tone).toBe("destructive")
    expect(routineRunPresentation({ status: "completed", outcome: "NEEDS_HUMAN" }).tone).toBe("warn")
  })
  it("does not invent a business interpretation for false", () => {
    expect(routineResultLabel("false", { outputs: [{ name: "spike" }] })).toBeNull()
    expect(routineRunPresentation({ status: "completed" }).label).toBe("Completed")
  })
  it("uses only declared labels from the executed recipe, including object outputs", () => {
    const definition = { outputs: [{ name: "spike", value_labels: { false: "Limit not exceeded", true: "Limit exceeded" } }] }
    expect(routineResultLabel("false", definition)).toBe("Limit not exceeded")
    expect(routineResultLabel('{"spike":true}', definition)).toBe("Limit exceeded")
    expect(routineResultLabel('{"unrelated":false}', definition)).toBeNull()
    expect(routineResultLabel("toString", definition)).toBeNull()
  })
  it("shows a human label without changing the programmatic key or type", () => {
    const fields = slashFieldsFromRoutineInputs([{ name: "threshold_usd", label: "Cost limit (USD)", type: "number", default: 5 }])
    expect(fields[0].label).toBe("Cost limit (USD)")
    expect(routineInputsFromValues(fields, { threshold_usd: "7" })).toEqual({ threshold_usd: 7 })
  })
})


describe("client run explanations", () => {
  it.each([
    ["PARTIAL", "Partially completed", "warn"],
    ["NO_CHANGE", "No change needed", "success"],
    ["WORK_CREATED", "Follow-up work created", "blue"],
    ["CANCELLED", "Stopped", "default"],
  ])("preserves %s business outcome", (outcome, label, tone) => {
    expect(routineRunPresentation({ status: "completed", outcome })).toEqual({ label, tone })
  })
  it("does not mistake every wait for a human decision", () => {
    expect(routineRunPresentation({ status: "waiting" }).label).toBe("Waiting")
    expect(routineRunExplanation({ status: "waiting" }, "event").title).toBe("Waiting for an event")
    expect(routineRunExplanation({ status: "running" }, "datetime").title).toBe("Waiting until a scheduled time")
    expect(routineRunExplanation({ status: "waiting" }, "approval").title).toBe("A review is needed")
    expect(routineRunExplanation({ status: "waiting" }).detail).toContain("No human decision has been confirmed")
  })
  it("keeps recorded work separate from missing completion and arbitrary errors", () => {
    expect(routineRunExplanation({ status: "completed", outcome: "FAILED", error_message: "no outcome reported", output: "report" }).title).toContain("result was recorded")
    expect(routineRunExplanation({ status: "failed", error_message: "fetch failed" }).title).toBe("This run could not finish")
    expect(routineRunExplanation({ status: "completed", outcome: "SUCCEEDED", output: "false" }).title).toBe("Completed")
  })
  it("does not report stale waits as active after stop", () => {
    expect(routineRunExplanation({ status: "cancelled" }, "approval").title).toBe("Run stopped")
  })
})
