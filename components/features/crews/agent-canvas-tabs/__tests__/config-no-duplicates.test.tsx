import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

import { ConfigTab } from "@/components/features/crews/agent-canvas-tabs/config-tab"
import type { AgentRecord } from "@/components/features/crews/agent-canvas-tabs/types"

// =============================================================================
// One field, one control — and one editor.
//
// Identity, model, tools and the system prompt are edited in the agent Edit
// dialog's own sections. This tab used to carry a second, save-per-field
// editor for them; it is gone, so the tab must not offer those controls.
//
// The old "Advanced (LLM tuning, tools, memory, webhook, hooks)" panel sat
// below these cards and offered a SECOND control for timeout_seconds,
// tool_profile and memory_enabled. Two widgets writing one field is how the
// screen ends up disagreeing with itself: change it in one, the other keeps
// showing the stale value until a refetch, and which one wins depends on
// whichever the user touched last.
//
// This is the duplication Pavel kept pointing at, so it gets a test rather
// than a code comment.
// =============================================================================

// The "Pays with" row reads the caller's abilities; the real hook asks the
// workspace store, which asks the network.
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => true }, role: "OWNER", capabilities: [], hasCapability: () => false, loading: false }),
}))

vi.mock("@/components/features/agents/agent-learning-toggle", () => ({
  AgentLearningToggle: () => <div data-testid="learning-toggle" />,
}))

const agent = {
  id: "a1",
  workspace_id: "w1",
  name: "Morgan",
  slug: "morgan",
  role_title: "SRE / Ops Lead",
  description: "",
  agent_role: "LEAD",
  lead_mode: "active",
  llm_provider: "ANTHROPIC",
  llm_model: "claude-haiku-4-5",
  cli_adapter: "CLAUDE_CODE",
  tool_profile: "FULL",
  timeout_seconds: 3600,
  memory_enabled: true,
  system_prompt: "You are Morgan.",
  updated_at: new Date("2026-07-27").toISOString(),
  crew_id: "c1",
  crew: { id: "c1", name: "Ops", slug: "ops" },
  schedule_enabled: false,
  schedule_cron: null,
  schedule_prompt: null,
  schedule_next_run: null,
  cli_tools: ["bash", "read", "write"],
} as unknown as AgentRecord

function renderTab() {
  return render(<ConfigTab agent={agent} patch={vi.fn()} />)
}

describe("agent configuration", () => {
  it("offers no second editor for what the dialog's sections own", () => {
    const { container } = renderTab()
    expect(screen.queryByText("Longest run")).not.toBeInTheDocument()
    expect(screen.queryByRole("switch", { name: "Memory between sessions" })).not.toBeInTheDocument()
    expect(screen.queryAllByRole("radiogroup")).toHaveLength(0)
    expect(screen.queryByText("System prompt")).not.toBeInTheDocument()
    expect(container.querySelectorAll('[aria-label="Pays with"]')).toHaveLength(0)
  })

  it("has no collapsed Advanced panel left to hide a second copy in", () => {
    renderTab()
    expect(screen.queryByText(/^Advanced \(/)).not.toBeInTheDocument()
  })

  // Waking an agent from outside is gated off (lib/feature-gates.ts): issues
  // and routines are the two finished ways to give an agent work, and a
  // third-party trigger with no delivery log or docs is not a third one.
  it("does not advertise external triggers while the gate is off", () => {
    renderTab()
    expect(screen.queryByText(/Webhook and hooks/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/rotate-webhook-secret/)).not.toBeInTheDocument()
  })

  // Memory is four markdown files almost nobody edits. It lives in the ···
  // menu — reachable, not resident.
  it("does not park the memory editor under the settings", () => {
    renderTab()
    expect(screen.queryByText("AGENT.md")).not.toBeInTheDocument()
    expect(screen.queryByText(/^Memory$/)).not.toBeInTheDocument()
  })

  // Self-improving mode is gated off (lib/feature-gates.ts).
  it("does not offer self-improving mode while the gate is off", () => {
    renderTab()
    expect(screen.queryByTestId("learning-card")).not.toBeInTheDocument()
    expect(screen.queryByText(/Learning posture/i)).not.toBeInTheDocument()
  })
})
