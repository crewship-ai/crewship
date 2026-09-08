import { describe, expect, it } from "vitest"
import { routineResultLabel, routineRunPresentation } from "../routine-run-presentation"
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
