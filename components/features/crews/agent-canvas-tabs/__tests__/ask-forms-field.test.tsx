import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

import { ConfigTab } from "@/components/features/crews/agent-canvas-tabs/config-tab"
import type { AgentRecord } from "@/components/features/crews/agent-canvas-tabs/types"

// =============================================================================
// Ask forms — the JSON editor next to the suggested questions.
//
// The forms are edited with the builder inside the agent Edit dialog, which
// keeps them as a draft until Save. What is worth pinning on the card is the
// one rule an author cannot infer — the line drop — stated where they read it.
// =============================================================================

// The "Pays with" row reads the caller's abilities; the real hook asks the
// workspace store, which asks the network.
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => true }, role: "OWNER", capabilities: [], hasCapability: () => false, loading: false }),
}))

vi.mock("@/components/features/agents/agent-learning-toggle", () => ({
  AgentLearningToggle: () => <div data-testid="learning-toggle" />,
}))

const baseAgent = {
  id: "a1",
  workspace_id: "w1",
  name: "Lucy",
  slug: "lucy",
  role_title: "Bookkeeping",
  description: "",
  agent_role: "AGENT",
  lead_mode: null,
  llm_provider: "ANTHROPIC",
  llm_model: "claude-haiku-4-5",
  cli_adapter: "CLAUDE_CODE",
  tool_profile: "CODING",
  timeout_seconds: 3600,
  memory_enabled: true,
  system_prompt: "You are Lucy.",
  updated_at: new Date("2026-08-13").toISOString(),
  crew_id: "c1",
  crew: { id: "c1", name: "Back office", slug: "back-office" },
  schedule_enabled: false,
  schedule_cron: null,
  schedule_prompt: null,
  schedule_next_run: null,
  cli_tools: [],
} as unknown as AgentRecord

describe("Ask forms card", () => {
  it("states the line-drop rule where the author will read it", () => {
    render(<ConfigTab agent={baseAgent} patch={vi.fn()} />)
    expect(screen.getByText(/takes its whole line away/)).toBeInTheDocument()
  })
})
