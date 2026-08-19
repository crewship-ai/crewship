import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

// AgentAvatar fetches /api/v1/agents/<id>/avatar on mount. Nothing about the
// link target depends on it, and vitest.setup.ts fails any test that leaks a
// network call, so stub it to a plain box.
vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: () => <div data-testid="avatar" />,
}))

import { AgentCard } from "@/components/features/agents/agent-card"

// =============================================================================
// The whole card is one link, and it used to point at /crews/agents/<id> —
// a route the selection-driven /crews redesign deleted. Clicking an agent
// card fell through to the SPA index and rendered nothing.
//
// The replacement is /crews?agent=<slug>, and the SLUG matters: the canvas
// reads ?agent= through hooks/use-crews-selection.tsx and matches it against
// agent.slug. An id in that parameter is worse than an empty one — the
// stale-selection watcher finds no agent with that slug and clears it, so the
// URL silently rewrites itself and the user lands on an empty canvas with no
// error to report.
//
// The card has the slug in scope (AgentData.slug), so there is no excuse to
// fall back here.
// =============================================================================

type CardAgent = Parameters<typeof AgentCard>[0]["agent"]

function agent(overrides: Partial<CardAgent> = {}): CardAgent {
  return {
    id: "ag_0f1e2d3c4b5a",
    name: "Casey",
    slug: "casey",
    description: null,
    role_title: "Reviewer",
    agent_role: "AGENT",
    status: "IDLE",
    cli_adapter: "CLAUDE_CODE",
    llm_provider: null,
    llm_model: null,
    crew: null,
    _count: { skills: 0, credentials: 0, chats: 0 },
    ...overrides,
  } as CardAgent
}

function cardLink() {
  // The card link is the one wrapping the agent's name heading.
  return screen.getByRole("link", { name: /Casey|Sběrač/ })
}

describe("AgentCard link target", () => {
  it("opens the agent on the /crews canvas, keyed by slug", () => {
    render(<AgentCard agent={agent()} />)
    expect(cardLink()).toHaveAttribute("href", "/crews?agent=casey")
  })

  it("never puts the agent id in ?agent= (the canvas would clear it)", () => {
    render(<AgentCard agent={agent()} />)
    const href = cardLink().getAttribute("href") ?? ""
    expect(href).not.toContain("ag_0f1e2d3c4b5a")
  })

  it("percent-encodes a slug that needs it", () => {
    render(<AgentCard agent={agent({ name: "Sběrač", slug: "sběrač dokladů" })} />)
    expect(cardLink()).toHaveAttribute(
      "href",
      `/crews?agent=${encodeURIComponent("sběrač dokladů")}`,
    )
  })
})
