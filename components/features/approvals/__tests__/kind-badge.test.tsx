import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { KindBadge } from "@/components/features/approvals/kind-badge"

describe("KindBadge", () => {
  it("replaces underscores with spaces in the displayed label", () => {
    render(<KindBadge kind="destructive_op" />)
    expect(screen.getByText("destructive op")).toBeInTheDocument()
  })

  it.each([
    ["destructive_op", "destructive op", "text-red-300"],
    ["cost_threshold", "cost threshold", "text-amber-300"],
    ["target_environment", "target environment", "text-orange-300"],
    ["tool_call", "tool call", "text-blue-300"],
    ["custom", "custom", "text-slate-300"],
  ])("kind=%s renders label %q with %s class", (kind, label, classFragment) => {
    const { container } = render(<KindBadge kind={kind} />)
    expect(screen.getByText(label)).toBeInTheDocument()
    expect(container.querySelector(`.${classFragment}`)).toBeTruthy()
  })

  it("unknown kinds fall back to muted styling", () => {
    const { container } = render(<KindBadge kind="weird_thing" />)
    expect(screen.getByText("weird thing")).toBeInTheDocument()
    expect(container.querySelector(".text-muted-foreground")).toBeTruthy()
  })

  it("custom className is appended", () => {
    const { container } = render(<KindBadge kind="custom" className="extra-test-class" />)
    expect(container.querySelector(".extra-test-class")).toBeTruthy()
  })

  it("multi-word underscore kinds get every underscore replaced", () => {
    render(<KindBadge kind="a_b_c_d" />)
    expect(screen.getByText("a b c d")).toBeInTheDocument()
  })
})
