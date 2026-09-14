import { describe, expect, it } from "vitest"
import { routinePublicationChanges } from "../routine-publication-changes"

describe("publication review", () => {
  it("matches stable IDs, identifies removals and ignores object key order", () => {
    const before = { steps: [{ id: "a", prompt: "old" }, { id: "b", prompt: "same" }, { id: "c" }] }
    const after = { steps: [{ prompt: "same", id: "b" }, { id: "a", prompt: "new" }, { id: "d" }] }
    expect(routinePublicationChanges(before, after).groups[0]).toMatchObject({
      added: ["d"],
      changed: ["a"],
      removed: ["c"],
      reordered: true,
    })
  })
  it("keeps false, zero, absent and null distinct without returning their values", () => {
    const delta = routinePublicationChanges(
      {
        inputs: [
          { name: "flag", default: false },
          { name: "count", default: 0 },
        ],
      },
      {
        inputs: [{ name: "flag", default: null }, { name: "count" }],
        secret_setting: "private-secret",
      },
    )
    expect(delta.groups[1].changed).toEqual(["flag", "count"])
    expect(delta.settings).toEqual(["secret_setting"])
    expect(JSON.stringify(delta)).not.toContain("private-secret")
  })
  it("does not claim a complete comparison for duplicate IDs or unfamiliar schemas", () => {
    expect(
      routinePublicationChanges({}, { steps: [{ id: "a" }, { id: "a" }], outputs: "custom" })
        .groups.filter((g) => !g.readable)
        .map((g) => g.label),
    ).toEqual(["Workflow steps", "Expected results"])
  })
})
