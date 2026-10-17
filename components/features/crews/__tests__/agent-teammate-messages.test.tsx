import { describe, it, expect, beforeEach } from "vitest"
import { render, screen, cleanup, within } from "@testing-library/react"

import { TeammateMessages, teammateLine } from "../agent-teammate-messages"

describe("Messages from teammates (agent Overview)", () => {
  beforeEach(() => cleanup())

  it("names who asked, quotes the question and says whether it was answered", () => {
    expect(teammateLine({ direction: "incoming", from_agent_name: "Writer", question: "Can I use the EU prices?", response: "Yes", status: "COMPLETED" }))
      .toEqual({ title: "Writer: “Can I use the EU prices?”", state: "answered" })
    expect(teammateLine({ direction: "outgoing", to_agent_name: "Scout", question: "Which source?", status: "RUNNING" }))
      .toEqual({ title: "To Scout: “Which source?”", state: "waiting for an answer" })
    expect(teammateLine({ direction: "incoming", from_agent_slug: "ops", question: "Ping", status: "FAILED" }).state).toBe("failed")
  })

  it("renders one row per message, newest first as the server sends them, at most five", () => {
    const rows = Array.from({ length: 7 }, (_, i) => ({ id: `m${i}`, direction: "incoming", from_agent_name: `Agent ${i}`, question: `Q${i}`, status: "COMPLETED", response: "ok", created_at: new Date().toISOString() }))
    render(<TeammateMessages messages={rows} />)
    const card = screen.getByRole("region", { name: "Messages from teammates" })
    expect(within(card).getByText("Agent 0: “Q0”")).toBeInTheDocument()
    expect(within(card).queryByText("Agent 5: “Q5”")).not.toBeInTheDocument()
    expect(within(card).getByText("7")).toBeInTheDocument()
  })

  it("says so when there are none, and when they could not be loaded", () => {
    const { rerender } = render(<TeammateMessages messages={[]} />)
    expect(screen.getByText("No messages from teammates yet.")).toBeInTheDocument()
    rerender(<TeammateMessages messages={[]} unavailable />)
    expect(screen.getByText("Messages from teammates could not be loaded.")).toBeInTheDocument()
  })
})
