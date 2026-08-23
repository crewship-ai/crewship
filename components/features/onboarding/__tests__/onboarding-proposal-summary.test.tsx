import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import { OnboardingProposalSummary } from "../onboarding-proposal-summary"
import type { OnboardingProposal } from "../setup-agent-api"

const proposal: OnboardingProposal = {
  id: "proposal-1",
  crewName: "Seznam Zprávy Watch",
  crewSlug: "seznam-zpravy-watch",
  templateSlug: "research-analysis",
  agents: [
    { name: "Research Lead", role: "Research Director", model: "claude-sonnet-5" },
    { name: "Data Collector", role: "Data Acquisition Specialist", model: "claude-sonnet-5" },
  ],
  egressDomains: [],
  status: "PENDING",
}

describe("OnboardingProposalSummary", () => {
  it("shows the proposed crew and every agent as a visual roster", () => {
    render(<OnboardingProposalSummary proposal={proposal} created={false} />)

    expect(screen.getByRole("region", { name: /Crew proposal: Seznam Zprávy Watch/i })).toBeTruthy()
    expect(screen.getByText("Ready to review")).toBeTruthy()
    expect(screen.getAllByTestId("onboarding-proposal-summary-agent")).toHaveLength(2)
    expect(screen.getByAltText("Research Lead")).toBeTruthy()
    expect(screen.getByAltText("Data Collector")).toBeTruthy()
    expect(screen.getByTitle("Lead agent")).toBeTruthy()
    expect(screen.getByText("Research Director")).toBeTruthy()
    expect(screen.getByText("Data Acquisition Specialist")).toBeTruthy()
  })

  // The panel renders one card per crew, and `created` is decided PER CARD.
  // It used to be `appliedProposal !== null` in the page — "has any crew been
  // created at all" — so once crew #1 existed, crew #2's card showed the green
  // "Created" badge while nothing had been written for it yet. The panel was
  // wrong at precisely the moment the person was deciding whether to click
  // Create. This pins the two states being independently renderable side by
  // side, which is what the page now does.
  it("renders a created crew and a still-pending one distinctly, side by side", () => {
    const second: OnboardingProposal = {
      ...proposal,
      id: "proposal-2",
      crewName: "Uptime Watch",
      crewSlug: "uptime-watch",
      agents: [{ name: "Uptime Sentry", role: "Availability Monitor", model: "claude-haiku-4-5" }],
    }
    render(
      <>
        <OnboardingProposalSummary proposal={proposal} created />
        <OnboardingProposalSummary proposal={second} created={false} />
      </>,
    )

    expect(screen.getByRole("region", { name: /Crew proposal: Seznam Zprávy Watch/i })).toBeTruthy()
    expect(screen.getByRole("region", { name: /Crew proposal: Uptime Watch/i })).toBeTruthy()
    // Exactly one of each badge — the second crew must NOT read as created.
    expect(screen.getAllByText("Created")).toHaveLength(1)
    expect(screen.getAllByText("Ready to review")).toHaveLength(1)
    // Both rosters survive: 2 agents + 1 agent.
    expect(screen.getAllByTestId("onboarding-proposal-summary-agent")).toHaveLength(3)
  })

  it("switches to the created state without losing the roster", () => {
    render(<OnboardingProposalSummary proposal={proposal} created />)

    expect(screen.getByText("Created")).toBeTruthy()
    expect(screen.getByText("Created crew")).toBeTruthy()
    expect(screen.getAllByTestId("onboarding-proposal-summary-agent")).toHaveLength(2)
  })
})
