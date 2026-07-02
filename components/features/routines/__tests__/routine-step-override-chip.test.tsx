import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { StepOverrideChip } from "@/components/features/routines/routine-step-override-chip"

describe("StepOverrideChip", () => {
  it("renders 'model: <tier>' when a model override is set", () => {
    render(<StepOverrideChip override={{ step_id: "s1", model_override: "haiku" }} />)
    expect(screen.getByText("model: haiku")).toBeInTheDocument()
  })

  it("renders 'prompt override' when only the prompt is overridden", () => {
    render(<StepOverrideChip override={{ step_id: "s1", prompt: "be terser" }} />)
    expect(screen.getByText("prompt override")).toBeInTheDocument()
  })

  it("prefers the model label when both prompt and model are overridden", () => {
    render(<StepOverrideChip override={{ step_id: "s1", prompt: "be terser", model_override: "opus" }} />)
    expect(screen.getByText("model: opus")).toBeInTheDocument()
  })

  it("renders nothing when override is undefined", () => {
    const { container } = render(<StepOverrideChip />)
    expect(container).toBeEmptyDOMElement()
  })

  it("renders nothing when override has neither field set", () => {
    const { container } = render(<StepOverrideChip override={{ step_id: "s1" }} />)
    expect(container).toBeEmptyDOMElement()
  })
})
