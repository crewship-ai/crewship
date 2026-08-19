import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"

import { SetupNudge } from "@/components/features/onboarding/setup-nudge"

// =============================================================================
// The first-run checklist, and every step on it has to resolve.
//
// Two of the three pointed at routes the /crews redesign deleted:
// "Create a crew" -> /crews/new, "Add an agent" -> /crews/agents/new. Neither
// exists; creating either is a dialog ON /crews now, opened by ?new=crew /
// ?new=agent (components/features/crews/crews-subbar.tsx:47-63). That query
// contract is the only deep link into those dialogs, which is exactly why it
// exists — the command palette needed it for the same reason
// (components/command-palette.tsx:163-164).
//
// This is the nudge that appears when a workspace has nothing in it, so its
// links are among the first a new user clicks. Both were dead.
// =============================================================================

function hrefFor(label: string): string | null {
  return screen.getByRole("link", { name: new RegExp(label, "i") }).getAttribute("href")
}

describe("SetupNudge step targets", () => {
  it("sends 'Add an agent' to the create-agent dialog on /crews", () => {
    render(<SetupNudge crewCount={1} agentCount={0} credentialCount={1} />)
    expect(hrefFor("Add an agent")).toBe("/crews?new=agent")
  })

  it("sends 'Create a crew' to the create-crew dialog on /crews", () => {
    render(<SetupNudge crewCount={0} agentCount={1} credentialCount={1} />)
    expect(hrefFor("Create a crew")).toBe("/crews?new=crew")
  })

  it("leaves the credentials step alone — /credentials is a real route", () => {
    render(<SetupNudge crewCount={1} agentCount={1} credentialCount={0} />)
    expect(hrefFor("Add credentials")).toBe("/credentials")
  })

  it("renders nothing once every step is done", () => {
    const { container } = render(
      <SetupNudge crewCount={1} agentCount={1} credentialCount={1} />,
    )
    expect(container).toBeEmptyDOMElement()
  })
})
