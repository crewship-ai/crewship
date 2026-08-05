import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"

import { RoutineProposalAsk } from "../routine-proposal-ask"

// "Nevím, co schvaluju."
//
// The banner said the routine was flagged because it "requires
// credentials" — the risk CATEGORY. Which credentials, which
// integrations, which hosts it will reach is the question the person
// pressing Approve actually has, and the routine's own definition
// answers it. The banner was reading none of it.

describe("<RoutineProposalAsk>", () => {
  it("names the credentials, with scope", () => {
    render(
      <RoutineProposalAsk
        definition={{
          credentials_required: [{ type: "github", scope: "repo" }, { type: "openai" }],
        }}
      />,
    )
    // "github" and "github:repo" are different asks.
    expect(screen.getByText("github:repo")).toBeInTheDocument()
    expect(screen.getByText("openai")).toBeInTheDocument()
  })

  it("names integrations and egress hosts", () => {
    render(
      <RoutineProposalAsk
        definition={{ integrations_required: ["slack"], egress_targets: ["api.example.com"] }}
      />,
    )
    expect(screen.getByText("slack")).toBeInTheDocument()
    expect(screen.getByText("api.example.com")).toBeInTheDocument()
  })

  it("renders nothing when the routine declares nothing", () => {
    // A heading with no chips under it reads as "we could not find
    // out", which is a different claim from "it asks for none".
    const { container } = render(<RoutineProposalAsk definition={{ steps: [] }} />)
    expect(container.textContent).toBe("")
  })

  it("survives a definition that is not shaped the way it should be", () => {
    const { container } = render(
      <RoutineProposalAsk
        definition={{ credentials_required: "github", integrations_required: [42] } as never}
      />,
    )
    expect(container.textContent).toBe("")
  })
})
