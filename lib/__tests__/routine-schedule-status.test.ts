import { describe, expect, it } from "vitest"
import { scheduleLastStatusLabel } from "@/lib/routine-schedule-status"

describe("scheduleLastStatusLabel", () => {
  it.each([
    ["QUEUED", undefined, "Waiting for capacity"],
    ["QUEUED", 2, "Waiting for capacity"],
    ["SKIPPED", 1, "Skipped – a run is already waiting"],
    ["SKIPPED", 3, "Skipped – a run is already waiting"],
    ["SKIPPED", 0, "Routine inactive"],
    ["SKIPPED", undefined, "Routine inactive"],
    ["COMPLETED", undefined, "COMPLETED"],
    ["FAILED", 1, "FAILED"],
    ["WAITING", undefined, "WAITING"],
    ["DEDUPED", undefined, "DEDUPED"],
  ])("reads %s with last_missed_count=%s as %s", (last_status, last_missed_count, label) => {
    expect(scheduleLastStatusLabel({ last_status, last_missed_count })).toBe(label)
  })

  it("shows nothing for a schedule that never fired", () => {
    expect(scheduleLastStatusLabel({})).toBeUndefined()
    expect(scheduleLastStatusLabel({ last_status: "" })).toBeUndefined()
  })
})
