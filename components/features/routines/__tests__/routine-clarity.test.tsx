import { fireEvent, render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { InputsForm } from "../routine-run-inputs-dialog"
import { RoutineBehaviorSummary } from "../routine-behavior"

const inputs = [
  {
    name: "coverage",
    label: "Coverage",
    type: "number",
    widget: "number",
    min: 0,
    max: 1,
    default: 0.7,
  },
  {
    name: "count",
    label: "Count",
    type: "integer",
    widget: "number",
    default: 1,
  },
  {
    name: "dir",
    label: "Directory",
    type: "string",
    format: "absolute_path",
    default: "/crew/shared/project",
  },
]
describe("routine input contracts", () => {
  it("shows bounds, validates all fields on blur/submit and sends typed values", () => {
    const onRun = vi.fn()
    render(<InputsForm inputs={inputs} onRun={onRun} />)
    const coverage = screen.getByLabelText("Coverage")
    expect(coverage).toHaveAttribute("step", "any")
    expect(coverage).toHaveAttribute("min", "0")
    expect(coverage).toHaveAttribute("max", "1")
    expect(screen.getByText(/From 0 to 1, inclusive/)).toBeInTheDocument()
    fireEvent.change(coverage, { target: { value: "70" } })
    fireEvent.blur(coverage)
    expect(coverage).toHaveAttribute("aria-invalid", "true")
    fireEvent.change(screen.getByLabelText("Count"), {
      target: { value: "1.5" },
    })
    fireEvent.change(screen.getByLabelText("Directory"), {
      target: { value: "../private" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Run now" }))
    expect(onRun).not.toHaveBeenCalled()
    // Three field errors plus the summary that counts them.
    expect(screen.getAllByRole("alert")).toHaveLength(4)
    expect(screen.getByTestId("routine-input-error-summary")).toHaveTextContent("3 answers need a fix")
    expect(coverage).toHaveFocus()
    fireEvent.change(coverage, { target: { value: "0.7" } })
    fireEvent.change(screen.getByLabelText("Count"), {
      target: { value: "0" },
    })
    fireEvent.change(screen.getByLabelText("Directory"), {
      target: { value: "/crew/shared/valid" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Run now" }))
    expect(onRun).toHaveBeenCalledWith({
      coverage: 0.7,
      count: 0,
      dir: "/crew/shared/valid",
    })
  })
  it("restores recipe defaults rather than the saved preset and preserves false", () => {
    const onRun = vi.fn()
    render(
      <InputsForm
        inputs={[inputs[0], { name: "enabled", type: "boolean", default: true }]}
        initialInputs={{ coverage: 0.2, enabled: false }}
        onRun={onRun}
      />,
    )
    expect(screen.getByLabelText("Coverage")).toHaveValue(0.2)
    fireEvent.click(screen.getAllByRole("button", { name: "Restore default" })[0])
    expect(screen.getByLabelText("Coverage")).toHaveValue(0.7)
    fireEvent.click(screen.getByRole("button", { name: "Run now" }))
    expect(onRun).toHaveBeenCalledWith({ coverage: 0.7, enabled: false })
  })
  it("disables input changes while submitting", () => {
    render(<InputsForm inputs={inputs} onRun={vi.fn()} submitting />)
    expect(screen.getByLabelText("Coverage")).toBeDisabled()
  })
})
it("renders server-derived rules without turning configuration into a passing verdict", () => {
  render(
    <RoutineBehaviorSummary
      behavior={{
        scope: "Configured rules, not recorded verdicts",
        cost: "No recipe cap",
        steps: [
          {
            id: "a",
            name: "Extract",
            performer: "Agent: worker",
            checks: ["Required checker: judge"],
            failure: "Stop on rejection",
            attempts: "Two tiers",
            timeout: "30 seconds",
          },
        ],
      }}
    />,
  )
  fireEvent.click(screen.getByText(/How this routine works/))
  expect(screen.getByText("Required checker: judge")).toBeVisible()
  expect(screen.getByText(/Stop on rejection/)).toBeVisible()
  expect(screen.queryByText("Passed")).not.toBeInTheDocument()
})
