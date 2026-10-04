import { describe, expect, it } from "vitest"
import { summarizeValue } from "../summarize-value"

describe("value previews", () => {
  it.each([[undefined, ""], [null, "null"], [true, "true"], [false, "false"], [42, "42"], [0, "0"], ["hello", "hello"]])("renders %s as %s", (value, preview) => {
    expect(summarizeValue(value)).toBe(preview)
  })

  it("renders compact JSON for nested objects and arrays", () => {
    expect(summarizeValue({ items: [1, "two", null] })).toBe('{"items":[1,"two",null]}')
    expect(summarizeValue([true, false])).toBe("[true,false]")
  })

  it("keeps an exact-length value and truncates only past the cap", () => {
    expect(summarizeValue("x".repeat(80))).toBe("x".repeat(80))
    expect(summarizeValue("x".repeat(81))).toBe(`${"x".repeat(79)}…`)
    expect(summarizeValue({ long: "x".repeat(50) }, { maxChars: 12 })).toBe('{"long":"xx…')
  })

  it.each([0, -5, 1, 7])("clamps a requested cap of %s to eight characters", (maxChars) => {
    expect(summarizeValue("123456789", { maxChars })).toBe("1234567…")
  })

  it("includes both quotes within the returned preview's cap", () => {
    expect(summarizeValue("123456", { maxChars: 8, quoteStrings: true })).toBe('"123456"')
    expect(summarizeValue("1234567", { maxChars: 8, quoteStrings: true })).toBe('"12345…"')
    expect(summarizeValue("x".repeat(81), { quoteStrings: true })).toBe(`"${"x".repeat(77)}…"`)
  })

  it("caps the fallback when JSON serialization fails", () => {
    const circular: { self?: unknown } = {}
    circular.self = circular
    expect(summarizeValue(circular, { maxChars: 8 })).toBe("[object…")
    expect(summarizeValue(12345678901234567890n, { maxChars: 8 })).toBe("1234567…")
  })

  it("caps a long number's preview without changing a short primitive", () => {
    expect(summarizeValue(123456789, { maxChars: 8 })).toBe("1234567…")
    expect(summarizeValue(false, { maxChars: 8 })).toBe("false")
  })

  it("handles an object whose toJSON omits its value", () => {
    expect(summarizeValue({ toJSON: () => undefined }, { maxChars: 8 })).toBe("[object…")
  })

  it("allows a caller to request a large preview", () => {
    expect(summarizeValue("x".repeat(1000), { maxChars: 1001 })).toBe("x".repeat(1000))
  })
})
