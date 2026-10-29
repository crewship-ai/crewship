import { describe, expect, it } from "vitest"
import { scheduleLastStatusLabel } from "@/lib/routine-schedule-status"

describe("scheduleLastStatusLabel", () => {
  it.each([
    ["QUEUED", undefined, "Waiting for capacity"],
    ["QUEUED", 2, "Waiting for capacity"],
    ["SKIPPED", 1, "Skipped – capacity or catch-up"],
    ["SKIPPED", 3, "Skipped – capacity or catch-up"],
    ["SKIPPED", 0, "Routine inactive"],
    ["SKIPPED", undefined, "Routine inactive"],
    ["COMPLETED", undefined, "COMPLETED"],
    ["FAILED", 1, "FAILED"],
    ["WAITING", undefined, "WAITING"],
    ["DEDUPED", undefined, "DEDUPED"],
  ])("reads %s with last_missed_count=%s as %s", (last_status, last_missed_count, label) => {
    expect(scheduleLastStatusLabel({ last_status, last_missed_count })).toBe(label)
  })

  it("never claims a run is waiting for a counted skip", () => {
    // Catch-up after downtime records SKIPPED with the backlog as
    // last_missed_count, exactly like a capacity skip (#3791).
    expect(scheduleLastStatusLabel({ last_status: "SKIPPED", last_missed_count: 4 })).not.toMatch(/waiting/i)
  })

  it("shows nothing for a schedule that never fired", () => {
    expect(scheduleLastStatusLabel({})).toBeUndefined()
    expect(scheduleLastStatusLabel({ last_status: "" })).toBeUndefined()
  })
})
