import { describe, it, expect, vi } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn().mockResolvedValue({
    ok: true,
    json: async () => ({ agent_id: "a1", enabled: false }),
  }),
}))
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => true }, loading: false }),
}))

import { AgentLearningToggle } from "@/components/features/agents/agent-learning-toggle"

// =============================================================================
// The switch has to describe what it actually gates.
//
// The old copy said turning it on made "recommended skills flip to active".
// There is no such code path. loadSelfLearningEnabled has exactly two callers:
//
//   internal/api/keeper_phase2.go:865  — a keeper ALLOW lesson is written to
//                                        lessons.md now, or queued for approval
//   internal/api/agent_persona.go:401  — a persona auto-apply is demoted to
//                                        inbox approval
//
// So it is an approval gate on the agent's own notes, not a "make it smarter"
// switch, and it can only ever tighten what the crew's autonomy level allows.
// Someone asked what it means and could not tell from the label; that is the
// bug this pins.
// =============================================================================

describe("self-improving mode copy", () => {
  it("names the two files it actually governs", async () => {
    render(<AgentLearningToggle agentId="a1" workspaceId="w1" />)
    await waitFor(() => expect(screen.getByTestId("agent-learning-switch")).toBeInTheDocument())

    const text = document.body.textContent ?? ""
    expect(text).toContain("lessons.md")
    expect(text).toContain("PERSONA.md")
  })

  it("does not claim it flips skills on", async () => {
    render(<AgentLearningToggle agentId="a1" workspaceId="w1" />)
    await waitFor(() => expect(screen.getByTestId("agent-learning-switch")).toBeInTheDocument())

    expect(document.body.textContent ?? "").not.toMatch(/skills? (flip|activate)/i)
  })

  it("says who signs off in each position", async () => {
    render(<AgentLearningToggle agentId="a1" workspaceId="w1" />)
    await waitFor(() => expect(screen.getByTestId("agent-learning-switch")).toBeInTheDocument())

    const text = document.body.textContent ?? ""
    expect(text).toMatch(/inbox/i)
    expect(text).toMatch(/autonomy level/i)
  })
})
