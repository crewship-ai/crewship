import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

import { ConfigTab } from "@/components/features/crews/agent-canvas-tabs/config-tab"
import type { AgentRecord } from "@/components/features/crews/agent-canvas-tabs/types"

// =============================================================================
// Ask forms — the JSON editor next to the suggested questions.
//
// The counts are live and nothing is blocked client-side, for the same reason
// the suggestions field works that way: the server validates on write and its
// refusal names the form and the placeholder, and a field that silently
// refuses to submit is worse than a specific error. What is worth pinning
// here is the PATCH key (a typo there is a save that quietly does nothing)
// and that the one rule an author cannot infer — the line drop — is actually
// stated on the card.
// =============================================================================

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

const receipt = JSON.stringify([
  {
    id: "receipt",
    label: "Add a receipt",
    template: "Supplier: {{supplier}}",
    fields: [
      { name: "supplier", label: "Supplier", type: "text" },
      { name: "amount", label: "Amount", type: "money" },
    ],
  },
])

function renderTab(overrides: Record<string, unknown> = {}, patch = vi.fn()) {
  render(
    <ConfigTab
      agent={{ ...baseAgent, ...overrides } as AgentRecord}
      crews={[{ id: "c1", name: "Back office", slug: "back-office" }]}
      patch={patch}
      onSelectCrew={vi.fn()}
    />,
  )
  return { patch, field: screen.getByLabelText("Forms") as HTMLTextAreaElement }
}

describe("Ask forms field", () => {
  it("shows the agent's stored definition and counts forms and fields", () => {
    const { field } = renderTab({ ask_forms: receipt })
    expect(field.value).toBe(receipt)
    expect(screen.getByText("1 / 4 forms · 2 fields")).toBeInTheDocument()
  })

  it("is empty and reads 0 / 4 for an agent with none", () => {
    const { field } = renderTab()
    expect(field.value).toBe("")
    expect(screen.getByText("0 / 4 forms · 0 fields")).toBeInTheDocument()
  })

  it("reports malformed JSON as the user types instead of throwing", () => {
    const { field } = renderTab()
    fireEvent.change(field, { target: { value: "[{" } })
    expect(screen.getByText("0 / 4 forms · 0 fields")).toBeInTheDocument()
    // The wording is the JSON parser's and varies by runtime, so what is
    // pinned is that an error is surfaced at all — the alternative, an editor
    // that shows "0 forms" for a typo, is indistinguishable from an empty one.
    expect(document.querySelectorAll(".text-destructive").length).toBeGreaterThan(0)
  })

  it("names the form that has too many fields", () => {
    const { field } = renderTab()
    const overFull = JSON.stringify([
      {
        id: "receipt",
        label: "R",
        template: "{{a}}",
        fields: Array.from({ length: 7 }, (_, i) => ({ name: `f${i}`, label: `F${i}`, type: "text" })),
      },
    ])
    fireEvent.change(field, { target: { value: overFull } })
    expect(screen.getByText(/receipt — the limit is 6 fields per form/)).toBeInTheDocument()
  })

  it("saves the raw text on blur under ask_forms", async () => {
    const patch = vi.fn().mockResolvedValue(undefined)
    const { field } = renderTab({}, patch)
    fireEvent.change(field, { target: { value: receipt } })
    fireEvent.blur(field)
    await waitFor(() => expect(patch).toHaveBeenCalledWith({ ask_forms: receipt }))
  })

  it("does not save when nothing changed", () => {
    const patch = vi.fn()
    const { field } = renderTab({ ask_forms: receipt }, patch)
    fireEvent.blur(field)
    expect(patch).not.toHaveBeenCalled()
  })

  it("states the line-drop rule where the author will read it", () => {
    renderTab({ ask_forms: receipt })
    expect(screen.getByText(/takes its whole line away/)).toBeInTheDocument()
  })
})
