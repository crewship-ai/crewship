// Tests for AgentAccessTab — agent-centric list of Composio access. Each row
// now leads with the agent's DiceBear avatar (same one shown everywhere else)
// so people can scan for an agent by face, not just name.

import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { AgentAccessTab } from "../agent-access-tab"
import type { AgentLite, AgentBindingsMap } from "../types"

const agents: AgentLite[] = [
  { id: "a1", name: "Riley", slug: "riley", avatar_seed: "riley", avatar_style: "bottts-neutral", crew: { name: "Ops" } },
  { id: "a2", name: "Morgan", slug: "morgan", crew: { name: "Ops", avatar_style: "micah" } },
]

function renderTab(over: Partial<React.ComponentProps<typeof AgentAccessTab>> = {}) {
  return render(
    <AgentAccessTab
      workspaceId="ws1"
      agents={agents}
      bindings={{} as AgentBindingsMap}
      loading={false}
      onChanged={vi.fn()}
      {...over}
    />,
  )
}

describe("AgentAccessTab avatars", () => {
  it("renders one avatar per agent row", () => {
    renderTab()
    expect(screen.getAllByTestId("agent-avatar")).toHaveLength(2)
  })

  it("avatar sits in the same row as the agent name", () => {
    renderTab()
    const row = screen.getByText("Riley").closest("[data-testid='agent-row']") as HTMLElement
    expect(row).toBeTruthy()
    expect(within(row).getByTestId("agent-avatar")).toBeDefined()
  })

  it("each avatar has a non-empty image source", () => {
    renderTab()
    for (const img of screen.getAllByTestId("agent-avatar")) {
      expect(img.getAttribute("src")).toBeTruthy()
    }
  })

  it("falls back to the crew's avatar_style when the agent has none", () => {
    renderTab()
    // Morgan has no avatar_seed/avatar_style — seed falls back to the slug and
    // style to the crew's "micah". Lock that exact contract, not just truthiness.
    const morganRow = screen.getByText("Morgan").closest("[data-testid='agent-row']") as HTMLElement
    const morganAvatar = within(morganRow).getByTestId("agent-avatar")
    expect(morganAvatar.getAttribute("src")).toBe(getAgentAvatarUrl("morgan", "micah"))
  })

  it("filtering by name hides non-matching agents' avatars too", () => {
    renderTab()
    fireEvent.change(screen.getByPlaceholderText("Filter agents…"), {
      target: { value: "riley" },
    })
    expect(screen.getAllByTestId("agent-avatar")).toHaveLength(1)
    expect(screen.queryByText("Morgan")).toBeNull()
  })
})
