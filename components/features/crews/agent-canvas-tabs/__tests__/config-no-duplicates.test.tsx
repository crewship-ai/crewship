import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

import { ConfigTab } from "@/components/features/crews/agent-canvas-tabs/config-tab"
import type { AgentRecord } from "@/components/features/crews/agent-canvas-tabs/types"

// =============================================================================
// One field, one control.
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
  return render(
    <ConfigTab
      agent={agent}
      crews={[{ id: "c1", name: "Ops", slug: "ops" }]}
      patch={vi.fn()}
      onSelectCrew={vi.fn()}
    />,
  )
}

describe("agent configuration", () => {
  it("offers exactly one control per field", () => {
    const { container } = renderTab()

    // timeout: the preset row, and nothing else
    expect(screen.getAllByText("Longest run")).toHaveLength(1)
    expect(container.querySelectorAll('[aria-label="Timeout in seconds"]')).toHaveLength(0)

    // memory: one switch
    expect(screen.getAllByRole("switch", { name: "Memory between sessions" })).toHaveLength(1)
    expect(container.querySelectorAll('[aria-label="Enable memory for agent"]')).toHaveLength(0)

    // tool profile: one radio group
    expect(screen.getAllByRole("radiogroup")).toHaveLength(1)
    expect(container.querySelectorAll('[aria-label="Tool profile"]')).toHaveLength(0)
  })

  it("has no collapsed Advanced panel left to hide a second copy in", () => {
    renderTab()
    expect(screen.queryByText(/^Advanced \(/)).not.toBeInTheDocument()
  })

  it("still shows the tool list the retired panel uniquely carried", () => {
    renderTab()
    expect(screen.getByText(/bash/)).toBeInTheDocument()
  })

  // Waking an agent from outside is gated off (lib/feature-gates.ts): issues
  // and routines are the two finished ways to give an agent work, and a
  // third-party trigger with no delivery log or docs is not a third one.
  it("does not advertise external triggers while the gate is off", () => {
    renderTab()
    expect(screen.queryByText(/Webhook and hooks/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/rotate-webhook-secret/)).not.toBeInTheDocument()
  })

  it("carries the system prompt as a card, not a trailing panel", () => {
    renderTab()
    expect(screen.getByText("System prompt").className).toContain("type-section")
  })

  // Self-improving mode is gated off (lib/feature-gates.ts). The switch and
  // the gate are both proven, but nothing demonstrates that a live agent run
  // reaches the handler they gate, so it is not offered yet.
  // Memory is four markdown files almost nobody edits. It briefly sat at the
  // bottom of this screen, where it was taller than every real setting above
  // it combined. It lives in the ··· menu now — reachable, not resident.
  it("does not park the memory editor under the settings", () => {
    renderTab()
    expect(screen.queryByText("AGENT.md")).not.toBeInTheDocument()
    expect(screen.queryByText(/^Memory$/)).not.toBeInTheDocument()
    // The switch that actually configures it stays.
    expect(screen.getByRole("switch", { name: "Memory between sessions" })).toBeInTheDocument()
  })

  it("does not offer self-improving mode while the gate is off", () => {
    renderTab()
    expect(screen.queryByTestId("learning-card")).not.toBeInTheDocument()
    expect(screen.queryByText(/Learning posture/i)).not.toBeInTheDocument()
  })
})
