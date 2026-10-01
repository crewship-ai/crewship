// The Harbor agent portrait: a 3px conic ring in the crew's colour and the
// engine as a small app-icon badge. The crew colour is stored either as a
// palette id or a hex, and both must reach the ring.

import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { AgentRingAvatar, HarborAgentCard } from "../agent-ring-avatar"

describe("AgentRingAvatar", () => {
  it.each([
    ["#EF4444", "#EF4444"],
    [null, "var(--primary)"],
  ])("rings a crew coloured %s in %s", (color, expected) => {
    const { container } = render(<AgentRingAvatar seed="jamie" crewColor={color} />)
    const ring = container.querySelector("[data-slot=agent-ring]") as HTMLElement
    expect(ring.style.getPropertyValue("--ring")).toBe(expected)
  })

  it("wears the engine as a labelled badge", () => {
    render(<AgentRingAvatar seed="jamie" engine="ANTHROPIC" />)
    expect(screen.getByLabelText("Anthropic engine")).toBeInTheDocument()
  })

  it("draws no badge for an agent with no engine yet", () => {
    render(<AgentRingAvatar seed="jamie" />)
    expect(screen.queryByLabelText(/engine/)).not.toBeInTheDocument()
  })
})

describe("HarborAgentCard", () => {
  it("shows name, role, crew, a status pill and the model, never a raw enum", () => {
    render(
      <HarborAgentCard
        agent={{ id: "a", name: "Jamie", slug: "jamie", status: "IDLE", role_title: "Test Engineer", agent_role: "MEMBER", llm_provider: "ANTHROPIC", llm_model: "claude-haiku-4-5" }}
        crewName="Engineering"
        crewColor="#EF4444"
        href="/crews?agent=jamie"
      />,
    )
    expect(screen.getByText("Jamie")).toBeInTheDocument()
    expect(screen.getByText("Test Engineer")).toBeInTheDocument()
    expect(screen.getByText("Engineering")).toBeInTheDocument()
    expect(document.querySelector("[data-slot=status-pill]")).toHaveTextContent("Idle")
    expect(screen.queryByText("IDLE")).not.toBeInTheDocument()
  })
})
