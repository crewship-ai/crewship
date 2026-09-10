import { describe, expect, it } from "vitest"
import { routinePresetSummary } from "../routine-preset-summary"

describe("routine preset summary", () => {
  it("distinguishes empty inputs from unavailable data", () => {
    expect(routinePresetSummary({})).toBe("No inputs")
    expect(routinePresetSummary(null)).toBe("Inputs unavailable")
    expect(routinePresetSummary(undefined)).toBe("Inputs unavailable")
  })
  it("shows primitives and counts remaining fields without serializing objects", () => {
    expect(routinePresetSummary({ count: 0, enabled: false, details: { content: "hidden" }, list: [1], extra: 3 })).toBe("Inputs: count: 0 · enabled: false · +3 more")
  })
  it("clips long keys and first lines in the actual text", () => {
    const text = routinePresetSummary({ ["k".repeat(300)]: "x".repeat(1000) + "\nsecond line" })
    expect(text.length).toBeLessThan(120)
    expect(text).toContain("…")
    expect(text).not.toContain("second line")
  })
  it("hides secrets and summarizes credentials and files by type", () => {
    expect(routinePresetSummary({ api_key: "secret-value", credential: "vault-private-id" })).toBe("Inputs: api_key: Hidden · credential: Credential reference")
    expect(routinePresetSummary({ file: { name: "invoice.pdf", content: "private-content" } })).toBe("Inputs: file: File (invoice.pdf)")
    expect(routinePresetSummary({ attachment: "data:text/plain;base64,c2VjcmV0" })).not.toContain("c2VjcmV0")
  })
})
