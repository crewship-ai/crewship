import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"

import { WizardSteps } from "../wizard-steps"

const STEPS = ["Basics", "When", "Steps", "Limits", "Test and save"]

describe("WizardSteps", () => {
  it("marks done, current and upcoming steps", () => {
    render(<WizardSteps steps={STEPS} current={2} />)
    const items = screen.getAllByRole("listitem")
    expect(items.map((li) => li.getAttribute("data-state"))).toEqual(["done", "done", "current", "todo", "todo"])
    expect(screen.getByText("Steps").closest("[aria-current]")).toHaveAttribute("aria-current", "step")
    expect(screen.getByText("3 / 5")).toBeInTheDocument()
  })

  it("goes back to an earlier step but not forward unless allowed", () => {
    const go = vi.fn()
    const { rerender } = render(<WizardSteps steps={STEPS} current={2} onStepChange={go} />)
    fireEvent.click(screen.getByRole("button", { name: "Step 1: Basics" }))
    expect(go).toHaveBeenCalledWith(0)
    expect(screen.queryByRole("button", { name: "Step 4: Limits" })).toBeNull()
    rerender(<WizardSteps steps={STEPS} current={2} onStepChange={go} freeNavigation />)
    fireEvent.click(screen.getByRole("button", { name: "Step 4: Limits" }))
    expect(go).toHaveBeenLastCalledWith(3)
  })

  // COM-07 (#3481): past the last step (a wizard's "all set" screen) the bar
  // read "4 / 3" on a phone.
  it("shows every step done once the wizard is finished, never a step past the last", () => {
    const steps = ["Workspace", "Runner", "Pair the CLI"]
    const go = vi.fn()
    render(<WizardSteps steps={steps} current={3} onStepChange={go} />)
    expect(screen.getAllByRole("listitem").map((li) => li.getAttribute("data-state"))).toEqual(["done", "done", "done"])
    expect(screen.getByRole("navigation", { name: "Steps" })).toHaveAttribute("data-complete")
    expect(screen.queryByText("4 / 3")).toBeNull()
    expect(screen.getByText("3 / 3 · Done")).toBeInTheDocument()
    // Done steps can still be revisited.
    fireEvent.click(screen.getByRole("button", { name: "Step 3: Pair the CLI" }))
    expect(go).toHaveBeenCalledWith(2)
  })

  it("clamps a current index far past the end to the finished state", () => {
    render(<WizardSteps steps={["A", "B"]} current={7} />)
    expect(screen.getByText("2 / 2 · Done")).toBeInTheDocument()
  })
})
