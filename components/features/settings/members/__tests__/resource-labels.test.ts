import { describe, expect, it } from "vitest"

import { kindName, operationName } from "../resource-labels"
import { shareOperations } from "@/lib/resource-sharing"

/** The server stores bare ids (`discover`, `delete_issues`); people read what they let someone do. */
describe("operationName", () => {
  const OPERATIONS = {
    crew: ["view", "use", "manage"],
    agent: ["discover", "chat", "run", "delegate"],
    project: ["list", "read", "contribute", "manage", "delete_issues", "write", "delete"],
    routine: ["view", "results", "run", "edit", "publish"],
  } as const

  it("names every operation of every kind, and never leaves an id on screen", () => {
    for (const [kind, ops] of Object.entries(OPERATIONS)) {
      for (const op of ops) {
        const name = operationName(kind, op)
        expect(name, `${kind}:${op}`).not.toMatch(/[_.:]/)
        expect(name, `${kind}:${op}`).not.toBe(op)
      }
    }
  })

  it("covers the operations the sharing contract offers", () => {
    for (const [kind, ops] of Object.entries(shareOperations)) for (const op of ops) expect(OPERATIONS[kind as keyof typeof OPERATIONS]).toContain(op)
  })

  it("says the same id differently for different kinds", () => {
    expect(operationName("agent", "run")).toBe("Run it")
    expect(operationName("project", "manage")).toBe("Manage it")
    expect(operationName("agent", "discover")).toBe("Find it")
    expect(operationName("project", "delete_issues")).toBe("Delete its issues")
  })

  it("reads an operation nobody has named yet as words, not as an id", () => {
    expect(operationName("agent", "future_thing")).toBe("Future thing")
    expect(kindName("agent")).toBe("Agent")
    expect(kindName("workflow_x")).toBe("Workflow x")
  })
})
