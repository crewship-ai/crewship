import { describe, expect, it } from "vitest"
import { scheduleLastStatusLabel } from "@/lib/routine-schedule-status"

describe("scheduleLastStatusLabel", () => {
  it.each([
    ["QUEUED", undefined, "active", "queued for capacity"],
    ["QUEUED", 2, "active", "queued for capacity"],
    ["SKIPPED", 1, "active", "skipped: capacity or catch-up"],
    ["SKIPPED", 3, "disabled", "skipped: capacity or catch-up"],
    ["SKIPPED", 0, "disabled", "skipped: routine inactive"],
    ["SKIPPED", undefined, "proposed", "skipped: routine inactive"],
    ["SKIPPED", 0, "active", "skipped: previous run busy or routine inactive"],
    ["COMPLETED", undefined, "active", "COMPLETED"],
    ["FAILED", 1, "active", "FAILED"],
    ["WAITING", undefined, "active", "WAITING"],
  ] as const)("reads %s with last_missed_count=%s on a %s routine as %s", (last_status, last_missed_count, lifecycle, label) => {
    expect(scheduleLastStatusLabel({ last_status, last_missed_count }, lifecycle)).toBe(label)
  })

  it("never claims a run is waiting now", () => {
    // Catch-up after downtime records SKIPPED with the backlog as
    // last_missed_count, exactly like a capacity skip (#3791), and a QUEUED
    // fire is not updated when its start later runs or expires.
    for (const s of [{ last_status: "SKIPPED", last_missed_count: 4 }, { last_status: "QUEUED" }]) {
      expect(scheduleLastStatusLabel(s, "active")).not.toMatch(/waiting/i)
    }
  })

  it("never calls an active routine inactive outright", () => {
    // concurrency_policy: skip drops a busy tick as SKIPPED with no count.
    expect(scheduleLastStatusLabel({ last_status: "SKIPPED", last_missed_count: 0 }, "active")).not.toBe("skipped: routine inactive")
  })

  it("shows nothing for a schedule that never fired", () => {
    expect(scheduleLastStatusLabel({}, "active")).toBeUndefined()
    expect(scheduleLastStatusLabel({ last_status: "" }, "active")).toBeUndefined()
  })
})
