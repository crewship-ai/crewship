import { useState } from "react"
import { fireEvent, render, screen } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { RuntimeSection } from "../runtime-section"
import type { AgentRecord } from "../types"

const adapterCatalog = vi.hoisted(() => ({ compatible: null as string[] | null }))
beforeEach(() => { adapterCatalog.compatible = null })

vi.mock("../../model-library-picker", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../model-library-picker")>()
  return { ...actual, getCompatibleAdapters: (provider: string) => adapterCatalog.compatible ?? actual.getCompatibleAdapters(provider), ModelLibraryPicker: ({ onPick, onCustom }: { onPick: (value: Record<string, unknown>) => void; onCustom: () => void }) => <>
    <button onClick={() => onPick({ llm_model: "catalog-model", llm_provider: "ANTHROPIC", cli_adapter: "CLAUDE_CODE" })}>Choose catalog model</button>
    <button onClick={onCustom}>Custom model</button>
  </> }
})
const agent: AgentRecord = {
  id: "a", workspace_id: "w", crew_id: null, name: "Writer", slug: "writer",
  description: null, role_title: null, agent_role: "SPECIALIST", lead_mode: null,
  status: "ACTIVE", cli_adapter: "CLAUDE_CODE", llm_provider: "ANTHROPIC", llm_model: null,
  system_prompt: null, timeout_seconds: 300, tool_profile: "CODING", memory_enabled: true,
  avatar_seed: null, avatar_style: null, updated_at: "2026-10-01T00:00:00Z", crew: null,
}
function Editor({ patch, record = agent }: { patch: (value: Record<string, unknown>) => void; record?: AgentRecord }) {
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState("")
  return <RuntimeSection agent={record} safePatch={patch} customModelOpen={open} setCustomModelOpen={setOpen} customModelDraft={draft} setCustomModelDraft={setDraft} />
}
const input = () => screen.getByPlaceholderText(/e.g. claude/)

describe("RuntimeSection", () => {
  it("keeps the known adapter usable when compatibility references a missing descriptor", () => {
    adapterCatalog.compatible = ["CLAUDE_CODE", "removed-adapter"]
    render(<Editor patch={vi.fn()} />)
    expect(screen.getByRole("button", { name: /^Claude Code/ })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /removed-adapter/ })).not.toBeInTheDocument()
  })
  it("forwards a catalog choice and changes only an inactive compatible adapter", () => {
    const patch = vi.fn()
    render(<Editor patch={patch} />)
    fireEvent.click(screen.getByRole("button", { name: "Choose catalog model" }))
    expect(patch).toHaveBeenLastCalledWith({ llm_model: "catalog-model", llm_provider: "ANTHROPIC", cli_adapter: "CLAUDE_CODE" })
    patch.mockClear()
    fireEvent.click(screen.getByRole("button", { name: /^Claude Code/ }))
    expect(patch).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: /^OpenCode/ }))
    expect(patch).toHaveBeenCalledExactlyOnceWith({ cli_adapter: "OPENCODE" })
  })
  it.each([null, "CURSOR"])("omits the adapter switch for provider %s", (provider) => {
    render(<Editor patch={vi.fn()} record={{ ...agent, llm_provider: provider, llm_model: "existing-model" }} />)
    expect(screen.queryByText("CLI adapter")).not.toBeInTheDocument()
  })
  it.each(["click", "Enter"])("saves a trimmed custom identifier through %s, keeping provider and adapter", (method) => {
    const patch = vi.fn()
    render(<Editor patch={patch} />)
    fireEvent.click(screen.getByRole("button", { name: "Custom model" }))
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled()
    fireEvent.change(input(), { target: { value: "   " } })
    fireEvent.keyDown(input(), { key: "Enter" })
    expect(patch).not.toHaveBeenCalled()
    fireEvent.change(input(), { target: { value: "  my-model  " } })
    fireEvent.keyDown(input(), { key: "ArrowLeft" })
    if (method === "click") fireEvent.click(screen.getByRole("button", { name: "Save" }))
    else fireEvent.keyDown(input(), { key: "Enter" })
    expect(patch).toHaveBeenCalledExactlyOnceWith({ llm_model: "my-model" })
    expect(screen.queryByText("Custom model identifier")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Custom model" }))
    expect(input()).toHaveValue("")
  })
  it.each(["Cancel", "Escape"])("discards draft through %s without patching", (method) => {
    const patch = vi.fn()
    render(<Editor patch={patch} />)
    fireEvent.click(screen.getByRole("button", { name: "Custom model" }))
    fireEvent.change(input(), { target: { value: "discard" } })
    if (method === "Cancel") fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    else fireEvent.keyDown(input(), { key: "Escape" })
    expect(patch).not.toHaveBeenCalled()
    expect(screen.queryByText("Custom model identifier")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Custom model" }))
    expect(input()).toHaveValue("")
  })
})
