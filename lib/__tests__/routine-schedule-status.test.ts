import { describe, expect, it } from "vitest"
import { scheduleLastStatusLabel } from "@/lib/routine-schedule-status"

describe("scheduleLastStatusLabel", () => {
  it.each([
    ["capacity", "skipped: capacity"],
    ["catchup", "skipped: catch-up"],
    ["inactive", "skipped: routine inactive"],
    ["busy", "skipped: previous run busy"],
  ])("reads the recorded %s skip independently of backlog", (last_skip_reason, label) => {
    for (const last_missed_count of [undefined, 0, 4]) {
      expect(scheduleLastStatusLabel({ last_status: "SKIPPED", last_skip_reason, last_missed_count })).toBe(label)
    }
  })

  it.each([undefined, "", "future_reason", "constructor", "__proto__"])("does not invent a missing or unknown cause (%s)", last_skip_reason => {
    for (const last_missed_count of [undefined, 0, 4]) {
      expect(scheduleLastStatusLabel({ last_status: "SKIPPED", last_skip_reason, last_missed_count })).toBe("skipped: reason unavailable")
    }
  })

  it.each([
    ["QUEUED", "queued for capacity"],
    ["COMPLETED", "COMPLETED"],
    ["FAILED", "FAILED"],
    ["WAITING", "WAITING"],
  ])("keeps %s independent of stale skip metadata", (last_status, label) => {
    expect(scheduleLastStatusLabel({ last_status, last_skip_reason: "busy" })).toBe(label)
  })

  it("never claims a run is waiting now", () => {
    for (const s of [{ last_status: "SKIPPED", last_skip_reason: "capacity" }, { last_status: "QUEUED" }]) {
      expect(scheduleLastStatusLabel(s)).not.toMatch(/waiting/i)
    }
  })

  it("shows nothing for a schedule that never fired", () => {
    expect(scheduleLastStatusLabel({})).toBeUndefined()
    expect(scheduleLastStatusLabel({ last_status: "", last_skip_reason: "busy" })).toBeUndefined()
  })
})
