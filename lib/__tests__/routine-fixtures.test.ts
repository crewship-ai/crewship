import { describe, expect, it } from "vitest"
import { capturedRoutineFixture } from "../routine-fixtures"

const raw = {
  id: "r",
  workspace_id: "ws",
  definition_status: "available",
  definition: { name: "test" },
  definition_hash: "hash",
  pipeline_version: 2,
  step_outputs_available: true,
  step_outputs: { done: "", count: "0" },
  inputs: { enabled: false, count: 0 },
  status: "failed",
}
describe("captured routine data", () => {
  it("preserves partial outputs, empty strings, false and zero with source evidence", () => {
    expect(capturedRoutineFixture(raw, "ws", "r")).toEqual({
      source: {
        run_id: "r",
        workspace_id: "ws",
        definition_hash: "hash",
        pipeline_version: 2,
        status: "failed",
      },
      inputs: raw.inputs,
      step_outputs: raw.step_outputs,
    })
  })
  it.each([
    { workspace_id: "other" },
    { id: "other" },
    { definition_status: "unavailable" },
    { definition_status: "error" },
    { step_outputs_available: false },
    { step_outputs: null },
    { inputs: [] },
  ])("rejects unusable or wrong-scope evidence %s", (patch) => {
    expect(() => capturedRoutineFixture({ ...raw, ...patch }, "ws", "r")).toThrow()
  })
})
