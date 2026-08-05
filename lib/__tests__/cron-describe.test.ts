import { describe, it, expect } from "vitest"

import { describeCron } from "@/lib/cron-describe"

// "Nechci to ve formátu 0 nebo hvězdička, chci kalendář."
//
// There were two of these, privately, in two files. The routines copy
// rendered `0 8 * * 1` as "weekly (dow 1)" — cron jargon with a word
// wrapped round it, which is the thing the reader was asking not to
// see. This is the other copy, kept, extended and made arguable.

describe("describeCron", () => {
  it("names the day rather than numbering it", () => {
    expect(describeCron("0 8 * * 1")).toBe("Every Monday at 08:00")
  })

  it("reads a weekday range as weekdays", () => {
    expect(describeCron("30 9 * * 1-5")).toBe("Every weekday at 09:30")
  })

  it("lists selected days in words", () => {
    expect(describeCron("0 7 * * 1,3,5")).toBe("Every Mon, Wed and Fri at 07:00")
  })

  it("handles a two-day list without a stray comma", () => {
    expect(describeCron("0 7 * * 2,4")).toBe("Every Tue and Thu at 07:00")
  })

  it("reads Sunday from either 0 or 7", () => {
    expect(describeCron("0 6 * * 0")).toBe("Every Sunday at 06:00")
    expect(describeCron("0 6 * * 7")).toBe("Every Sunday at 06:00")
  })

  it("describes a plain daily schedule", () => {
    expect(describeCron("0 8 * * *")).toBe("Every day at 08:00")
  })

  it("pads a single-digit time", () => {
    expect(describeCron("5 9 * * *")).toBe("Every day at 09:05")
  })

  it("describes minute and hour intervals", () => {
    expect(describeCron("*/15 * * * *")).toBe("Every 15 minutes")
    expect(describeCron("*/1 * * * *")).toBe("Every minute")
    expect(describeCron("0 */6 * * *")).toBe("Every 6 hours")
  })

  it("describes a monthly schedule with an ordinal", () => {
    expect(describeCron("0 0 1 * *")).toBe("Monthly on the 1st at 00:00")
    expect(describeCron("0 0 2 * *")).toBe("Monthly on the 2nd at 00:00")
    expect(describeCron("0 0 3 * *")).toBe("Monthly on the 3rd at 00:00")
    expect(describeCron("0 0 11 * *")).toBe("Monthly on the 11th at 00:00")
    expect(describeCron("0 0 21 * *")).toBe("Monthly on the 21st at 00:00")
  })

  it("falls back to the raw expression rather than inventing a sentence", () => {
    // An expression this does not understand is shown as written. A
    // wrong sentence is worse than a cron string: the reader cannot
    // tell it is wrong.
    expect(describeCron("0 0 1 1 1")).toBe("0 0 1 1 1")
    expect(describeCron("not a cron")).toBe("not a cron")
  })

  it("renders nothing as an em dash", () => {
    expect(describeCron("")).toBe("—")
    expect(describeCron(undefined)).toBe("—")
  })
})
