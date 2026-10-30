import { describe, expect, it } from "vitest"
import { scheduleLastStatusLabel } from "@/lib/routine-schedule-status"

describe("scheduleLastStatusLabel", () => {
  it.each([
    ["QUEUED", undefined, "queued for capacity"],
    ["QUEUED", 2, "queued for capacity"],
    ["SKIPPED", 1, "skipped: capacity or catch-up"],
    ["SKIPPED", 3, "skipped: capacity or catch-up"],
    ["SKIPPED", 0, "skipped: previous run busy or routine inactive"],
    ["SKIPPED", undefined, "skipped: previous run busy or routine inactive"],
    ["COMPLETED", undefined, "COMPLETED"],
    ["FAILED", 1, "FAILED"],
    ["WAITING", undefined, "WAITING"],
  ] as const)("reads %s with last_missed_count=%s as %s", (last_status, last_missed_count, label) => {
    expect(scheduleLastStatusLabel({ last_status, last_missed_count })).toBe(label)
  })

  it("never claims a run is waiting now", () => {
    // Catch-up after downtime records SKIPPED with the backlog as
    // last_missed_count, exactly like a capacity skip (#3791), and a QUEUED
    // fire is not updated when its start later runs or expires.
    for (const s of [{ last_status: "SKIPPED", last_missed_count: 4 }, { last_status: "QUEUED" }]) {
      expect(scheduleLastStatusLabel(s)).not.toMatch(/waiting/i)
    }
  })

  it("never names an uncounted skip's cause outright", () => {
    // concurrency_policy: skip drops a busy tick as SKIPPED with no count,
    // and a routine disabled afterwards was still active when it skipped.
    expect(scheduleLastStatusLabel({ last_status: "SKIPPED", last_missed_count: 0 })).toMatch(/busy/)
  })

  it("shows nothing for a schedule that never fired", () => {
    expect(scheduleLastStatusLabel({})).toBeUndefined()
    expect(scheduleLastStatusLabel({ last_status: "" })).toBeUndefined()
  })
})
