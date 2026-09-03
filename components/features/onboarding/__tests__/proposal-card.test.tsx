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
    render(<ProposalCard proposal={PROPOSAL} onCreate={noop} />)
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
    render(<ProposalCard proposal={PROPOSAL} onCreate={noop} />)
    expect(screen.getByText(PROPOSAL.crewName)).toBeTruthy()
  })

  it("never collapses the agent list into a count instead of rows", () => {
    render(<ProposalCard proposal={PROPOSAL} onCreate={noop} />)
    // A summary like "2 agents" is exactly the shape a lying proposal could
    // get away with — the row count itself is the only thing allowed to say
    // "2", and it has to come from actual rendered rows, not a label.
    expect(screen.queryByText(/\d+\s+agents?/i)).toBeNull()
  })

  it("renders every requested egress domain, not a domain count", () => {
    render(<ProposalCard proposal={PROPOSAL} onCreate={noop} />)
    const chips = screen.getAllByTestId("onboarding-proposal-domain")
    expect(chips.map((c) => c.textContent)).toEqual(PROPOSAL.egressDomains)
  })

  it("says plainly when a proposal asks for no network access", () => {
    render(
      <ProposalCard
        proposal={{ ...PROPOSAL, egressDomains: [] }}
        onCreate={noop}
      />,
    )
    expect(screen.getByText(/No external network access/)).toBeTruthy()
    expect(screen.queryByTestId("onboarding-proposal-domain")).toBeNull()
  })
})

