import { describe, expect, it } from "vitest"
import { routineDataSources } from "../routine-data-sources"
describe("routine data sources", () => {
  it("excludes the selected step and descendants including implicit template dependencies", () => {
    const sources = routineDataSources(
      {
        inputs: [{ name: "count", type: "integer" }],
        steps: [
          { id: "before" },
          { id: "target" },
          { id: "after", needs: ["target"] },
          { id: "implicit", transform: { input: "{{ steps.after.output }}" } },
        ],
      },
      "target",
    )
    expect(sources.map((s) => s.value)).toEqual(["{{ inputs.count }}", "{{ steps.before.output }}"])
    expect(sources[0].label).toContain("integer")
  })
  it("does not manufacture expressions for unsupported identifiers", () => {
    expect(
      routineDataSources({ inputs: [{ name: "a.b" }], steps: [{ id: "bad-id" }] }, "target"),
    ).toEqual([])
  })
})

import { routineSourcePatch } from "../routine-data-sources"

it("keeps linear order and never opts into parallelism when choosing a source", () => {
  const target = { id: "target", type: "transform", transform: { input: "old", expression: "." } }
  const definition = { steps: [{ id: "before" }, target, { id: "later" }] }
  const sources = routineDataSources(definition, "target")
  expect(sources.map((s) => s.stepId)).toEqual(["before"])
  expect(routineSourcePatch(definition, target, sources[0], "input", "transform")).toEqual({
    transform: { input: "{{ steps.before.output }}", expression: "." },
  })
})
it("retains all automatic dependencies instead of replacing them with one explicit edge", () => {
  const target = { id: "target", prompt: "Use {{ steps.before.output }}", type: "agent_run" }
  const definition = {
    parallelism: "auto",
    steps: [
      { id: "before" },
      target,
      { id: "other" },
      { id: "descendant", if: "steps.target == 'yes'" },
    ],
  }
  const sources = routineDataSources(definition, "target")
  expect(sources.some((s) => s.stepId === "descendant")).toBe(false)
  const chosen = sources.find((s) => s.stepId === "other")!
  expect(routineSourcePatch(definition, target, chosen, "prompt", undefined, true)).toEqual({
    prompt: "Use {{ steps.before.output }}\n{{ steps.other.output }}",
  })
  const explicit = { ...target, needs: ["before"] }
  expect(
    routineSourcePatch(
      { ...definition, steps: [definition.steps[0], explicit, definition.steps[2]] },
      explicit,
      chosen,
      "prompt",
    ).needs,
  ).toEqual(["before", "other"])
})
it("offers only declared list sources and safe projected properties for foreach", () => {
  const sources = routineDataSources(
    {
      inputs: [
        { name: "names", type: "array" },
        { name: "name", type: "string" },
      ],
      steps: [
        {
          id: "fetch",
          validation: {
            schema: {
              type: "object",
              properties: {
                items: { type: "array" },
                count: { type: "integer" },
                "bad.key": { type: "array" },
              },
            },
          },
        },
        { id: "loop" },
      ],
    },
    "loop",
    ["array"],
  )
  expect(sources.map((s) => s.value)).toEqual([
    "{{ inputs.names }}",
    "{{ steps.fetch.output.items }}",
  ])
})
