import { describe, it, expect } from "vitest"

import { parseDsl, toYaml, convertDsl } from "@/lib/routine-dsl-format"

// The editor authors YAML and the server stores canonical JSON — the
// same split internal/pipeline/parse_yaml.go already makes for the CLI
// (#1423). These cover the conversion in both directions.
//
// The reason for YAML is not fewer braces. It is `prompt: |` — the
// production accounting routine carries 600-character prompts as one
// JSON line of \n escapes, and nobody can read or edit that.

describe("toYaml", () => {
  it("writes a multiline string as a block scalar, not escapes", () => {
    const y = toYaml({ prompt: "first line\nsecond line\nthird" })
    expect(y).toContain("prompt: |")
    expect(y).toContain("first line")
    // The failure this exists to prevent: \n surviving as two characters.
    expect(y).not.toContain("\\n")
  })

  it("keeps a single-line string inline", () => {
    expect(toYaml({ id: "parse" })).toBe("id: parse\n")
  })

  it("renders nested structure without JSON punctuation", () => {
    const y = toYaml({ steps: [{ id: "a", needs: ["b"] }] })
    expect(y).not.toContain("{")
    expect(y).not.toContain('"')
    expect(y).toContain("- id: a")
  })
})

describe("parseDsl — YAML", () => {
  it("parses a document into a plain object", () => {
    const r = parseDsl("name: demo\nsteps:\n  - id: a\n    type: http\n", "yaml")
    expect(r.ok).toBe(true)
    if (!r.ok) return
    expect(r.value.name).toBe("demo")
    expect((r.value.steps as unknown[]).length).toBe(1)
  })

  it("does NOT turn `no` into false — YAML 1.2, not 1.1", () => {
    // The Norway problem: under YAML 1.1 (js-yaml's default) a step id
    // or a country code of `no` silently becomes the boolean false.
    const r = parseDsl("id: no\nregion: NO\n", "yaml")
    expect(r.ok).toBe(true)
    if (!r.ok) return
    expect(r.value.id).toBe("no")
    expect(r.value.region).toBe("NO")
  })

  it("reports the line of a syntax error", () => {
    const r = parseDsl("name: demo\nsteps:\n\t- id: a\n", "yaml")
    expect(r.ok).toBe(false)
    if (r.ok) return
    expect(r.message.length).toBeGreaterThan(0)
    expect(r.line).toBe(3)
  })

  it("rejects a document that is not a mapping", () => {
    const r = parseDsl("- just\n- a list\n", "yaml")
    expect(r.ok).toBe(false)
  })
})

describe("parseDsl — JSON", () => {
  it("parses valid JSON", () => {
    const r = parseDsl('{"name":"demo","steps":[]}', "json")
    expect(r.ok).toBe(true)
  })

  it("reports the line of a syntax error", () => {
    const r = parseDsl('{\n  "name": "demo",\n  "steps": [,]\n}', "json")
    expect(r.ok).toBe(false)
    if (r.ok) return
    expect(r.line).toBe(3)
  })

  it("rejects valid JSON that is not an object", () => {
    expect(parseDsl("[1,2,3]", "json").ok).toBe(false)
    expect(parseDsl('"a string"', "json").ok).toBe(false)
  })
})

describe("convertDsl", () => {
  const json = JSON.stringify(
    {
      name: "mesicni",
      steps: [
        { id: "a", type: "agent_run", prompt: "line one\nline two" },
        { id: "b", type: "http", needs: ["a"], http: { method: "GET", url: "https://x.test" } },
      ],
    },
    null,
    2,
  )

  it("round-trips JSON → YAML → JSON without changing the value", () => {
    const toY = convertDsl(json, "json", "yaml")
    expect(toY.ok).toBe(true)
    if (!toY.ok) return
    const back = convertDsl(toY.text, "yaml", "json")
    expect(back.ok).toBe(true)
    if (!back.ok) return
    expect(JSON.parse(back.text)).toEqual(JSON.parse(json))
  })

  it("survives the multiline prompt through both directions", () => {
    const toY = convertDsl(json, "json", "yaml")
    if (!toY.ok) throw new Error("expected yaml")
    expect(toY.text).toContain("prompt: |")
    const back = convertDsl(toY.text, "yaml", "json")
    if (!back.ok) throw new Error("expected json")
    const steps = JSON.parse(back.text).steps
    expect(steps[0].prompt).toBe("line one\nline two")
  })

  it("is a no-op when the formats match", () => {
    const same = convertDsl(json, "json", "json")
    expect(same.ok).toBe(true)
    if (!same.ok) return
    expect(same.text).toBe(json)
  })

  it("passes the error through instead of producing half a document", () => {
    const bad = convertDsl("steps:\n\t- broken\n", "yaml", "json")
    expect(bad.ok).toBe(false)
  })
})
