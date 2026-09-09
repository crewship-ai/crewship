import { describe, expect, it, vi } from "vitest"
import { render, screen } from "@testing-library/react"
import { RoutineAgentLink } from "../routine-agent-link"

vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: (props: { avatarUrl?: string; seed: string; style?: string; agentId?: string }) => <span data-testid="avatar" data-src={props.avatarUrl} data-seed={props.seed} data-style={props.style} data-agent={props.agentId} />,
}))

describe("routine agent identity", () => {
  it("uses the saved identity and opens the agent by slug, not its database id", () => {
    render(<RoutineAgentLink slug="morgan" workspaceId="ws" agent={{ id: "agent-123", slug: "morgan", name: "Morgan", avatar_url: "/saved-morgan.svg", avatar_seed: "original-face", crew: { avatar_style: "bottts" } }} />)
    expect(screen.getByRole("link", { name: "Morgan" })).toHaveAttribute("href", "/crews?agent=morgan")
    expect(screen.getByTestId("avatar")).toHaveAttribute("data-src", "/saved-morgan.svg")
    expect(screen.getByTestId("avatar")).toHaveAttribute("data-seed", "original-face")
    expect(screen.getByTestId("avatar")).toHaveAttribute("data-style", "bottts")
  })
  it("keeps unresolved agents navigable without inventing a saved avatar", () => {
    render(<RoutineAgentLink slug="review agent" />)
    expect(screen.getByRole("link", { name: "review agent" })).toHaveAttribute("href", "/crews?agent=review%20agent")
    expect(screen.queryByTestId("avatar")).not.toBeInTheDocument()
  })
})
