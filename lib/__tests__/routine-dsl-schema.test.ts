import { describe, it, expect } from "vitest"

import { stepKinds, stepKeys, topLevelKeys, keysForKind } from "@/lib/routine-dsl-schema"

// Completions are read out of schemas/routine.v1.json rather than
// retyped. The schema is generated from the Go structs, so a step kind
// added to internal/pipeline/types.go reaches the editor without anyone
// remembering to update a second list — and a list that has to be
// remembered is a list that goes stale.

describe("stepKinds", () => {
  const kinds = stepKinds()

  it("offers exactly the kinds the executor recognises", () => {
    expect(kinds.map((k) => k.kind).sort()).toEqual(
      [
        "agent_run",
        "call_pipeline",
        "code",
        "foreach",
        "http",
        "notify",
        "query",
        "script",
        "transform",
        "wait",
        "crewship",
      ].sort(),
    )
  })

  it("carries a one-line description for each", () => {
    for (const k of kinds) {
      expect(k.detail.length, `${k.kind} has no detail`).toBeGreaterThan(0)
    }
  })

  // The schema does not describe its own type enum, so the prose is
  // authored here while the LIST stays derived. That split only works
  // if drift is loud: add a kind to internal/pipeline/types.go,
  // regenerate the schema, and this reddens until someone writes the
  // sentence explaining what the new kind does.
  it("reddens when the schema gains a kind nobody has described", () => {
    const described = new Set(kinds.filter((k) => k.detail.length > 0).map((k) => k.kind))
    const inSchema = stepKinds().map((k) => k.kind)
    for (const kind of inSchema) {
      expect(described.has(kind), `step kind "${kind}" has no authored description`).toBe(true)
    }
  })
})

describe("stepKeys", () => {
  it("includes the fields every step has", () => {
    const keys = stepKeys().map((k) => k.key)
    expect(keys).toContain("id")
    expect(keys).toContain("type")
    expect(keys).toContain("needs")
  })

  it("leaves each kind's body object to keysForKind", () => {
    // `http`, `foreach`, `wait` … are step properties in the schema,
    // but offering all eight at step level buries the fields that
    // always apply under seven that never do for the kind being typed.
    const keys = stepKeys().map((k) => k.key)
    for (const body of ["http", "foreach", "wait", "query", "notify", "script", "code", "transform"]) {
      expect(keys, `${body} should be offered by keysForKind, not stepKeys`).not.toContain(body)
    }
  })

  it("marks id and type required", () => {
    const required = stepKeys().filter((k) => k.required).map((k) => k.key)
    expect(required.sort()).toEqual(["id", "type"])
  })
})

describe("keysForKind", () => {
  it("offers the body field a kind actually uses", () => {
    expect(keysForKind("http").map((k) => k.key)).toContain("method")
    expect(keysForKind("http").map((k) => k.key)).toContain("url")
    expect(keysForKind("foreach").map((k) => k.key)).toContain("items")
    expect(keysForKind("query").map((k) => k.key)).toContain("source")
  })

  it("does not offer another kind's fields", () => {
    expect(keysForKind("http").map((k) => k.key)).not.toContain("items")
  })

  it("returns nothing for a kind with no body object", () => {
    // agent_run's fields sit on the step itself, not in a sub-object.
    expect(keysForKind("agent_run")).toEqual([])
  })

  it("returns nothing for an unknown kind rather than throwing", () => {
    expect(() => keysForKind("nonsense")).not.toThrow()
    expect(keysForKind("nonsense")).toEqual([])
  })
})

describe("topLevelKeys", () => {
  it("offers the routine-level fields", () => {
    const keys = topLevelKeys().map((k) => k.key)
    expect(keys).toContain("name")
    expect(keys).toContain("steps")
    expect(keys).toContain("integrations_required")
    expect(keys).toContain("credentials_required")
  })

  it("does not leak the informational $schema pointer into completions", () => {
    expect(topLevelKeys().map((k) => k.key)).not.toContain("$schema")
  })
})
