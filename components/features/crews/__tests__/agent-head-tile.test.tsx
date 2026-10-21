import { describe, it, expect, afterEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

import { AgentHeadTile } from "../agent-ring-avatar"

afterEach(cleanup)

// PAR-123: v67's agent head is a square 52 px tile with the lead star and a
// status dot, not a round 64 px portrait with a conic ring and an engine badge.
describe("AgentHeadTile", () => {
  it("is a 52 px rounded-square tile with no ring and no engine badge", () => {
    render(<AgentHeadTile seed="scout" crewColor="#0F8C8C" status="IDLE" />)
    const tile = document.querySelector("[data-slot=agent-head-tile]") as HTMLElement
    expect(tile.style.width).toBe("52px")
    expect(tile.style.height).toBe("52px")
    expect(tile.style.borderRadius).toBe("28%")
    expect(document.querySelector("[data-slot=agent-ring]")).toBeNull()
    expect(screen.queryByLabelText(/engine/)).toBeNull()
  })
  it("wears the lead star and a working status dot", () => {
    render(<AgentHeadTile seed="scout" crewColor={null} status="RUNNING" lead />)
    expect(screen.getByLabelText("Lead")).toBeInTheDocument()
    expect(screen.getByLabelText("Working")).toBeInTheDocument()
  })
  it("an idle agent has no status dot", () => {
    render(<AgentHeadTile seed="scout" status="IDLE" />)
    expect(screen.queryByLabelText("Working")).toBeNull()
  })
})
