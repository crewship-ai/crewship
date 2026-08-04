import { describe, it, expect } from "vitest"

import { stepLineRanges, stepIdAtLine } from "@/lib/routine-dsl-lines"

// Maps a caret position in the DSL source back to the step it sits in,
// so editing line 81 can select the node that line defines. The mapping
// has to survive the shapes a real definition takes — nested objects
// inside a step, arrays of strings, braces inside prompt text — because
// a mapping that is only right for tidy input silently selects the
// wrong node the moment someone writes a realistic prompt.

// Deliberately hostile, in two specific ways that a tidy fixture hides:
//
//   1. `first` opens a NESTED object carrying its own "id" BEFORE the
//      step's real id line. A scanner that accepts an id at any depth
//      names the step "not-the-step-id". (An earlier version of this
//      fixture put the nested object after the id, so the bug could not
//      show — the guard was untested and mutation proved it.)
//   2. `second` has an UNBALANCED brace inside a prompt: a `}` arrives
//      before the `{`. Balanced braces cancel out and let a scanner that
//      ignores strings look correct; an unbalanced one closes the step
//      seven lines early.
const SOURCE = `{
  "name": "demo",
  "steps": [
    {
      "transform": {
        "id": "not-the-step-id",
        "expression": "default(x)"
      },
      "id": "first",
      "type": "transform"
    },
    {
      "id": "second",
      "type": "agent_run",
      "needs": [
        "first"
      ],
      "prompt": "emit exactly } and nothing else {"
    },
    {
      "id": "third",
      "type": "wait"
    }
  ],
  "max_cost_usd": 5
}`

describe("stepLineRanges", () => {
  const ranges = stepLineRanges(SOURCE)

  it("finds every step in source order", () => {
    expect(ranges.map((r) => r.id)).toEqual(["first", "second", "third"])
  })

  it("spans each step from its opening brace to its closing brace", () => {
    const first = ranges[0]
    // 1-indexed lines: `{` of the first step is line 4, `}` is line 11.
    expect(first.startLine).toBe(4)
    expect(first.endLine).toBe(11)
  })

  it("names a step from its OWN id, not one nested inside it", () => {
    // `first` carries a nested object whose "id" appears three lines
    // before the step's real one.
    expect(ranges[0].id).toBe("first")
    expect(ranges.map((r) => r.id)).not.toContain("not-the-step-id")
  })

  it("is not confused by an unbalanced brace inside a string value", () => {
    const [, second] = ranges
    expect(second.startLine).toBe(12)
    // The prompt on line 18 opens with `}`. Counted as structure, it
    // closes the step there and the range ends at 18 instead of 19.
    expect(second.endLine).toBe(19)
    expect(stepIdAtLine(ranges, 19)).toBe("second")
    expect(ranges).toHaveLength(3)
  })

  it("ignores objects outside the steps array", () => {
    const withOther = `{
  "inputs": [
    {
      "id": "not-a-step"
    }
  ],
  "steps": [
    {
      "id": "real"
    }
  ]
}`
    expect(stepLineRanges(withOther).map((r) => r.id)).toEqual(["real"])
  })

  it("returns nothing for a source with no steps array", () => {
    expect(stepLineRanges('{"name":"x"}')).toEqual([])
  })

  it("never throws on malformed input", () => {
    expect(() => stepLineRanges("{{{ not json")).not.toThrow()
    expect(() => stepLineRanges("")).not.toThrow()
  })
})

describe("stepIdAtLine", () => {
  const ranges = stepLineRanges(SOURCE)

  it("resolves a line in the middle of a step to that step", () => {
    // Line 6 is the nested id — still inside `first`.
    expect(stepIdAtLine(ranges, 6)).toBe("first")
    expect(stepIdAtLine(ranges, 18)).toBe("second")
  })

  it("resolves the boundary lines inclusively", () => {
    expect(stepIdAtLine(ranges, 4)).toBe("first")
    expect(stepIdAtLine(ranges, 11)).toBe("first")
  })

  it("returns null for a line between or outside steps", () => {
    expect(stepIdAtLine(ranges, 2)).toBeNull()
    expect(stepIdAtLine(ranges, 999)).toBeNull()
  })

  it("returns null when there are no ranges", () => {
    expect(stepIdAtLine([], 5)).toBeNull()
  })
})
