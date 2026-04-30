import { describe, it, expect } from "vitest"
import { assignmentField } from "@/hooks/use-chat"

describe("assignmentField", () => {
  it("returns string values verbatim", () => {
    expect(assignmentField("viktor")).toBe("viktor")
  })

  it("renders objects via slug when present (avoids '[object Object]')", () => {
    expect(assignmentField({ slug: "viktor", id: "agent_1" })).toBe("viktor")
  })

  it("falls back to name then id", () => {
    expect(assignmentField({ name: "Viktor", id: "agent_1" })).toBe("Viktor")
    expect(assignmentField({ id: "agent_1" })).toBe("agent_1")
  })

  it("falls back to JSON for shape-less objects", () => {
    expect(assignmentField({ foo: "bar" })).toBe('{"foo":"bar"}')
  })

  it("handles null / undefined as empty string", () => {
    expect(assignmentField(null)).toBe("")
    expect(assignmentField(undefined)).toBe("")
  })

  it("never returns the literal '[object Object]'", () => {
    const cases: unknown[] = [
      "ok",
      { slug: "viktor" },
      { name: "Viktor" },
      { id: "agent_1" },
      { foo: "bar" },
      null,
      undefined,
      42,
      true,
    ]
    for (const c of cases) {
      expect(assignmentField(c)).not.toBe("[object Object]")
    }
  })
})
