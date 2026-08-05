import { describe, it, expect } from "vitest"

import { diagnose } from "@/lib/routine-dsl-diagnostics"

// Inline errors, on the line that caused them.
//
// Deliberately NOT a generic JSON Schema validator. Ajv against a
// schema this size answers a mistyped step kind with "must match
// exactly one schema in oneOf" pointed at the whole document, which is
// worse than no message. These are the handful of mistakes people
// actually make, each phrased as the fix.

const yaml = (s: string) => diagnose(s, "yaml")

describe("diagnose — syntax", () => {
  it("reports a parse failure on its own line", () => {
    const d = yaml("name: demo\nsteps:\n\t- id: a\n")
    expect(d).toHaveLength(1)
    expect(d[0].line).toBe(3)
    expect(d[0].severity).toBe("error")
  })

  it("stops after a syntax error instead of piling on nonsense", () => {
    // Semantic checks need a parsed document; running them on a broken
    // one produces a cascade that buries the one error that matters.
    const d = yaml("\t\tbroken")
    expect(d).toHaveLength(1)
  })
})

describe("diagnose — structure", () => {
  it("flags a missing steps array", () => {
    const d = yaml("name: demo\n")
    expect(d.some((x) => /steps/.test(x.message))).toBe(true)
  })

  it("accepts a well-formed routine with nothing to say", () => {
    const d = yaml(`name: demo
steps:
  - id: a
    type: http
  - id: b
    type: agent_run
    needs:
      - a
`)
    expect(d).toEqual([])
  })
})

describe("diagnose — steps", () => {
  it("names an unknown step kind and points at its line", () => {
    const d = yaml(`name: demo
steps:
  - id: a
    type: htpp
`)
    expect(d).toHaveLength(1)
    expect(d[0].message).toContain("htpp")
    // Line 4 is `type: htpp` — the marker sits on the token that is
    // wrong, not on the first line of the step containing it.
    expect(d[0].line).toBe(4)
  })

  it("catches a needs reference to a step that does not exist", () => {
    const d = yaml(`name: demo
steps:
  - id: a
    type: http
  - id: b
    type: http
    needs:
      - typo
`)
    expect(d).toHaveLength(1)
    expect(d[0].message).toContain("typo")
  })

  it("catches a duplicate step id", () => {
    const d = yaml(`name: demo
steps:
  - id: a
    type: http
  - id: a
    type: http
`)
    expect(d.some((x) => /a/.test(x.message))).toBe(true)
    expect(d).toHaveLength(1)
  })

  it("catches a step with no id", () => {
    const d = yaml(`name: demo
steps:
  - type: http
`)
    expect(d).toHaveLength(1)
    expect(d[0].message).toMatch(/id/)
  })

  it("allows needs pointing at a step declared later — order is not the DAG", () => {
    const d = yaml(`name: demo
steps:
  - id: a
    type: http
    needs:
      - b
  - id: b
    type: http
`)
    expect(d).toEqual([])
  })

  it("reports every problem, not just the first", () => {
    const d = yaml(`name: demo
steps:
  - id: a
    type: nope
  - id: a
    type: alsonope
`)
    expect(d.length).toBeGreaterThanOrEqual(3)
  })
})

describe("diagnose — JSON", () => {
  it("works the same on JSON input", () => {
    const d = diagnose('{"name":"x","steps":[{"id":"a","type":"htpp"}]}', "json")
    expect(d).toHaveLength(1)
    expect(d[0].message).toContain("htpp")
  })

  it("never throws on garbage", () => {
    expect(() => diagnose("", "json")).not.toThrow()
    expect(() => diagnose("@@@@", "yaml")).not.toThrow()
  })
})
