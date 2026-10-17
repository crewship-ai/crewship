import { describe, it, expect } from "vitest"
import { CHIP_TONE, STATUS_TONES, statusTone, toStatusTone } from "@/lib/status-tone"

// Every status pill picks one of six tones. Feature code grew its own words
// for them (DetailTone "destructive", backups "bad", severity "high", palette
// "emerald"); this is the single translation, so the same meaning always
// lands on the same chip.
describe("toStatusTone", () => {
  it("passes the six pill tones through", () => {
    for (const t of STATUS_TONES) expect(toStatusTone(t)).toBe(t)
  })

  it("reads the chip token names", () => {
    expect(toStatusTone("ok")).toBe("success")
    expect(toStatusTone("info")).toBe("blue")
    expect(toStatusTone("warn")).toBe("warn")
    expect(toStatusTone("danger")).toBe("danger")
    expect(toStatusTone("neutral")).toBe("muted")
    expect(toStatusTone("violet")).toBe("purple")
  })

  it("reads the kit DetailTone vocabulary", () => {
    expect(toStatusTone("default")).toBe("muted")
    expect(toStatusTone("destructive")).toBe("danger")
  })

  it("reads ok/bad, severity and palette words consistently", () => {
    expect(toStatusTone("bad")).toBe("danger")
    expect(toStatusTone("none")).toBe("muted")
    expect(toStatusTone("critical")).toBe("danger")
    expect(toStatusTone("high")).toBe("danger")
    expect(toStatusTone("medium")).toBe("warn")
    expect(toStatusTone("warning")).toBe("warn")
    expect(toStatusTone("low")).toBe("muted")
    expect(toStatusTone("emerald")).toBe("success")
    expect(toStatusTone("green")).toBe("success")
    expect(toStatusTone("amber")).toBe("warn")
    expect(toStatusTone("orange")).toBe("warn")
    expect(toStatusTone("red")).toBe("danger")
    expect(toStatusTone("slate")).toBe("muted")
    expect(toStatusTone("gray")).toBe("muted")
  })

  it("is case-insensitive", () => {
    expect(toStatusTone("Destructive")).toBe("danger")
    expect(toStatusTone("OK")).toBe("success")
  })

  it("falls back for unknown or missing words", () => {
    expect(toStatusTone("chartreuse")).toBe("muted")
    expect(toStatusTone(undefined)).toBe("muted")
    expect(toStatusTone("chartreuse", "blue")).toBe("blue")
  })

  it("names the chip token behind each tone", () => {
    expect(CHIP_TONE).toEqual({ success: "ok", blue: "info", warn: "warn", danger: "danger", muted: "neutral", purple: "violet" })
  })
})

describe("statusTone", () => {
  it("is the tone formatStatus gives the raw status", () => {
    expect(statusTone("IN_PROGRESS")).toBe("blue")
    expect(statusTone("failed")).toBe("danger")
    expect(statusTone("completed")).toBe("success")
    expect(statusTone("never-seen")).toBe("muted")
  })
})
