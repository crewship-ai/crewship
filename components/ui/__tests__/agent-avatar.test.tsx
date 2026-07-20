import { beforeEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"

// Persisted agent avatars (#1297) — the component's job is to prefer a
// stored render when there is one, degrade to seed generation whenever it
// can't be used, and hand the server a render for agents that lack one.

const h = vi.hoisted(() => ({
  /** Set to make resolveStoredAvatarSrc decline (bearer mode, no stored render). */
  declineStored: false,
}))

vi.mock("@/lib/agent-avatar", () => ({
  getAgentAvatarUrl: (seed: string, style?: string | null) =>
    `data:image/svg+xml;utf8,generated-${seed}-${style ?? "default"}`,
}))

// Argument-aware on purpose. A mock that ignores its argument and returns a
// fixed string would pass even if the component resolved the wrong prop —
// swapping `avatarUrl` for `seed` in the component left the whole suite green
// under such a mock, so it proved nothing about prop wiring.
vi.mock("@/lib/agent-avatar-persist", () => ({
  resolveStoredAvatarSrc: (url: string | null | undefined) =>
    !url || h.declineStored ? null : `resolved:${url}`,
  queueAvatarBackfill: vi.fn().mockResolvedValue(undefined),
}))

vi.mock("@/hooks/use-avatar-styles", () => ({ useAvatarStylesVersion: () => 0 }))

import { queueAvatarBackfill } from "@/lib/agent-avatar-persist"

import { AgentAvatar } from "../agent-avatar"

const mockBackfill = vi.mocked(queueAvatarBackfill)

beforeEach(() => {
  mockBackfill.mockClear()
  h.declineStored = false
})

describe("AgentAvatar", () => {
  it("generates from the seed when the agent has no stored render", () => {
    render(<AgentAvatar seed="alice" style="thumbs" alt="Alice" />)
    expect(screen.getByAltText("Alice")).toHaveAttribute(
      "src",
      "data:image/svg+xml;utf8,generated-alice-thumbs",
    )
  })

  // Asserts the resolved value derives from the avatarUrl prop specifically,
  // so the test fails if the component ever resolves a different prop.
  it("prefers the stored render, resolved from the avatarUrl prop", () => {
    render(
      <AgentAvatar
        agentId="ag-1"
        seed="alice"
        style="thumbs"
        avatarUrl="/api/v1/agents/ag-1/avatar?v=abc"
        alt="Alice"
      />,
    )
    expect(screen.getByAltText("Alice")).toHaveAttribute(
      "src",
      "resolved:/api/v1/agents/ag-1/avatar?v=abc",
    )
  })

  // Bearer mode: the resolver declines, so the component must generate
  // rather than render a URL an <img> cannot authenticate.
  it("generates when the resolver declines the stored URL", () => {
    h.declineStored = true
    render(
      <AgentAvatar
        agentId="ag-1"
        seed="alice"
        style="thumbs"
        avatarUrl="/api/v1/agents/ag-1/avatar?v=abc"
        alt="Alice"
      />,
    )
    expect(screen.getByAltText("Alice")).toHaveAttribute(
      "src",
      "data:image/svg+xml;utf8,generated-alice-thumbs",
    )
  })

  // The stored URL is an authed same-origin request made by the browser's
  // image loader, so it can fail for reasons the component can't predict
  // (session expiry, a cleared row, an offline tab). An agent showing a
  // broken-image icon would be a visible regression over generating, which
  // always works — so failure has to fall back, not surface.
  it("falls back to generating when the stored render fails to load", async () => {
    render(
      <AgentAvatar
        agentId="ag-1"
        seed="alice"
        style="thumbs"
        avatarUrl="/api/v1/agents/ag-1/avatar?v=abc"
        alt="Alice"
      />,
    )
    const img = screen.getByAltText("Alice")
    fireEvent.error(img)
    await waitFor(() =>
      expect(screen.getByAltText("Alice")).toHaveAttribute(
        "src",
        "data:image/svg+xml;utf8,generated-alice-thumbs",
      ),
    )
  })

  it("offers a render to the server for an agent that has none", async () => {
    render(<AgentAvatar agentId="ag-1" seed="alice" style="thumbs" />)
    await waitFor(() => expect(mockBackfill).toHaveBeenCalledWith("ag-1", "alice", "thumbs"))
  })

  it("does not re-offer a render for an agent that already has one", async () => {
    render(
      <AgentAvatar
        agentId="ag-1"
        seed="alice"
        style="thumbs"
        avatarUrl="/api/v1/agents/ag-1/avatar?v=abc"
      />,
    )
    await waitFor(() => expect(mockBackfill).not.toHaveBeenCalled())
  })

  // Most call sites render an avatar for something that isn't a persisted
  // agent row (a crew, a skill author, a comment byline). Those have no id
  // to store against and must stay exactly as they are.
  it("does not offer a render when there is no agent id", async () => {
    render(<AgentAvatar seed="alice" style="thumbs" />)
    await waitFor(() => expect(mockBackfill).not.toHaveBeenCalled())
  })

  it("keeps the existing class merging and img passthrough behaviour", () => {
    render(<AgentAvatar seed="alice" alt="A" className="rounded-lg w-8" data-testid="av" />)
    const img = screen.getByTestId("av")
    expect(img.className).toContain("rounded-lg")
    expect(img.className).not.toContain("rounded-full")
  })
})
