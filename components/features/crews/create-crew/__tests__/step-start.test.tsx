import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

import { StepIdentity } from "../step-identity"
import { StepSuggest } from "../step-start"
import { INITIAL_STATE, type WizardState } from "../types"

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe("New crew — step 1 (v67)", () => {
  it("picks icon and colour inline and offers four ways to start", () => {
    const setState = vi.fn()
    const onStart = vi.fn()
    const onPickIcon = vi.fn()
    render(<StepIdentity state={INITIAL_STATE} setState={setState} onPickIcon={onPickIcon} start="suggest" onStart={onStart} />)
    expect(screen.getByTestId("crew-icon-picker")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("radio", { name: "violet" }))
    expect(setState).toHaveBeenCalledWith({ color: "violet" })
    fireEvent.click(screen.getByRole("radio", { name: "Rocket" }))
    expect(setState).toHaveBeenCalledWith({ icon: "rocket" })
    fireEvent.click(screen.getByRole("button", { name: /more icons/i }))
    expect(onPickIcon).toHaveBeenCalled()
    const ways = screen.getByRole("radiogroup", { name: "Start with" })
    expect(Array.from(ways.querySelectorAll("[role=radio] b")).map((b) => b.textContent)).toEqual([
      "Suggest a team", "From a template", "Import crewship.yaml", "Empty crew",
    ])
    fireEvent.click(screen.getByRole("radio", { name: /Empty crew/ }))
    expect(onStart).toHaveBeenCalledWith("empty")
    expect(screen.getByText("What should this crew do?")).toBeInTheDocument()
  })

  it("an import has no description to ask for; editing keeps the icon tile", () => {
    const { rerender } = render(<StepIdentity state={INITIAL_STATE} setState={vi.fn()} onPickIcon={vi.fn()} start="yaml" onStart={vi.fn()} />)
    expect(screen.queryByText("What should this crew do?")).toBeNull()
    rerender(<StepIdentity state={INITIAL_STATE} setState={vi.fn()} onPickIcon={vi.fn()} />)
    expect(screen.queryByRole("radiogroup", { name: "Start with" })).toBeNull()
    expect(screen.getByRole("button", { name: /pick icon and color/i })).toBeInTheDocument()
  })
})

describe("New crew — Suggested team", () => {
  it("suggested agents are editable before create", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ crew_name: "Research", crew_slug: "research", description: "", agents: [
        { name: "Scout", slug: "scout", role_title: "Lead researcher", agent_role: "LEAD", system_prompt: "" },
        { name: "Writer", slug: "writer", role_title: "Writer", agent_role: "COORDINATOR", system_prompt: "" },
      ] }),
    }) as unknown as Response))
    let state: WizardState = { ...INITIAL_STATE, mode: "suggest", description: "Weekly research" }
    const setState = vi.fn((p: Partial<WizardState>) => { state = { ...state, ...p } })
    const view = render(<StepSuggest workspaceId="ws" state={state} setState={setState} />)
    fireEvent.click(screen.getByRole("button", { name: /Suggest a team/ }))
    await vi.waitFor(() => expect(setState).toHaveBeenCalled())
    view.rerender(<StepSuggest workspaceId="ws" state={state} setState={setState} />)
    await screen.findByTestId("crew-suggested-team")
    // An unknown role is shown as an ordinary agent, never as the raw token.
    expect(screen.queryByText("COORDINATOR")).toBeNull()
    fireEvent.change(screen.getByRole("textbox", { name: "Agent 2 name" }), { target: { value: "Fact checker" } })
    expect(state.suggested[1]).toMatchObject({ name: "Fact checker", slug: "fact-checker" })
    view.rerender(<StepSuggest workspaceId="ws" state={state} setState={setState} />)
    fireEvent.click(screen.getByRole("button", { name: "Remove Scout" }))
    expect(state.suggested.map((a) => a.name)).toEqual(["Fact checker"])
  })
})
