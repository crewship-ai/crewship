import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

// The cells read relations through hooks that hit the API; the structure under
// test is the grouping, not the fetching.
vi.mock("@/components/features/crews/canvas/use-agent-relations", () => ({
  useAgentRelations: () => ({
    issues: [], credentials: [], pipelines: [],
    skills: [
      { id: "s1", enabled: true, skill: { name: "incident-triage", slug: "incident-triage" } },
      { id: "s2", enabled: false, skill: { name: "pr-review", slug: "pr-review" } },
    ],
  }),
  deriveTriggers: () => [],
}))
vi.mock("@/hooks/use-agent-reach", () => ({
  useAgentReach: () => ({ toolkits: [], channels: [], loading: false, refresh: vi.fn() }),
}))
vi.mock("@/components/features/crews/agent-canvas-managers", () => ({
  SkillsManager: () => <div data-testid="skills-manager" />,
}))
vi.mock("@/components/features/integrations/composio/access-editor", () => ({
  AgentConnectorsCard: () => <div data-testid="connectors-manager" />,
}))
vi.mock("@/components/features/crews/agent-channels-card", () => ({
  AgentChannelsCard: () => <div data-testid="channels-manager" />,
}))

import { OverviewTab } from "@/components/features/crews/agent-canvas-tabs/overview-tab"
import type { AgentRecord } from "@/components/features/crews/agent-canvas-tabs/types"

// =============================================================================
// The overview answers three questions, and says which is which.
//
// It used to answer them with 6 cards plus a row of 7 chips, each chip sliding
// a panel in from the right. The panels were two different kinds of thing —
// three held a list, four held an entire tab component — which is why one
// pattern needed two widths. And six of the seven chips duplicated something
// already on the screen: Manage skills was Skills, Workspace was the header's
// Files button, Activity the header's Journal link, Memory was configuration.
//
// So: no chips, no drawer, three labelled bands. These pin that, because the
// cheapest way to regress it is to quietly add a card to the wrong band.
// =============================================================================

const agent = {
  id: "a1", workspace_id: "w1", name: "Casey", slug: "casey",
  agent_role: "AGENT", memory_enabled: true, tool_profile: "CODING",
  crew: { id: "c1", name: "Quality", slug: "quality" },
} as unknown as AgentRecord

function renderTab() {
  return render(
    <OverviewTab
      workspaceId="w1"
      agent={agent}
      crews={[]}
      inbox={{ count: 0 }}
      chats={[]}
      runs={[]}
      peerMessages={[]}
      patch={vi.fn()}
      onAgentChanged={vi.fn()}
    />,
  )
}

describe("agent overview structure", () => {
  it("groups the cards under the question each answers", () => {
    renderTab()
    for (const band of ["What it holds", "What it can do", "What it has been up to"]) {
      expect(screen.getByRole("heading", { name: band })).toBeInTheDocument()
    }
  })

  it("puts each card in the band that matches its question", () => {
    const { container } = renderTab()
    const bandOf = (title: string) => {
      const heading = [...container.querySelectorAll("h2")].find((h) => h.textContent === title)
      return [...(heading?.closest("section")?.querySelectorAll(".type-section") ?? [])]
        .map((e) => e.textContent)
        .filter((t) => t !== title)
    }
    expect(bandOf("What it holds")).toEqual(["Issues", "Routines", "Triggers", "Credentials"])
    expect(bandOf("What it can do")).toEqual(["Skills", "Tools", "Channels"])
    expect(bandOf("What it has been up to")).toEqual(["Runs", "Sessions"])
  })

  it("has no chip row and nothing that slides in from the right", () => {
    renderTab()
    // The chips carried a count beside a label; the cards carry it in a header.
    expect(screen.queryByRole("button", { name: /Manage skills 2/ })).not.toBeInTheDocument()
    expect(screen.queryByText("Workspace")).not.toBeInTheDocument()
    expect(screen.queryByText("Activity")).not.toBeInTheDocument()
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  })

  it("opens one manager at a time, centred, from the card that owns it", async () => {
    renderTab()
    fireEvent.click(screen.getByRole("button", { name: "Manage skills" }))

    const dialog = await screen.findByRole("dialog")
    expect(dialog).toHaveTextContent("Skills")
    expect(screen.getByTestId("skills-manager")).toBeInTheDocument()
    // Only its own manager — this replaced a drawer that showed all four.
    expect(screen.queryByTestId("connectors-manager")).not.toBeInTheDocument()
    expect(screen.queryByTestId("channels-manager")).not.toBeInTheDocument()
  })

  it("counts enabled skills against the total in the card header", async () => {
    renderTab()
    await waitFor(() => expect(screen.getByText("1 / 2")).toBeInTheDocument())
  })
})
