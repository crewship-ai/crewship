import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"

vi.mock("next/link", () => ({ default: ({ children, ...p }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => <a {...p}>{children}</a> }))

import { AgentCommandLine, RoutinesMovedNote, agentCommandRows } from "../agent-settings-parts"

afterEach(cleanup)

describe("agent Settings › Command line (v67 rows, each with what it does)", () => {
  it("lists get, update --llm-model and logs --follow with a description", () => {
    expect(agentCommandRows({ slug: "scout", llm_model: "claude-haiku-4-5" })).toEqual([
      { cmd: "crewship agent get scout", note: "Everything about this agent" },
      { cmd: 'crewship agent update scout --llm-model "claude-haiku-4-5"', note: "Change the model" },
      { cmd: "crewship logs scout --follow", note: "Live log of its runs" },
    ])
    // No model set: the placeholder is honest about it instead of inventing one.
    expect(agentCommandRows({ slug: "scout", llm_model: null })[1].cmd).toBe('crewship agent update scout --llm-model "<model>"')
  })

  it("shows each row with a copy button", () => {
    const write = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, "clipboard", { value: { writeText: write }, configurable: true })
    render(<AgentCommandLine agent={{ slug: "scout", llm_model: "m" }} />)
    const card = screen.getByRole("region", { name: "Command line" })
    expect(within(card).getByText("Live log of its runs")).toBeInTheDocument()
    fireEvent.click(within(card).getByRole("button", { name: "Copy crewship logs scout --follow" }))
    expect(write).toHaveBeenCalledWith("crewship logs scout --follow")
  })
})

describe("RoutinesMovedNote", () => {
  it("says what moved and where it went", () => {
    render(<RoutinesMovedNote />)
    expect(screen.getByText("Schedules and webhooks moved to Routines")).toBeInTheDocument()
    expect(screen.getByText(/A routine runs this agent on a schedule, a webhook or an issue, with a test run and history\./)).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Routines" })).toHaveAttribute("href", "/routines")
  })
})
