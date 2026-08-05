import { describe, it, expect } from "vitest"

import { STARTER_TEMPLATES } from "../routine-create-dialog"
import { parseRoutineBuffer } from "@/lib/routine-buffer"
import { diagnose } from "@/lib/routine-dsl-diagnostics"
import { toYaml } from "@/lib/routine-dsl-format"

// The starter templates are a third copy of DSL knowledge, next to
// schemas/routine.v1.json and lib/routine-dsl-schema.ts. Nothing
// checked them, so a template that stopped matching the schema would
// ship silently — and it is the first thing a new author sees, so it
// would teach the wrong shape before anything else got a chance to.
//
// These run every template through the SAME validator the editor uses.
// A template that cannot pass the editor's own check has no business
// being the thing the editor opens with.

describe("starter templates", () => {
  it("ships at least one", () => {
    expect(STARTER_TEMPLATES.length).toBeGreaterThan(0)
  })

  it.each(STARTER_TEMPLATES.map((t) => [t.id, t] as const))(
    "%s parses as a routine",
    (_id, tpl) => {
      const json = JSON.stringify(tpl.json, null, 2)
      const parsed = parseRoutineBuffer(json, "json")
      expect(parsed.ok, parsed.ok ? "" : parsed.message).toBe(true)
    },
  )

  it.each(STARTER_TEMPLATES.map((t) => [t.id, t] as const))(
    "%s survives the round trip into YAML the editor authors in",
    (_id, tpl) => {
      // The editor's buffer is YAML. A template that only holds
      // together as JSON would break the moment it was opened.
      const yaml = toYaml(tpl.json)
      const parsed = parseRoutineBuffer(yaml, "yaml")
      expect(parsed.ok, parsed.ok ? "" : parsed.message).toBe(true)
    },
  )

  it.each(STARTER_TEMPLATES.map((t) => [t.id, t] as const))(
    "%s raises no schema diagnostics",
    (_id, tpl) => {
      // Not just parseable — clean. A template that opens with a
      // squiggle on line 3 reads as "this tool is broken", not as
      // "fill this in".
      const problems = diagnose(toYaml(tpl.json), "yaml")
      expect(problems.map((p) => p.message)).toEqual([])
    },
  )

  it("has a unique id per template, since applying one is keyed on it", () => {
    const ids = STARTER_TEMPLATES.map((t) => t.id)
    expect(new Set(ids).size).toBe(ids.length)
  })
})
