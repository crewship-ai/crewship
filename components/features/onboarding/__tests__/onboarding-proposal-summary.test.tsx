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

  it("switches to the created state without losing the roster", () => {
    render(<OnboardingProposalSummary proposal={proposal} created />)

    expect(screen.getByText("Created")).toBeTruthy()
    expect(screen.getByText("Your first crew")).toBeTruthy()
    expect(screen.getAllByTestId("onboarding-proposal-summary-agent")).toHaveLength(2)
  })
})
