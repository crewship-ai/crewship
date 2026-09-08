import { useState } from "react"
import { fireEvent, render, screen, within } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { AgentModelSettings } from "../agent-model-settings"
import { initialAgentDraft, type AgentDraft } from "../types"

vi.mock("../../canvas/config-model", () => ({ ConfigModel: ({ value }: { value: string }) => <div data-testid="model">{value}</div> }))

function Harness() {
  const [draft, setDraft] = useState<AgentDraft>({ ...initialAgentDraft(null), cliAdapter: "CLAUDE_CODE" as const })
  return <><AgentModelSettings draft={draft} setDraft={setDraft} workspaceId="ws1" /><output data-testid="draft">{JSON.stringify(draft)}</output></>
}
const draft = () => JSON.parse(screen.getByTestId("draft").textContent || "{}")

describe("Agent model settings", () => {
  it("switches provider, runner and model together when selecting Codex", () => {
    render(<Harness />)
    fireEvent.click(screen.getByRole("radio", { name: /^Codex CLI/ }))
    expect(draft()).toMatchObject({ cliAdapter: "CODEX_CLI", llmProvider: "OPENAI" })
    expect(draft().llmModel).not.toContain("claude")
    expect(draft().llmModel).not.toBe("")
  })

  it("keeps OpenCode when changing its provider and converts minutes only at the API boundary", () => {
    render(<Harness />)
    fireEvent.click(screen.getByRole("radio", { name: /^OpenCode/ }))
    fireEvent.click(within(screen.getByRole("radiogroup", { name: "Model provider" })).getByRole("radio", { name: "OpenAI", exact: true }))
    fireEvent.change(screen.getByLabelText("Maximum run duration"), { target: { value: "15" } })
    expect(draft()).toMatchObject({ cliAdapter: "OPENCODE", llmProvider: "OPENAI", timeoutSeconds: 900 })
  })

  it("supports arrow-key selection with a single radio tab stop", () => {
    render(<Harness />)
    const group = screen.getByRole("radiogroup", { name: "Model provider" })
    const anthropic = within(group).getByRole("radio", { name: "Anthropic" })
    anthropic.focus()
    fireEvent.keyDown(anthropic, { key: "ArrowRight" })
    expect(within(group).getByRole("radio", { name: "OpenAI" })).toHaveFocus()
    expect(draft()).toMatchObject({ llmProvider: "OPENAI", cliAdapter: "CODEX_CLI" })
    expect(within(group).getAllByRole("radio").filter(radio => radio.tabIndex === 0)).toHaveLength(1)
  })
})
