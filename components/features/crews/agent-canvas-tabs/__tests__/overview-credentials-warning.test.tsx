import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

// =============================================================================
// #2169 — a fresh agent with no credential is silently indistinguishable from
// a healthy one.
//
// The Credentials cell warned on `c.credential_status !== "ACTIVE"`, which
// never fires when the credentials array is empty — nothing looped over means
// nothing to be unhappy about, even though "nothing" is exactly the failure
// case: the agent's first run has nothing to authenticate with.
//
// The mitigation that already exists is crew-credential inheritance
// (internal/api/agent_credentials.go): an agent with no explicit grant of its
// own still shows up here with a `grant_source: "crew"` row, status ACTIVE,
// because the crew already has a credential. That case must stay quiet — the
// agent is fine. Only the true empty array is the alarm.
// =============================================================================

let credentials: Array<{
  id: string
  credential_id: string
  credential_name: string
  credential_type: string
  credential_provider: string
  credential_status: string
  env_var_name: string
  priority: number
  created_at: string
  grant_source?: string
}> = []

vi.mock("@/components/features/crews/canvas/use-agent-relations", () => ({
  useAgentRelations: () => ({ issues: [], credentials, pipelines: [], skills: [] }),
  deriveTriggers: () => [],
}))
vi.mock("@/hooks/use-agent-reach", () => ({
  useAgentReach: () => ({ toolkits: [], channels: [], loading: false, refresh: vi.fn() }),
}))
vi.mock("@/components/features/crews/agent-canvas-managers", () => ({
  SkillsManager: () => <div />,
  CredentialsManager: () => <div />,
}))
vi.mock("@/components/features/integrations/composio/access-editor", () => ({
  AgentConnectorsCard: () => <div />,
}))
vi.mock("@/components/features/crews/agent-channels-card", () => ({
  AgentChannelsCard: () => <div />,
}))

import { OverviewTab } from "@/components/features/crews/agent-canvas-tabs/overview-tab"
import type { AgentRecord } from "@/components/features/crews/agent-canvas-tabs/types"

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

describe("overview Credentials cell — empty-state warning (#2169)", () => {
  it("warns when the agent has no credential at all", () => {
    credentials = []
    renderTab()
    const badges = screen.getAllByTestId("cell-badge")
    // One badge per DetailCell on the page; the Credentials one is the only
    // one expected to warn in this fixture (no issues/skills/etc. to trip
    // any other cell's own warn logic).
    expect(badges.some((b) => b.getAttribute("data-warn") === "true")).toBe(true)
    expect(screen.getByText(/no credential/i)).toBeInTheDocument()
  })

  it("does NOT warn when the agent inherits a crew credential", () => {
    credentials = [
      {
        id: "",
        credential_id: "cred_1",
        credential_name: "Prod OpenAI key",
        credential_type: "API_KEY",
        credential_provider: "OPENAI",
        credential_status: "ACTIVE",
        env_var_name: "Prod OpenAI key",
        priority: 0,
        created_at: "2026-08-01T00:00:00Z",
        grant_source: "crew",
      },
    ]
    renderTab()
    const badges = screen.getAllByTestId("cell-badge")
    expect(badges.every((b) => b.getAttribute("data-warn") === "false")).toBe(true)
    expect(screen.queryByText(/no credential/i)).not.toBeInTheDocument()
  })

  it("still warns when an explicit credential exists but is not ACTIVE", () => {
    credentials = [
      {
        id: "ac_1",
        credential_id: "cred_2",
        credential_name: "Stale key",
        credential_type: "API_KEY",
        credential_provider: "OPENAI",
        credential_status: "REVOKED",
        env_var_name: "STALE_KEY",
        priority: 0,
        created_at: "2026-08-01T00:00:00Z",
        grant_source: "explicit",
      },
    ]
    renderTab()
    const badges = screen.getAllByTestId("cell-badge")
    expect(badges.some((b) => b.getAttribute("data-warn") === "true")).toBe(true)
  })
})
