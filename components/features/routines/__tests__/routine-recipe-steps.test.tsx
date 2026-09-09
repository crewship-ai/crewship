import { describe, it, expect, vi } from "vitest"
import { fireEvent, render, screen } from "@testing-library/react"
import { useState } from "react"
vi.mock("../routine-definition-canvas", () => ({ RoutineDefinitionCanvas: () => <div>Interactive graph</div> }))
import { RoutineRecipeSteps, recipeCondition, recipeStepSummary } from "../routine-recipe-steps"
const definition = { name: "demo", inputs: [{ name: "scenario", label: "Demo scenario" }], steps: [{ id: "request_approval", type: "wait", if: "inputs.scenario == 'Approval'", needs: ["prepare"], timeout_seconds: 300, wait: { kind: "approval", approval_title: "Review the result", approval_prompt: "Check it", unknown_option: true } }] }
describe("readable recipe steps", () => {
  it("explains real actions and input conditions without exposing only identifiers", () => {
    expect(recipeStepSummary(definition.steps[0])).toBe("Review the result")
    expect(recipeStepSummary({type: "transform", transform: { input: "{{ inputs.a }} {{ inputs.b }} {{ inputs.c }}" }})).toBe("Prepare data from 3 start-form answers")
    expect(recipeCondition(definition.steps[0], definition.inputs)).toBe("Demo scenario: Approval")
    expect(recipeCondition({ if: "size(inputs.items) > 1" }, [])).toBe("Conditional step")
    render(<RoutineRecipeSteps definition={definition} slug="demo" name="Demo" onChange={vi.fn()} onOpenCode={vi.fn()} />)
    expect(screen.getByText("Request approval")).toBeInTheDocument()
    expect(screen.getByText("Demo scenario: Approval")).toBeInTheDocument()
    expect(screen.queryByText("Interactive graph")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Graph", exact: true }))
    expect(screen.getByText("Interactive graph")).toBeInTheDocument()
  })
  it("edits the selected step while preserving conditions, dependencies and unknown fields", () => {
    const save = vi.fn()
    function Harness() { const [value, setValue] = useState<Record<string, unknown>>(definition); return <RoutineRecipeSteps definition={value} slug="demo" name="Demo" onOpenCode={vi.fn()} onChange={next => { setValue(next); save(next) }} /> }
    render(<Harness />)
    fireEvent.click(screen.getByRole("button", { name: /Request approval/ }))
    fireEvent.change(screen.getByLabelText("Approval title"), { target: { value: "Approve the delivery" } })
    const next = save.mock.calls.at(-1)![0]
    expect(next.steps[0]).toEqual({ ...definition.steps[0], wait: { ...definition.steps[0].wait, approval_title: "Approve the delivery" } })
    expect(definition.steps[0].wait.approval_title).toBe("Review the result")
  })
})
