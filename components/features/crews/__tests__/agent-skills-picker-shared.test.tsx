import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup, waitFor } from "@testing-library/react"

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }))
vi.mock("sonner", () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn() }) }))

const shared = vi.hoisted(() => ({ props: null as null | Record<string, unknown> }))
vi.mock("@/components/features/skills/give-to-picker", () => ({
  GiveToPicker: (p: Record<string, unknown>) => {
    shared.props = p
    return <div data-testid="shared-give-to" data-direction={String(p.direction)} />
  },
}))

import { AgentSkillsPicker } from "../agent-skills-picker"

// CRW-03 / kit request #3481: "Give skills to {agent}" is the Skills area's
// GiveToPicker turned around, not a mirrored copy.
describe("AgentSkillsPicker renders the shared GiveToPicker", () => {
  afterEach(() => { cleanup(); vi.restoreAllMocks(); shared.props = null })

  it("hands the shared picker the agent, its held skills and the loaded library", async () => {
    vi.spyOn(global, "fetch").mockResolvedValue(new Response(JSON.stringify([{ id: "s1", name: "web-research" }])))
    render(
      <AgentSkillsPicker open agentId="a1" agentName="Scout" workspaceId="ws1" onClose={vi.fn()} onApplied={vi.fn()}
        assigned={[{ id: "as1", skill_id: "s1", enabled: true, skill: { id: "s1", name: "web-research", slug: "web-research" } }]} />,
    )
    expect(await screen.findByTestId("shared-give-to")).toHaveAttribute("data-direction", "agent-to-skills")
    await waitFor(() => expect((shared.props?.library as unknown[] | null)?.length).toBe(1))
    expect(shared.props).toMatchObject({ agent: { id: "a1", name: "Scout" } })
    expect([...(shared.props?.held as Map<string, string>)]).toEqual([["s1", "as1"]])
  })

  it("renders nothing while closed", () => {
    render(<AgentSkillsPicker open={false} agentId="a1" agentName="Scout" workspaceId="ws1" assigned={[]} onClose={vi.fn()} onApplied={vi.fn()} />)
    expect(screen.queryByTestId("shared-give-to")).not.toBeInTheDocument()
  })
})
