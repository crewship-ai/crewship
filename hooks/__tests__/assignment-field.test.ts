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

  it("returns empty string for shape-less objects (no JSON dump)", () => {
    // Intentionally returns "" rather than JSON.stringify(v) — the result
    // lands in user-visible chat and may carry tokens/PII when the backend
    // shape changes. Better to render nothing than to leak a payload.
    expect(assignmentField({ foo: "bar" })).toBe("")
  })

  it("returns empty string for non-serializable (circular) objects", () => {
    const circular: Record<string, unknown> = {}
    circular.self = circular
    expect(assignmentField(circular)).toBe("")
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
