import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

import { ConfigTab } from "@/components/features/crews/agent-canvas-tabs/config-tab"
import type { AgentRecord } from "@/components/features/crews/agent-canvas-tabs/types"

// =============================================================================
// Suggested questions — the textarea that is the whole of Step 7's UI.
//
// What matters here is that the counts are LIVE (typed, not saved) and that
// nothing is blocked client-side: the server is the enforcement, and a field
// that silently refuses to submit is worse than a specific error. So the test
// asserts the marks appear AND that the save still fires with the raw text.
// =============================================================================

vi.mock("@/components/features/agents/agent-learning-toggle", () => ({
  AgentLearningToggle: () => <div data-testid="learning-toggle" />,
}))

const baseAgent = {
  id: "a1",
  workspace_id: "w1",
  name: "Morgan",
  slug: "morgan",
  role_title: "SRE / Ops Lead",
  description: "",
  agent_role: "AGENT",
  lead_mode: null,
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
  cli_tools: [],
} as unknown as AgentRecord

function renderTab(overrides: Record<string, unknown> = {}, patch = vi.fn()) {
  render(
    <ConfigTab
      agent={{ ...baseAgent, ...overrides } as AgentRecord}
      crews={[{ id: "c1", name: "Ops", slug: "ops" }]}
      patch={patch}
      onSelectCrew={vi.fn()}
    />,
  )
  return { patch, field: screen.getByLabelText("Suggested questions") as HTMLTextAreaElement }
}

describe("Suggested questions field", () => {
  it("shows the agent's stored prompts and counts them", () => {
    const { field } = renderTab({ suggested_prompts: "What shipped?\nWho is blocked?" })
    expect(field.value).toBe("What shipped?\nWho is blocked?")
    expect(screen.getByText("2 / 8")).toBeInTheDocument()
  })

  it("is empty and reads 0 / 8 for an agent that has none", () => {
    const { field } = renderTab()
    expect(field.value).toBe("")
    expect(screen.getByText("0 / 8")).toBeInTheDocument()
  })

  it("counts live as the user types, ignoring blank lines", () => {
    const { field } = renderTab()
    fireEvent.change(field, { target: { value: "one\n\n  \ntwo\nthree\n" } })
    expect(screen.getByText("3 / 8")).toBeInTheDocument()
  })

  it("marks an over-long question by position", () => {
    const { field } = renderTab()
    fireEvent.change(field, { target: { value: `short\n${"x".repeat(130)}` } })
    expect(screen.getByText(/question 2 is 130 characters/)).toBeInTheDocument()
    expect(screen.getByText(/the limit is 120/)).toBeInTheDocument()
  })

  it("saves the raw text on blur — the server, not the field, is the enforcement", async () => {
    const patch = vi.fn().mockResolvedValue(undefined)
    const { field } = renderTab({}, patch)
    fireEvent.change(field, { target: { value: "  Only one  " } })
    fireEvent.blur(field)
    await waitFor(() => expect(patch).toHaveBeenCalledWith({ suggested_prompts: "  Only one  " }))
  })

  it("does not save when nothing changed", () => {
    const patch = vi.fn()
    const { field } = renderTab({ suggested_prompts: "unchanged" }, patch)
    fireEvent.blur(field)
    expect(patch).not.toHaveBeenCalled()
  })
})
