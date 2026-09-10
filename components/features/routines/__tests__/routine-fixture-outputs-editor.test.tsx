import { useState } from "react"
import { fireEvent, render, screen } from "@testing-library/react"
import { expect, it, vi } from "vitest"
import { RoutineFixtureOutputsEditor } from "../routine-fixture-outputs-editor"
const step = { transform: { input: "{{ steps.fetch.output }}" } }
const steps = [{ id: "fetch", name: "Customer data" }]

it("requires an explicit sample and returns to missing when it is removed", () => {
  const save = vi.fn()
  function Harness() {
    const [value, setValue] = useState("{}")
    return (
      <RoutineFixtureOutputsEditor
        value={value}
        onChange={(next) => {
          setValue(next)
          save(next)
        }}
        step={step}
        steps={steps}
        disabled={false}
      />
    )
  }
  render(<Harness />)
  expect(save).not.toHaveBeenCalled()
  expect(screen.getByText("Not provided")).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Add sample for Customer data" }))
  expect(JSON.parse(save.mock.calls.at(-1)![0])).toEqual({ fetch: "" })
  expect(screen.getByLabelText("Customer data")).toHaveFocus()
  fireEvent.change(screen.getByLabelText("Customer data"), { target: { value: "0" } })
  expect(JSON.parse(save.mock.calls.at(-1)![0])).toEqual({ fetch: "0" })
  fireEvent.click(screen.getByRole("button", { name: "Remove sample" }))
  expect(JSON.parse(save.mock.calls.at(-1)![0])).toEqual({})
})

it("preserves malformed JSON for repair instead of overwriting it", () => {
  const save = vi.fn()
  render(
    <RoutineFixtureOutputsEditor
      value="{broken"
      onChange={save}
      step={step}
      steps={steps}
      disabled={false}
    />,
  )
  expect(screen.getByRole("alert")).toHaveTextContent("Enter a JSON object")
  expect(screen.getByLabelText("Sample results · JSON")).toHaveValue("{broken")
  expect(save).not.toHaveBeenCalled()
})
