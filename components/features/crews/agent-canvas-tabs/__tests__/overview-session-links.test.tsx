import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

vi.mock("@/components/features/crews/canvas/use-agent-relations", () => ({
  useAgentRelations: () => ({ issues: [], credentials: [], pipelines: [], skills: [] }),
  deriveTriggers: () => [],
}))
vi.mock("@/hooks/use-agent-reach", () => ({
  useAgentReach: () => ({ toolkits: [], channels: [], loading: false, refresh: vi.fn() }),
}))
vi.mock("@/components/features/crews/agent-canvas-managers", () => ({
  SkillsManager: () => <div />,
}))
vi.mock("@/components/features/integrations/composio/access-editor", () => ({
  AgentConnectorsCard: () => <div />,
}))
vi.mock("@/components/features/crews/agent-channels-card", () => ({
  AgentChannelsCard: () => <div />,
}))

import { OverviewTab } from "@/components/features/crews/agent-canvas-tabs/overview-tab"
import type { AgentRecord, ChatRow } from "@/components/features/crews/agent-canvas-tabs/types"

// =============================================================================
// A session row opens THAT session.
//
// The Sessions cell listed one row per chat and pointed every one of them at
// the bare /chat/<slug>, which drops the id: the page then falls back to "the
// freshest session", so clicking the third row from the bottom silently opened
// a different conversation. Nothing about the click looked wrong — a chat
// opened, it just was not the one that was named on the row.
//
// agent-canvas-cards.tsx's Recent sessions card already carried ?session=<id>,
// which is why this only ever misbehaved on the canvas Overview tab.
// =============================================================================

const agent = {
  id: "a1", workspace_id: "w1", name: "Casey", slug: "casey",
  agent_role: "AGENT", memory_enabled: true, tool_profile: "CODING",
  crew: { id: "c1", name: "Quality", slug: "quality" },
} as unknown as AgentRecord

const chats = [
  { id: "chat_one", title: "Deploy checklist", message_count: 4, started_at: "2026-08-01T10:00:00Z", status: "ACTIVE" },
  { id: "chat two/&", title: "Retro notes", message_count: 1, started_at: "2026-08-02T10:00:00Z", status: "ENDED" },
] as unknown as ChatRow[]

function renderTab() {
  return render(
    <OverviewTab
      workspaceId="w1"
      agent={agent}
      crews={[]}
      inbox={{ count: 0 }}
      chats={chats}
      runs={[]}
      peerMessages={[]}
      patch={vi.fn()}
      onAgentChanged={vi.fn()}
    />,
  )
}

describe("overview Sessions cell", () => {
  it("links each session row to its own session", () => {
    renderTab()
    expect(screen.getByText("Deploy checklist").closest("a")).toHaveAttribute(
      "href",
      "/chat/casey?session=chat_one",
    )
  })

  it("encodes ids that are not URL-safe", () => {
    renderTab()
    expect(screen.getByText("Retro notes").closest("a")).toHaveAttribute(
      "href",
      `/chat/casey?session=${encodeURIComponent("chat two/&")}`,
    )
  })

  it("keeps the cell footer on the bare chat surface", () => {
    renderTab()
    // The footer means "open this agent's chat", not "open a session" — it is
    // the one link here that should NOT carry an id.
    expect(screen.getByRole("link", { name: /Open chat/ })).toHaveAttribute("href", "/chat/casey")
  })
})