describe("ProposalCard — nothing is written before Create", () => {
  it("does not call onCreate on mount", () => {
    const onCreate = vi.fn()
    render(<ProposalCard proposal={PROPOSAL} onCreate={onCreate} />)
    expect(onCreate).not.toHaveBeenCalled()
  })

  it("does not call onCreate on a re-render with the same props", () => {
    const onCreate = vi.fn()
    const { rerender } = render(
      <ProposalCard proposal={PROPOSAL} onCreate={onCreate} />,
    )
    rerender(<ProposalCard proposal={PROPOSAL} onCreate={onCreate} />)
    rerender(<ProposalCard proposal={PROPOSAL} onCreate={onCreate} />)
    expect(onCreate).not.toHaveBeenCalled()
  })

  it("does not call onCreate when the proposal prop changes (e.g. a revised offer arrives)", () => {
    const onCreate = vi.fn()
    const { rerender } = render(
      <ProposalCard proposal={PROPOSAL} onCreate={onCreate} />,
    )
    const revised: OnboardingProposal = {
      ...PROPOSAL,
      id: "prop_456",
      crewName: "Revised crew",
      agents: [{ name: "New Agent", role: "Lead", model: "claude-sonnet-5" }],
    }
    rerender(<ProposalCard proposal={revised} onCreate={onCreate} />)
    expect(onCreate).not.toHaveBeenCalled()
  })

  it("does not call onCreate while `creating` toggles true and back to false (a failed prior attempt)", () => {
    const onCreate = vi.fn()
    const { rerender } = render(
      <ProposalCard proposal={PROPOSAL} onCreate={onCreate} creating={false} />,
    )
    rerender(
      <ProposalCard proposal={PROPOSAL} onCreate={onCreate} creating={true} />,
    )
    rerender(
      <ProposalCard
        proposal={PROPOSAL}
        onCreate={onCreate}
        creating={false}
        error="Could not create the crew (HTTP 500)"
      />,
    )
    expect(onCreate).not.toHaveBeenCalled()
  })

  it("calls onCreate exactly once when Create is clicked, and with no arguments", () => {
    const onCreate = vi.fn()
    render(<ProposalCard proposal={PROPOSAL} onCreate={onCreate} />)
    fireEvent.click(screen.getByRole("button", { name: /create/i }))
    expect(onCreate).toHaveBeenCalledTimes(1)
    expect(onCreate).toHaveBeenCalledWith()
  })

  // Create is the ONLY control on this card. "Edit" and "Ask for something
  // else" used to sit beside it and neither wrote anything — one prefilled
  // the composer with "Let's change: ", the other sent the fixed sentence
  // "Let's try a different crew." Both were slower than typing the next
  // message, so they were removed. This asserts they stay removed, because
  // re-adding a control here is the kind of change that looks harmless in a
  // diff: every button on this card sits one click from the only write in
  // the onboarding flow.
  // Runtime tools change what the crew's container IS and force an image
  // build the person waits through, so they belong on the card for the same
  // reason egress does — and they must come from the SERVER's resolved list,
  // never the Guide's request, or the card could promise a tool that is
  // silently dropped.
  it("shows the resolved runtime tools when the proposal has any", () => {
    render(<ProposalCard proposal={{ ...PROPOSAL, tools: ["python", "jq"] }} onCreate={noop} />)
    const chips = screen.getAllByTestId("onboarding-proposal-tool").map((el) => el.textContent)
    expect(chips).toEqual(["python", "jq"])
  })

  it("shows no tools row when the default container is enough", () => {
    render(<ProposalCard proposal={{ ...PROPOSAL, tools: [] }} onCreate={noop} />)
    expect(screen.queryAllByTestId("onboarding-proposal-tool")).toHaveLength(0)
    expect(screen.queryByText(/Extra tools in the container/i)).toBeNull()
  })

  it("offers Create and no other action", () => {
    render(<ProposalCard proposal={PROPOSAL} onCreate={noop} />)
    expect(screen.getByRole("button", { name: /create/i })).toBeTruthy()
    expect(screen.queryByRole("button", { name: /edit/i })).toBeNull()
    expect(screen.queryByRole("button", { name: /ask for something else/i })).toBeNull()
    expect(screen.getAllByRole("button")).toHaveLength(1)
  })

  it("disables Create (rather than allowing a second call) once a click is in flight", () => {
    const onCreate = vi.fn()
    render(<ProposalCard proposal={PROPOSAL} onCreate={onCreate} />)
    const button = screen.getByRole("button", { name: /create/i })
    fireEvent.click(button)
    fireEvent.click(button)
    fireEvent.click(button)
    expect(onCreate).toHaveBeenCalledTimes(1)
  })

  it("replaces the buttons with a confirmation once created, and renders no error", () => {
    render(
      <ProposalCard proposal={PROPOSAL} onCreate={noop} created />,
    )
    expect(screen.getByText(/Crew created/)).toBeTruthy()
    expect(screen.queryByRole("button", { name: /create/i })).toBeNull()
  })

  it("surfaces an apply failure next to the card instead of silently retrying", () => {
    render(
      <ProposalCard
        proposal={PROPOSAL}
        onCreate={noop}
        error="Could not create the crew (HTTP 500)"
      />,
    )
    expect(screen.getByRole("alert").textContent).toMatch(/Could not create the crew/)
    // Still actionable — a failure must not lock the human out of retrying
    // or backing out.
    expect(screen.getByRole("button", { name: /create/i })).not.toBeDisabled()
  })
})

describe("ProposalCard — the crew's look and a readable roster", () => {
  it("draws the crew's own icon when the proposal carries one", () => {
    const { container } = render(
      <ProposalCard proposal={{ ...PROPOSAL, crewIcon: "eye", crewColor: "violet" }} onCreate={vi.fn()} />,
    )
    // CrewIcon renders an svg inside a tinted box; the generic Sparkles
    // header is only the fallback for a proposal with no icon.
    expect(container.querySelector("svg.lucide-sparkles")).toBeNull()
    expect(container.querySelector("svg")).not.toBeNull()
  })

  it("shows every agent's full name and full role, never a truncated first letter", () => {
    const longRole = "Reads open pull requests and their diffs, summarises the changes and flags spots that need a human look"
    render(
      <ProposalCard
        proposal={{ ...PROPOSAL, agents: [{ name: "Recenzent PR", role: longRole, model: "claude-sonnet-5" }] }}
        onCreate={vi.fn()}
      />,
    )
    expect(screen.getByText("Recenzent PR")).toBeTruthy()
    expect(screen.getByText(longRole)).toBeTruthy()
  })
})
