import { useState } from "react"
import { render, screen, fireEvent, within } from "@testing-library/react"
import { describe, it, expect } from "vitest"
import { RoutineInputFormBuilder } from "../routine-input-form-builder"
import { type RoutineInputSpec, slashFieldsFromRoutineInputs, routineInputsFromValues, isMissingRequired } from "@/lib/routine-inputs"
import { FormField } from "@/components/features/chat/asks/form-field"

function Builder() {
  const [inputs, setInputs] = useState<RoutineInputSpec[]>([])
  return <><RoutineInputFormBuilder inputs={inputs} onChange={setInputs} /><output data-testid="definition">{JSON.stringify(inputs)}</output></>
}

describe("Routine input surveys", () => {
  it("authors a multiple-choice question with a typed default and variable", () => {
    render(<Builder />)
    fireEvent.click(screen.getByRole("button", { name: "+ Add question" }))
    fireEvent.change(screen.getByLabelText("Question"), { target: { value: "Which teams?" } })
    fireEvent.change(screen.getByLabelText("Variable name"), { target: { value: "teams" } })
    fireEvent.change(screen.getByLabelText("Answer type"), { target: { value: "multi" } })
    fireEvent.change(screen.getByLabelText("Prepared answers — one per line"), { target: { value: "Support\nResearch, Europe" } })
    fireEvent.click(within(screen.getByRole("group", { name: "Default answer (optional)" })).getByLabelText("Support"))
    const [input] = JSON.parse(screen.getByTestId("definition").textContent!)
    expect(input).toMatchObject({ name: "teams", label: "Which teams?", type: "array", widget: "multiselect", options: ["Support", "Research, Europe"], default: ["Support"] })
    expect(screen.getByText(/inputs.teams/)).toBeInTheDocument()
  })

  it("preserves comma-containing choices as JSON through the shared field", () => {
    const field = slashFieldsFromRoutineInputs([{ name: "teams", type: "array", options: ["A,B", "C"], required: true }])[0]
    function Field() { const [value, setValue] = useState("[]"); return <><FormField field={field} value={value} onChange={e => setValue(e.target.value)} /><output data-testid="value">{value}</output></> }
    render(<Field />)
    fireEvent.click(screen.getByLabelText("A,B"))
    const raw = screen.getByTestId("value").textContent!
    expect(routineInputsFromValues([field], { teams: raw })).toEqual({ teams: ["A,B"] })
    expect(isMissingRequired(field, "[]")).toBe(true)
    expect(() => routineInputsFromValues([field], { teams: '["unknown"]' })).toThrow("available answer")
  })

  it("offers custom text only when the recipe permits it", () => {
    const input = { name: "period", type: "string", options: ["Monthly"], allow_custom: true }
    const field = slashFieldsFromRoutineInputs([input])[0]
    render(<FormField field={field} value="" onChange={() => {}} />)
    expect(screen.getByRole("combobox")).toHaveAttribute("list", "period-choices")
    expect(routineInputsFromValues([field], { period: "My custom period" })).toEqual({ period: "My custom period" })
    expect(() => routineInputsFromValues([{ ...field, allow_custom: false }], { period: "My custom period" })).toThrow()
  })
})
