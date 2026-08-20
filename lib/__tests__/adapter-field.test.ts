import { describe, it, expect } from "vitest"

import { fieldText } from "../adapter-field"

describe("fieldText — adapter-supplied JSON read as display text", () => {
  it("passes a string through, trimmed", () => {
    expect(fieldText(" claude-opus-4 ")).toBe("claude-opus-4")
  })

  it("treats an empty or whitespace-only string as absent", () => {
    // An empty field and a missing field must render identically, so callers
    // need one falsy answer, not two.
    expect(fieldText("")).toBeUndefined()
    expect(fieldText("   ")).toBeUndefined()
  })

  it("renders scalars an adapter may legitimately send", () => {
    expect(fieldText(7)).toBe("7")
    expect(fieldText(0)).toBe("0")
    expect(fieldText(false)).toBe("false")
  })

  it("reads a structured value as absent rather than stringifying it", () => {
    // The whole point: the field whose type changed under us costs a row, not
    // a thrown render and not an "[object Object]" in a label.
    expect(fieldText({ name: "fs" })).toBeUndefined()
    expect(fieldText(["a", "b"])).toBeUndefined()
    expect(fieldText(null)).toBeUndefined()
    expect(fieldText(undefined)).toBeUndefined()
  })
})
