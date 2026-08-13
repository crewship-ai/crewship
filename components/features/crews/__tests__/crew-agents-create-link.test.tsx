import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

vi.mock("@/components/features/agents/agent-card", () => ({
  AgentCard: ({ agent }: { agent: { slug: string; name: string } }) => (
    <a href={`/crews?agent=${agent.slug}`}>{agent.name}</a>
  ),
}))

import { CrewAgents } from "@/components/features/crews/crew-agents"

// =============================================================================
// "New Agent" / "Add Agent" both pointed at /crews/agents/new?crew_id=<id>.
// That route was deleted with the redesign; agent creation is a dialog on
// /crews reached by ?new=agent (crews-subbar.tsx:47-63).
//
// The crew is NOT carried through. crew_id was an id, and the dialog's
// pre-selection prop takes a crew *slug* (CrewsSubbarProps.defaultCrewSlug via
// crewSlug) — and the ?new= handler does not set it at all, only the toolbar
// button does. Inventing ?crew=<id> here would put an id where the selection
// hook expects a slug, which reads as "no such crew" and clears itself. So the
// link opens the dialog with the crew picker unfilled: one extra choice, and
// the dialog lists the crews. A working generic entry point beats a broken
// specific one.
// =============================================================================

const agents = [
  {
    id: "ag_1",
    name: "Casey",
    slug: "casey",
    description: null,
    role_title: null,
    agent_role: "AGENT",
    status: "IDLE",
    cli_adapter: "CLAUDE_CODE",
    llm_provider: "anthropic",
    llm_model: "claude",
    crew: null,
    _count: { skills: 0, credentials: 0, chats: 0 },
  },
]

const CREW_ID = "crew_9a8b7c6d"

describe("CrewAgents create-agent entry point", () => {
  it("points the header CTA at the create-agent dialog", () => {
    render(<CrewAgents agents={agents} crewId={CREW_ID} canCreate />)
    expect(screen.getByRole("link", { name: /New Agent/i })).toHaveAttribute(
      "href",
      "/crews?new=agent",
    )
  })

  it("points the empty-state CTA at the same dialog", () => {
    render(<CrewAgents agents={[]} crewId={CREW_ID} canCreate />)
    expect(screen.getByRole("link", { name: /Add Agent/i })).toHaveAttribute(
      "href",
      "/crews?new=agent",
    )
  })

  it("never smuggles the crew id into a query the selection hook reads as a slug", () => {
    render(<CrewAgents agents={[]} crewId={CREW_ID} canCreate />)
    expect(
      screen.getByRole("link", { name: /Add Agent/i }).getAttribute("href"),
    ).not.toContain(CREW_ID)
  })

  it("shows no create CTA without permission", () => {
    render(<CrewAgents agents={[]} crewId={CREW_ID} canCreate={false} />)
    expect(screen.queryByRole("link", { name: /Agent/i })).not.toBeInTheDocument()
  })
})
