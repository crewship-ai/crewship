import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"

import { ProposalCard } from "../proposal-card"
import type { OnboardingProposal } from "../setup-agent-api"

// =============================================================================
// PRD §5.6 ("proposal integrity") and §4.2 ("concrete, not prose") are the
// two properties this file exists to pin:
//
//   1. Every agent's name, role and model is rendered as its own row — never
//      folded into a count or a sentence, because a summary is exactly what a
//      prompt-injected agent could lie in ("3 agents" while creating 30).
//   2. Nothing is written before a human clicks Create. Not on mount, not on
//      a re-render, not on a prop change — only the button's own onClick.
// =============================================================================

const PROPOSAL: OnboardingProposal = {
  id: "prop_123",
  crewName: "Seznam Listing Scraper",
  agents: [
    { name: "Scraper Lead", role: "Lead", model: "claude-sonnet-5" },
    { name: "Data Cleaner", role: "Engineer", model: "claude-haiku-4-5" },
  ],
  egressDomains: ["www.sreality.cz", "www.bezrealitky.cz"],
}

function noop() {}

describe("ProposalCard — concrete rows, never a paragraph", () => {
  it("renders every agent's name, role and model as its own row", () => {
    render(<ProposalCard proposal={PROPOSAL} onCreate={noop} onEdit={noop} onAskDifferent={noop} />)
    const rows = screen.getAllByTestId("onboarding-proposal-agent-row")
    expect(rows).toHaveLength(PROPOSAL.agents.length)
    for (const agent of PROPOSAL.agents) {
      expect(screen.getByText(agent.name)).toBeTruthy()
      expect(screen.getByText(agent.role)).toBeTruthy()
    }
    // Models are resolved through the same label registry every other
    // surface uses — never a summary, never a raw truncated id.
    expect(screen.getByText("Claude Sonnet 5")).toBeTruthy()
    expect(screen.getByText("Claude Haiku 4.5")).toBeTruthy()
  })

  it("renders the crew name", () => {
    render(<ProposalCard proposal={PROPOSAL} onCreate={noop} onEdit={noop} onAskDifferent={noop} />)
    expect(screen.getByText(PROPOSAL.crewName)).toBeTruthy()
  })

  it("never collapses the agent list into a count instead of rows", () => {
    render(<ProposalCard proposal={PROPOSAL} onCreate={noop} onEdit={noop} onAskDifferent={noop} />)
    // A summary like "2 agents" is exactly the shape a lying proposal could
    // get away with — the row count itself is the only thing allowed to say
    // "2", and it has to come from actual rendered rows, not a label.
    expect(screen.queryByText(/\d+\s+agents?/i)).toBeNull()
  })

  it("renders every requested egress domain, not a domain count", () => {
    render(<ProposalCard proposal={PROPOSAL} onCreate={noop} onEdit={noop} onAskDifferent={noop} />)
    const chips = screen.getAllByTestId("onboarding-proposal-domain")
    expect(chips.map((c) => c.textContent)).toEqual(PROPOSAL.egressDomains)
  })

  it("says plainly when a proposal asks for no network access", () => {
    render(
      <ProposalCard
        proposal={{ ...PROPOSAL, egressDomains: [] }}
        onCreate={noop}
        onEdit={noop}
        onAskDifferent={noop}
      />,
    )
    expect(screen.getByText(/No external network access/)).toBeTruthy()
    expect(screen.queryByTestId("onboarding-proposal-domain")).toBeNull()
  })
})

