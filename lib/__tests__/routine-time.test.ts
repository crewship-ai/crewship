import { describe, it, expect } from "vitest"
import { clockChangeFlags, formatRoutineTime } from "../routine-time"
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

describe("clockChangeFlags", () => {
  const prague = "Europe/Prague"
  it("flags the start after which the zone's clock differs from the one before", () => {
    // Before the 31 Mar 2030 change Prague is UTC+1, after it UTC+2.
    const occurrences = ["2030-03-29T07:00:00Z", "2030-04-05T06:00:00Z", "2030-04-12T06:00:00Z"]
    expect(clockChangeFlags(occurrences, prague, new Date("2030-03-22T00:00:00Z"))).toEqual([false, true, false])
  })

  it("compares the first start with the moment the preview was asked", () => {
    expect(clockChangeFlags(["2030-04-05T06:00:00Z"], prague, new Date("2030-03-25T00:00:00Z"))).toEqual([true])
    expect(clockChangeFlags(["2030-04-05T06:00:00Z"], prague, new Date("2030-04-01T00:00:00Z"))).toEqual([false])
  })

  it("is quiet for a zone with no clock changes, and for what it cannot read", () => {
    expect(clockChangeFlags(["2030-03-29T07:00:00Z", "2030-04-05T06:00:00Z"], "Asia/Tokyo", new Date("2030-03-22T00:00:00Z"))).toEqual([false, false])
    expect(clockChangeFlags(["not a date", "2030-04-05T06:00:00Z"], "Not/AZone", new Date("2030-03-22T00:00:00Z"))).toEqual([false, false])
  })
})
