import { useState } from "react"
import { fireEvent, render, screen } from "@testing-library/react"
import { expect, it, vi } from "vitest"
import { RoutineDecisionFormBuilder } from "../routine-decision-form-builder"
import type { DecisionForm } from "@/lib/decision-form"

it("customizes approval only after an explicit choice and preserves stable action IDs", () => {
  const save = vi.fn()
  function Harness() {
    const [value, setValue] = useState<DecisionForm>()
    return (
      <RoutineDecisionFormBuilder
        value={value}
        onChange={(next) => {
          setValue(next)
          save(next)
        }}
        onOpenCode={vi.fn()}
      />
    )
  }
  render(<Harness />)
  expect(save).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button", { name: "Customize decision" }))
  expect(save.mock.calls.at(-1)![0].fields).toEqual([])
  fireEvent.change(screen.getByLabelText("Decision 1"), { target: { value: "Send to customer" } })
  expect(save.mock.calls.at(-1)![0].actions[0]).toEqual({
    id: "continue",
    label: "Send to customer",
    approved: true,
  })
  fireEvent.change(screen.getByLabelText("Outcome of decision 1"), { target: { value: "stop" } })
  expect(save.mock.calls.at(-1)![0].actions[0].approved).toBe(false)
  expect(screen.getByRole("button", { name: "Remove decision 1" })).toBeDisabled()
})

it("retains unknown form configuration while editing reviewer questions", () => {
  const save = vi.fn()
  const value = {
    fields: [],
    actions: [
      { id: "yes", label: "Yes", approved: true },
      { id: "no", label: "No", approved: false },
    ],
    future: { keep: true },
  }
  render(<RoutineDecisionFormBuilder value={value} onChange={save} onOpenCode={vi.fn()} />)
  fireEvent.click(screen.getByRole("button", { name: "+ Add question" }))
  expect(save.mock.calls[0][0]).toMatchObject({
    ...value,
    fields: [{ name: "input_1", type: "string" }],
  })
})

it("keeps unrecognized forms intact and directs the author to Code", () => {
  const save = vi.fn()
  const open = vi.fn()
  render(
    <RoutineDecisionFormBuilder value={{ future_form: true }} onChange={save} onOpenCode={open} />,
  )
  fireEvent.click(screen.getByRole("button", { name: "Review in Code" }))
  expect(save).not.toHaveBeenCalled()
  expect(open).toHaveBeenCalledOnce()
})
