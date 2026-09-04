import { describe, it, expect } from "vitest"
import { formatStatus, isKnownStatus } from "@/lib/format-status"

// Raw enums reached the screen in thirteen different spellings; this is the
// one place they become words, so the contract is: known → its word and tone,
// unknown → readable words in the muted tone, never the enum itself.
describe("formatStatus", () => {
  it("maps the enums the product actually stores", () => {
    expect(formatStatus("IN_PROGRESS")).toEqual({ label: "In progress", tone: "blue" })
    expect(formatStatus("pending_review")).toEqual({ label: "Pending review", tone: "warn" })
    expect(formatStatus("running")).toEqual({ label: "Running", tone: "blue" })
    expect(formatStatus("FAILED").tone).toBe("danger")
    expect(formatStatus("COMPLETED").label).toBe("Done")
    expect(formatStatus("DONE").label).toBe("Done")
  })

  it("maps the connection, delivery and agent states the fleet screens carry", () => {
    expect(formatStatus("delivering")).toEqual({ label: "Delivering", tone: "success" })
    expect(formatStatus("never_used")).toEqual({ label: "Never used", tone: "warn" })
    expect(formatStatus("NOT_CHECKED")).toEqual({ label: "Not checked", tone: "muted" })
    expect(formatStatus("dropped_pref").label).toBe("Muted")
    expect(formatStatus("dropped_rate").label).toBe("Rate-gated")
    expect(formatStatus("STOPPED").tone).toBe("warn")
  })

  it("never shows a raw enum for an unknown status", () => {
    expect(formatStatus("SOME_NEW_STATE")).toEqual({ label: "Some new state", tone: "muted" })
    expect(formatStatus("awaiting-something")).toEqual({ label: "Awaiting something", tone: "muted" })
    expect(formatStatus(null)).toEqual({ label: "Unknown", tone: "muted" })
    expect(formatStatus("")).toEqual({ label: "Unknown", tone: "muted" })
  })

  it("is case- and separator-insensitive", () => {
    expect(formatStatus("in progress")).toEqual(formatStatus("IN_PROGRESS"))
    expect(isKnownStatus("in-progress")).toBe(true)
    expect(isKnownStatus("nope")).toBe(false)
  })
})
