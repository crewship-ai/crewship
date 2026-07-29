import { describe, it, expect, vi } from "vitest"
import { render, screen, within } from "@testing-library/react"

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

  it("still shows what the retired panel uniquely carried", () => {
    renderTab()
    // the read-only tool list, and the CLI-only surfaces
    expect(screen.getByText(/bash/)).toBeInTheDocument()
    expect(screen.getByText(/crewship hooks/)).toBeInTheDocument()
  })

  it("carries the system prompt and learning posture as cards, not a trailing panel", () => {
    renderTab()
    const prompt = screen.getByText("System prompt")
    expect(prompt.className).toContain("type-section")
    expect(within(screen.getByTestId("learning-card")).getByTestId("learning-toggle")).toBeInTheDocument()
  })
})
