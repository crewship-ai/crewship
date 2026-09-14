import { describe, expect, it } from "vitest"
import {
  comparisonVerdict,
  parseComparisonCases,
  readComparisonResult,
  type ComparisonJob,
} from "../routine-comparison"
const job: ComparisonJob = {
  case: { id: "case", inputs: { count: 0, enabled: false }, expected_output: "" },
  side: "A",
  config: { tier: "fast", version: { version: 3, definition_hash: "frozen" } },
  key: "k",
  run_id: "r",
}
const raw = {
  id: "r",
  workspace_id: "ws",
  status: "completed",
  output: "",
  cost_usd: 0,
  duration_ms: 0,
  pipeline_version: 3,
  definition_hash: "frozen",
}
describe("routine comparisons", () => {
  it.each(["interrupted", "dry_run"])("N7 finishes comparison for terminal %s", (status) => {
    const result = readComparisonResult({ ...raw, status }, job, "ws")
    expect(comparisonVerdict({ ...job, result })).not.toBe("Pending")
  })
  it("preserves typed dataset values and exact empty expectations", () => {
    expect(parseComparisonCases(JSON.stringify([job.case]))[0]).toEqual(job.case)
    const result = readComparisonResult(raw, job, "ws")
    expect(comparisonVerdict({ ...job, result })).toBe("Exact match")
    expect(
      comparisonVerdict({ ...job, case: { ...job.case, expected_output: undefined }, result }),
    ).toContain("quality ungraded")
    expect(comparisonVerdict({ ...job, result: { ...result, status: "waiting" } })).toBe("Pending")
  })
  it.each(
    [
      [],
      [{ id: "x", inputs: [] }],
      [job.case, job.case],
      [{ id: "x", inputs: {}, expected_output: 0 }],
    ].map((value) => [value]),
  )("rejects invalid datasets", (cases) => {
    expect(() => parseComparisonCases(JSON.stringify(cases))).toThrow()
  })
  it.each([
    { workspace_id: "other" },
    { id: "other" },
    { pipeline_version: 4 },
    { definition_hash: "published-between-runs" },
    { cost_usd: null },
  ])("rejects mismatched run evidence", (patch) => {
    expect(() => readComparisonResult({ ...raw, ...patch }, job, "ws")).toThrow()
  })
})
