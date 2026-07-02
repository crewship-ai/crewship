import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { AgentlessBadge } from "@/components/features/routines/routine-agentless-badge"

describe("AgentlessBadge", () => {
  it("renders the token-zero badge when agentless is true", () => {
    render(<AgentlessBadge agentless />)
    expect(screen.getByText("Agentless · token-zero")).toBeInTheDocument()
  })

  it("carries an explanatory tooltip", () => {
    render(<AgentlessBadge agentless />)
    const badge = screen.getByText("Agentless · token-zero").closest('[data-slot="badge"]')
    expect(badge).toHaveAttribute("title")
    expect(badge?.getAttribute("title")).toMatch(/never invoke an LLM/i)
  })

  it("renders nothing when agentless is false", () => {
    const { container } = render(<AgentlessBadge agentless={false} />)
    expect(container).toBeEmptyDOMElement()
  })

  it("renders nothing when agentless is undefined", () => {
    const { container } = render(<AgentlessBadge />)
    expect(container).toBeEmptyDOMElement()
  })

  it("supports a compact size for list rows", () => {
    render(<AgentlessBadge agentless size="sm" />)
    const badge = screen.getByText("Agentless · token-zero").closest('[data-slot="badge"]')
    expect(badge?.classList.contains("text-[10px]")).toBe(true)
  })
})
