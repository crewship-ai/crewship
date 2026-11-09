import { describe, expect, it } from "vitest"
import { parseDashboardRuns } from "../artifact-dashboard-runs"

const summary = (over: Record<string, unknown> = {}, latest: Record<string, unknown> = {}) => ({
  collection: "sales", last_attempt_at: "2026-11-08T11:00:00Z", last_success_at: "2026-11-08T10:00:00Z",
  latest: { id: "run_0123456789abcdef01234567", collection: "sales", state: "failed", started_by: "Agent", started_by_kind: "agent", started_at: "2026-11-08T11:00:00Z", finished_at: "2026-11-08T11:01:00Z", failure_reason: "warehouse 502", ...latest },
  ...over,
})

describe("parseDashboardRuns", () => {
  it("reads the latest run, last attempt and last success per declared collection", () => {
    const runs = parseDashboardRuns({ code_version: 2, runs: [summary()] }, 2, ["sales"])
    expect(runs).toEqual([{ collection: "sales", state: "failed", startedAt: new Date("2026-11-08T11:00:00Z"), finishedAt: new Date("2026-11-08T11:01:00Z"), lastAttemptAt: new Date("2026-11-08T11:00:00Z"), lastSuccessAt: new Date("2026-11-08T10:00:00Z"), reason: "warehouse 502", startedBy: "Agent" }])
  })
  it("keeps a run without a success or a reason", () => {
    const runs = parseDashboardRuns({ code_version: 1, runs: [summary({ last_success_at: undefined }, { state: "running", finished_at: undefined, failure_reason: undefined })] }, 1, ["sales"])
    expect(runs?.[0]).toMatchObject({ state: "running", finishedAt: null, lastSuccessAt: null, reason: null })
  })
  it.each([
    ["another code version", { code_version: 3, runs: [summary()] }],
    ["an unknown state", { code_version: 2, runs: [summary({}, { state: "queued" })] }],
    ["an invalid date", { code_version: 2, runs: [summary({ last_attempt_at: "yesterday" })] }],
    ["a mismatched collection", { code_version: 2, runs: [summary({}, { collection: "costs" })] }],
    ["not an object", "runs"],
    ["runs not a list", { code_version: 2, runs: {} }],
  ])("rejects %s", (_, raw) => {
    expect(parseDashboardRuns(raw, 2, ["sales"])).toBeNull()
  })
  it("drops a collection the shown version does not declare", () => {
    expect(parseDashboardRuns({ code_version: 2, runs: [summary({ collection: "costs" }, { collection: "costs" })] }, 2, ["sales"])).toEqual([])
  })
})
