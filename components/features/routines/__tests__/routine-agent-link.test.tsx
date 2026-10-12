import { describe, expect, it, vi } from "vitest"
import { render, screen } from "@testing-library/react"
import { RoutineAgentLink } from "../routine-agent-link"

vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: (props: { avatarUrl?: string; seed: string; style?: string }) => <span data-testid="avatar" data-src={props.avatarUrl} data-seed={props.seed} data-style={props.style} />,
}))

describe("routine agent identity", () => {
  it("uses the saved identity and opens the agent by slug, not its database id", () => {
    render(<RoutineAgentLink slug="morgan" agent={{ id: "agent-123", slug: "morgan", name: "Morgan", avatar_url: "/saved-morgan.svg", avatar_seed: "original-face", crew: { avatar_style: "bottts" } }} />)
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

  it("prefers the agent's style over its crew and derives an absent seed from its name", () => {
    render(<RoutineAgentLink slug="review/a?b" agent={{ id: "a", slug: "review/a?b", name: "Reviewer", avatar_style: "lorelei", crew: { avatar_style: "bottts" } }} />)
    expect(screen.getByRole("link", { name: "Reviewer" })).toHaveAttribute("href", "/crews?agent=review%2Fa%3Fb")
    expect(screen.getByTestId("avatar")).toHaveAttribute("data-seed", "Reviewer")
    expect(screen.getByTestId("avatar")).toHaveAttribute("data-style", "lorelei")
  })

  it("keeps an unnamed saved agent navigable when no crew or avatar style is available", () => {
    render(<RoutineAgentLink slug="reviewer" agent={{ id: "a", slug: "reviewer", name: "" }} />)
    expect(screen.getByRole("link", { name: "reviewer" })).toHaveAttribute("title", "Open reviewer")
    expect(screen.getByTestId("avatar")).not.toHaveAttribute("data-style")
  })
})
