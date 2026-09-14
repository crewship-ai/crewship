import { describe, it, expect } from "vitest"
import { formatRoutineTime } from "../routine-time"
describe("routine times", () => {
  it("shows the schedule zone and the corresponding summer/winter wall clock", () => {
    expect(formatRoutineTime("2026-09-13T00:30:00Z", "Europe/Prague")).toBe(
      "13 Sept 2026, 02:30 · Europe/Prague",
    )
    expect(formatRoutineTime("2026-12-13T00:30:00Z", "Europe/Prague")).toBe(
      "13 Dec 2026, 01:30 · Europe/Prague",
    )
    expect(formatRoutineTime("2026-09-13T00:30:00Z", "UTC")).toBe(
      "13 Sept 2026, 00:30 · UTC",
    )
  })
  it("does not invent a date for malformed data", () => {
    expect(formatRoutineTime("bad")).toBe("Time unavailable")
  })
})
