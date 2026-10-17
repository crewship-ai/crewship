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
})
