import { describe, it, expect } from "vitest"

import { parseRoutineBuffer } from "@/lib/routine-buffer"

// The editor gained a YAML mode and kept a JSON-only save path: the
// live validity indicator parsed in the active format while Save called
// a second, older function that ran JSON.parse. So the header said
// "syntax ok", the buffer was valid YAML, and Save reported
//
//   Unexpected token 'c', "credential"... is not valid JSON
//
// Two functions doing the same job is how they came to disagree. There
// is one now, and both callers use it.

const YAML_DOC = `name: approval-gate-demo
dsl_version: "1.0"
steps:
  - id: draft
    type: agent_run
    agent_slug: morgan
`

const JSON_DOC = JSON.stringify(
  { name: "approval-gate-demo", dsl_version: "1.0", steps: [{ id: "draft", type: "agent_run" }] },
  null,
  2,
)

describe("parseRoutineBuffer — YAML", () => {
  it("accepts a YAML buffer in yaml mode", () => {
    const r = parseRoutineBuffer(YAML_DOC, "yaml")
    expect(r.ok).toBe(true)
    if (!r.ok) return
    expect(r.parsed.name).toBe("approval-gate-demo")
    expect((r.parsed.steps as unknown[]).length).toBe(1)
  })

  it("does not try to JSON.parse it", () => {
    // The exact failure that shipped.
    const r = parseRoutineBuffer(YAML_DOC, "yaml")
    if (r.ok) return
    expect(r.message).not.toMatch(/is not valid JSON/i)
  })
})

describe("parseRoutineBuffer — JSON", () => {
  it("accepts a JSON buffer in json mode", () => {
    const r = parseRoutineBuffer(JSON_DOC, "json")
    expect(r.ok).toBe(true)
  })

  it("reports the line of a syntax error", () => {
    const r = parseRoutineBuffer('{\n  "name": "x",\n  "steps": [,]\n}', "json")
    expect(r.ok).toBe(false)
    if (r.ok) return
    expect(r.message).toMatch(/line 3/)
  })
})

describe("parseRoutineBuffer — shape", () => {
  it("requires a name", () => {
    const r = parseRoutineBuffer("steps:\n  - id: a\n    type: http\n", "yaml")
    expect(r.ok).toBe(false)
    if (r.ok) return
    expect(r.message).toMatch(/name/)
  })

  it("requires a non-empty steps array", () => {
    expect(parseRoutineBuffer("name: x\nsteps: []\n", "yaml").ok).toBe(false)
    expect(parseRoutineBuffer("name: x\n", "yaml").ok).toBe(false)
  })

  it("accepts a routine that has both", () => {
    expect(parseRoutineBuffer(YAML_DOC, "yaml").ok).toBe(true)
  })
})
