import { describe, it, expect, vi } from "vitest"
import { fireEvent, render, screen } from "@testing-library/react"
import { useState } from "react"
vi.mock("../routine-definition-canvas", () => ({
  RoutineDefinitionCanvas: () => <div>Interactive graph</div>,
}))
import { RoutineRecipeSteps, recipeCondition, recipeStepSummary } from "../routine-recipe-steps"
const definition = {
  name: "demo",
  inputs: [{ name: "scenario", label: "Demo scenario" }],
  steps: [
    {
      id: "request_approval",
      type: "wait",
      if: "inputs.scenario == 'Approval'",
      needs: ["prepare"],
      timeout_seconds: 300,
      wait: {
        kind: "approval",
        approval_title: "Review the result",
        approval_prompt: "Check it",
        unknown_option: true,
      },
    },
  ],
}
describe("readable recipe steps", () => {
  it("explains real actions and input conditions without exposing only identifiers", () => {
    expect(recipeStepSummary(definition.steps[0])).toBe("Review the result")
    expect(
      recipeStepSummary({
        type: "transform",
        transform: { input: "{{ inputs.a }} {{ inputs.b }} {{ inputs.c }}" },
      }),
    ).toBe("Prepare data from 3 start-form answers")
    expect(recipeCondition(definition.steps[0], definition.inputs)).toBe("Demo scenario: Approval")
    expect(recipeCondition({ if: "size(inputs.items) > 1" }, [])).toBe("Conditional step")
    render(
      <RoutineRecipeSteps
        definition={definition}
        slug="demo"
        name="Demo"
        onChange={vi.fn()}
        onOpenCode={vi.fn()}
      />,
    )
    expect(screen.getByText("Request approval")).toBeInTheDocument()
    expect(screen.getByText("Demo scenario: Approval")).toBeInTheDocument()
    expect(screen.queryByText("Interactive graph")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Graph", exact: true }))
    expect(screen.getByText("Interactive graph")).toBeInTheDocument()
  })
  it("edits the selected step while preserving conditions, dependencies and unknown fields", () => {
    const save = vi.fn()
    function Harness() {
      const [value, setValue] = useState<Record<string, unknown>>(definition)
      return (
        <RoutineRecipeSteps
          definition={value}
          slug="demo"
          name="Demo"
          onOpenCode={vi.fn()}
          onChange={(next) => {
            setValue(next)
            save(next)
          }}
        />
      )
    }
    render(<Harness />)
    fireEvent.click(screen.getByRole("button", { name: /Request approval/ }))
    fireEvent.change(screen.getByLabelText("Approval title"), {
      target: { value: "Approve the delivery" },
    })
    const next = save.mock.calls.at(-1)![0]
    expect(next.steps[0]).toEqual({
      ...definition.steps[0],
      wait: { ...definition.steps[0].wait, approval_title: "Approve the delivery" },
    })
    expect(definition.steps[0].wait.approval_title).toBe("Review the result")
  })
  it("selects an upstream result without rewriting step identity or advanced options", () => {
    const save = vi.fn()
    const target = {
      id: "prepare",
      type: "transform",
      unknown: true,
      transform: { input: "custom", expression: ".", custom_setting: 42 },
    }
    render(
      <RoutineRecipeSteps
        definition={{
          steps: [{ id: "fetch", type: "http", http: { url: "https://example.com" } }, target],
        }}
        slug="demo"
        name="Demo"
        onChange={save}
        onOpenCode={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: /Prepare.*Prepare data/ }))
    fireEvent.change(screen.getByLabelText("Data source"), {
      target: { value: "{{ steps.fetch.output }}" },
    })
    expect(save.mock.calls.at(-1)![0].steps[1]).toEqual({
      ...target,
      transform: { ...target.transform, input: "{{ steps.fetch.output }}" },
    })
  })
})

it("inserts data into an agent prompt without erasing instructions or changing scheduling", () => {
  const save = vi.fn()
  const target = {
    id: "summarize",
    type: "agent_run",
    prompt: "Keep the summary short.",
    agent_slug: "writer",
    custom: true,
  }
  render(
    <RoutineRecipeSteps
      definition={{ inputs: [{ name: "request", type: "string" }], steps: [target] }}
      slug="demo"
      name="Demo"
      onChange={save}
      onOpenCode={vi.fn()}
    />,
  )
  fireEvent.click(screen.getByRole("button", { name: /Summarize/ }))
  fireEvent.change(screen.getByLabelText("Insert data into instructions"), {
    target: { value: "{{ inputs.request }}" },
  })
  expect(save.mock.calls.at(-1)![0].steps[0]).toEqual({
    ...target,
    prompt: "Keep the summary short.\n{{ inputs.request }}",
  })
})

it("changes one child routine binding while preserving typed literals and step configuration", () => {
  const save = vi.fn()
  const target = {
    id: "child",
    type: "call_pipeline",
    pipeline_slug: "delivery",
    inputs: { count: 1, enabled: false, options: { preserve: true } },
    custom: true,
  }
  render(
    <RoutineRecipeSteps
      definition={{ inputs: [{ name: "count", type: "integer" }], steps: [target] }}
      slug="demo"
      name="Demo"
      onChange={save}
      onOpenCode={vi.fn()}
    />,
  )
  fireEvent.click(screen.getByRole("button", { name: /Child.*Run another routine/ }))
  expect(save).not.toHaveBeenCalled()
  fireEvent.change(screen.getByLabelText("Source for count"), {
    target: { value: "{{ inputs.count }}" },
  })
  expect(save.mock.calls.at(-1)![0].steps[0]).toEqual({
    ...target,
    inputs: { ...target.inputs, count: "{{ inputs.count }}" },
  })
  expect(screen.getAllByText(/input schema determines the value type/)).toHaveLength(3)
})
