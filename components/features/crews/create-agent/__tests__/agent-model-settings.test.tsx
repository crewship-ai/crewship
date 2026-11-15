import { useState } from "react"
import { fireEvent, render, screen, within } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { AgentModelSettings } from "../agent-model-settings"
import { initialAgentDraft, type AgentDraft } from "../types"

vi.mock("../../canvas/config-model", () => ({ ConfigModel: ({ value }: { value: string }) => <div data-testid="model">{value}</div> }))

function Harness({ initial }: { initial?: Partial<AgentDraft> }) {
  const [draft, setDraft] = useState<AgentDraft>({ ...initialAgentDraft(null), cliAdapter: "CLAUDE_CODE" as const, ...initial })
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
    fireEvent.click(within(screen.getByRole("radiogroup", { name: "Agent runner" })).getByRole("radio", { name: /^OpenCode/ }))
    fireEvent.click(within(screen.getByRole("radiogroup", { name: "Model provider" })).getByRole("radio", { name: "OpenAI" }))
    fireEvent.change(screen.getByLabelText("Maximum run duration"), { target: { value: "15" } })
    expect(draft()).toMatchObject({ cliAdapter: "OPENCODE", llmProvider: "OPENAI", timeoutSeconds: 900 })
  })

  it.each([
    ["OpenCode Go", "OPENCODE_GO", "opencode-go/"],
    ["OpenCode Zen", "OPENCODE", "opencode/"],
    ["Z.AI Coding Plan", "ZAI_CODING_PLAN", "zai-coding-plan/"],
  ])("selects the correct runner and billing namespace for %s", (label, provider, prefix) => {
    render(<Harness />)
    fireEvent.click(within(screen.getByRole("radiogroup", { name: "Model provider" })).getByRole("radio", { name: label }))
    expect(draft()).toMatchObject({ cliAdapter: "OPENCODE", llmProvider: provider })
    expect(draft().llmModel.startsWith(prefix)).toBe(true)
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

  it("keeps a custom model id when the runner changes but the provider does not (#5376)", () => {
    render(<Harness initial={{ llmProvider: "OPENAI", cliAdapter: "CODEX_CLI", llmModel: "gpt-7-preview" }} />)
    fireEvent.click(within(screen.getByRole("radiogroup", { name: "Agent runner" })).getByRole("radio", { name: /^OpenCode/ }))
    expect(draft()).toMatchObject({ cliAdapter: "OPENCODE", llmProvider: "OPENAI", llmModel: "gpt-7-preview" })
    fireEvent.click(within(screen.getByRole("radiogroup", { name: "Model provider" })).getByRole("radio", { name: "OpenAI" }))
    expect(draft().llmModel).toBe("gpt-7-preview")
  })

  it("replaces a custom model id when the provider changes", () => {
    render(<Harness initial={{ llmProvider: "OPENAI", cliAdapter: "CODEX_CLI", llmModel: "gpt-7-preview" }} />)
    fireEvent.click(within(screen.getByRole("radiogroup", { name: "Model provider" })).getByRole("radio", { name: "Anthropic" }))
    expect(draft()).toMatchObject({ llmProvider: "ANTHROPIC", cliAdapter: "CLAUDE_CODE" })
    expect(draft().llmModel).toBe("claude-sonnet-5-5")
  })
})