describe("ProposalCard — nothing is written before Create", () => {
  it("does not call onCreate on mount", () => {
    const onCreate = vi.fn()
    render(<ProposalCard proposal={PROPOSAL} onCreate={onCreate} onEdit={noop} onAskDifferent={noop} />)
    expect(onCreate).not.toHaveBeenCalled()
  })

  it("does not call onCreate on a re-render with the same props", () => {
    const onCreate = vi.fn()
    const { rerender } = render(
      <ProposalCard proposal={PROPOSAL} onCreate={onCreate} onEdit={noop} onAskDifferent={noop} />,
    )
    rerender(<ProposalCard proposal={PROPOSAL} onCreate={onCreate} onEdit={noop} onAskDifferent={noop} />)
    rerender(<ProposalCard proposal={PROPOSAL} onCreate={onCreate} onEdit={noop} onAskDifferent={noop} />)
    expect(onCreate).not.toHaveBeenCalled()
  })

  it("does not call onCreate when the proposal prop changes (e.g. a revised offer arrives)", () => {
    const onCreate = vi.fn()
    const { rerender } = render(
      <ProposalCard proposal={PROPOSAL} onCreate={onCreate} onEdit={noop} onAskDifferent={noop} />,
    )
    const revised: OnboardingProposal = {
      ...PROPOSAL,
      id: "prop_456",
      crewName: "Revised crew",
      agents: [{ name: "New Agent", role: "Lead", model: "claude-sonnet-5" }],
    }
    rerender(<ProposalCard proposal={revised} onCreate={onCreate} onEdit={noop} onAskDifferent={noop} />)
    expect(onCreate).not.toHaveBeenCalled()
  })

  it("does not call onCreate while `creating` toggles true and back to false (a failed prior attempt)", () => {
    const onCreate = vi.fn()
    const { rerender } = render(
      <ProposalCard proposal={PROPOSAL} onCreate={onCreate} onEdit={noop} onAskDifferent={noop} creating={false} />,
    )
    rerender(
      <ProposalCard proposal={PROPOSAL} onCreate={onCreate} onEdit={noop} onAskDifferent={noop} creating={true} />,
    )
    rerender(
      <ProposalCard
        proposal={PROPOSAL}
        onCreate={onCreate}
        onEdit={noop}
        onAskDifferent={noop}
        creating={false}
        error="Could not create the crew (HTTP 500)"
      />,
    )
    expect(onCreate).not.toHaveBeenCalled()
  })

  it("calls onCreate exactly once when Create is clicked, and with no arguments", () => {
    const onCreate = vi.fn()
    render(<ProposalCard proposal={PROPOSAL} onCreate={onCreate} onEdit={noop} onAskDifferent={noop} />)
    fireEvent.click(screen.getByRole("button", { name: /create/i }))
    expect(onCreate).toHaveBeenCalledTimes(1)
    expect(onCreate).toHaveBeenCalledWith()
  })

  it("does not call onEdit or onAskDifferent on mount or re-render", () => {
    const onEdit = vi.fn()
    const onAskDifferent = vi.fn()
    const { rerender } = render(
      <ProposalCard proposal={PROPOSAL} onCreate={noop} onEdit={onEdit} onAskDifferent={onAskDifferent} />,
    )
    rerender(<ProposalCard proposal={PROPOSAL} onCreate={noop} onEdit={onEdit} onAskDifferent={onAskDifferent} />)
    expect(onEdit).not.toHaveBeenCalled()
    expect(onAskDifferent).not.toHaveBeenCalled()
  })

  it("disables Create (rather than allowing a second call) once a click is in flight", () => {
    const onCreate = vi.fn()
    render(<ProposalCard proposal={PROPOSAL} onCreate={onCreate} onEdit={noop} onAskDifferent={noop} />)
    const button = screen.getByRole("button", { name: /create/i })
    fireEvent.click(button)
    fireEvent.click(button)
    fireEvent.click(button)
    expect(onCreate).toHaveBeenCalledTimes(1)
  })

  it("replaces the buttons with a confirmation once created, and renders no error", () => {
    render(
      <ProposalCard proposal={PROPOSAL} onCreate={noop} onEdit={noop} onAskDifferent={noop} created />,
    )
    expect(screen.getByText(/Crew created/)).toBeTruthy()
    expect(screen.queryByRole("button", { name: /create/i })).toBeNull()
  })

  it("surfaces an apply failure next to the card instead of silently retrying", () => {
    render(
      <ProposalCard
        proposal={PROPOSAL}
        onCreate={noop}
        onEdit={noop}
        onAskDifferent={noop}
        error="Could not create the crew (HTTP 500)"
      />,
    )
    expect(screen.getByRole("alert").textContent).toMatch(/Could not create the crew/)
    // Still actionable — a failure must not lock the human out of retrying
    // or backing out.
    expect(screen.getByRole("button", { name: /create/i })).not.toBeDisabled()
  })
})
