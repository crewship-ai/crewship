import { describe, it, expect, afterEach } from "vitest"
import { render, screen, within, cleanup } from "@testing-library/react"

import { AgentReadOrder, READ_ORDER } from "../agent-read-order"

// The order is the backend's, not the mockup's: ethos, language, identity,
// the Instructions field, skills, routines (internal/api/agent_config.go
// loadAgentSystemPrompt), then crew context and memory with PERSONA.md
// (internal/orchestrator/orchestrator_run.go assembleSystemPrompt,
// internal/orchestrator/memory.go buildMemoryContext).
const EXPECTED = [
  "Crewship’s rules",
  "The workspace language",
  "Who it is",
  "Its Instructions",
  "Its skills",
  "The workspace’s routines",
  "Its crew",
  "Its memory",
  "PERSONA.md",
  "What it knows about you",
]

describe("What it reads before every run (A-PERS, K20)", () => {
  afterEach(() => cleanup())

  it("lists what a run reads in the order the backend assembles it", () => {
    render(<AgentReadOrder crewName="Research" memoryEnabled />)
    const card = screen.getByRole("region", { name: "What it reads before every run" })
    const items = within(within(card).getByRole("list")).getAllByRole("listitem")
    expect(items.map((li) => li.querySelector("b")?.textContent)).toEqual(EXPECTED)
    expect(READ_ORDER.map((r) => r.title)).toEqual(EXPECTED)
    expect(items[8]).toHaveTextContent("or the Research default when it has none")
  })

  it("marks the memory steps as skipped while Remember between runs is off", () => {
    render(<AgentReadOrder crewName="Research" memoryEnabled={false} />)
    const card = screen.getByRole("region", { name: "What it reads before every run" })
    const items = within(card).getAllByRole("listitem")
    for (const i of [7, 8, 9]) {
      expect(items[i]).toHaveAttribute("data-skipped", "true")
      expect(items[i]).toHaveTextContent("Skipped: Remember between runs is off")
    }
    for (const i of [0, 1, 2, 3, 4, 5, 6]) expect(items[i]).not.toHaveAttribute("data-skipped")
  })

  it("with memory on nothing is skipped", () => {
    render(<AgentReadOrder crewName={null} memoryEnabled />)
    const card = screen.getByRole("region", { name: "What it reads before every run" })
    expect(within(card).queryByText(/Skipped:/)).not.toBeInTheDocument()
    expect(within(card).getAllByRole("listitem")[8]).toHaveTextContent("or the crew default when it has none")
  })
})
