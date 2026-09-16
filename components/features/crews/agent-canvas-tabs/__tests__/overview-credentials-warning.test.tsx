import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

// =============================================================================
// #2169 / #2183 — a fresh agent with no model credential is silently
// indistinguishable from a healthy one.
//
// Two guards got this wrong before the server answered it. The first warned on
// `c.credential_status !== "ACTIVE"`, which never fires on an empty array. The
// second (#2177) warned on `credentials.length === 0`, which never fires on a
// real workspace: the list is the union of everything that reaches the agent,
// and one crew-scoped GH_TOKEN binding makes it non-empty for every agent in
// the crew — including the one #2169 was filed about. "The model slot in the
// env is unfilled" would have been wrong too: for CLAUDE_CODE the sidecar
// injects the key mid-flight and the env holds a dummy by design.
//
// So the cell renders GET /agents/{id}/credential-readiness, computed by the
// runtime's own delivery rules: warn on `missing`, nothing on `unknown`, and
// a healthy sidecar-delivered agent stays quiet. This file's fixtures are the
// issue's own measurements.
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

type ReadinessState = "ready" | "missing" | "unknown"
let readiness: {
  agent_id: string
  adapter: string
  model_credential: { state: ReadinessState; credential_name?: string; source?: string; delivery?: string; provider?: string }
  notes: string[]
} | null = null

vi.mock("@/components/features/crews/canvas/use-agent-relations", () => ({
  useAgentRelations: () => ({ issues: [], credentials, pipelines: [], skills: [], readiness }),
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

// The dev3 measurement from #2183: the only thing reaching a fresh CLAUDE_CODE
// agent is the crew's GH_TOKEN binding. One row, so the list is non-empty.
const ghBindingRow = {
  id: "",
  credential_id: "cred_gh",
  credential_name: "github-globex",
  credential_type: "CLI_TOKEN",
  credential_provider: "GITHUB",
  credential_status: "ACTIVE",
  env_var_name: "GH_TOKEN",
  priority: 0,
  created_at: "2026-08-01T00:00:00Z",
  grant_source: "binding",
}

describe("overview Credentials cell — model credential readiness (#2169, #2183)", () => {
  it("warns when a binding fills the list but the server says no model credential reaches the agent", () => {
    credentials = [ghBindingRow]
    readiness = {
      agent_id: "a1", adapter: "CLAUDE_CODE",
      model_credential: { state: "missing", provider: "ANTHROPIC" },
      notes: ["no credential in this agent's delivery authenticates ANTHROPIC for CLAUDE_CODE"],
    }
    renderTab()
    const badges = screen.getAllByTestId("cell-badge")
    // One badge per DetailCell on the page; the Credentials one is the only
    // one expected to warn in this fixture (no issues/skills/etc. to trip
    // any other cell's own warn logic).
    expect(badges.some((b) => b.getAttribute("data-warn") === "true")).toBe(true)
    expect(screen.getByText(/no model credential/i)).toBeInTheDocument()
    expect(screen.getByText(/authenticates ANTHROPIC for CLAUDE_CODE/i)).toBeInTheDocument()
    // The binding row is still listed: the warning is added, not substituted.
    expect(screen.getByText("github-globex")).toBeInTheDocument()
  })

  it("stays quiet for a healthy CLAUDE_CODE agent whose login the sidecar delivers", () => {
    credentials = [
      ghBindingRow,
      {
        id: "ac_1",
        credential_id: "cred_claude",
        credential_name: "CLAUDE_CODE_OAUTH_TOKEN",
        credential_type: "AI_CLI_TOKEN",
        credential_provider: "ANTHROPIC",
        credential_status: "ACTIVE",
        env_var_name: "CLAUDE_CODE_OAUTH_TOKEN",
        priority: 0,
        created_at: "2026-08-01T00:00:00Z",
        grant_source: "explicit",
      },
    ]
    readiness = {
      agent_id: "a1", adapter: "CLAUDE_CODE",
      model_credential: { state: "ready", credential_name: "CLAUDE_CODE_OAUTH_TOKEN", source: "agent_grant", delivery: "login_env", provider: "ANTHROPIC" },
      notes: [],
    }
    renderTab()
    const badges = screen.getAllByTestId("cell-badge")
    expect(badges.every((b) => b.getAttribute("data-warn") === "false")).toBe(true)
    expect(screen.queryByText(/no model credential/i)).not.toBeInTheDocument()
  })

  it("renders nothing for an unknown verdict, even with an empty list", () => {
    // The readiness family reports nothing it cannot back. An empty list plus
    // no opinion used to render the #2177 item; it now renders no alarm — a
    // false "missing" is the inverse of the bug and teaches operators to
    // ignore the cell.
    credentials = []
    readiness = {
      agent_id: "a1", adapter: "SOME_FUTURE_CLI",
      model_credential: { state: "unknown" },
      notes: ["adapter SOME_FUTURE_CLI is not one the runtime recognises; no opinion"],
    }
    renderTab()
    const badges = screen.getAllByTestId("cell-badge")
    expect(badges.some((b) => b.getAttribute("data-warn") === "true")).toBe(false)
    expect(screen.queryByText(/no model credential/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/no credential assigned/i)).not.toBeInTheDocument()
  })

  it("renders nothing before the server has answered", () => {
    credentials = []
    readiness = null
    renderTab()
    const badges = screen.getAllByTestId("cell-badge")
    expect(badges.some((b) => b.getAttribute("data-warn") === "true")).toBe(false)
    expect(screen.queryByText(/no model credential/i)).not.toBeInTheDocument()
  })

  it("does NOT warn when the agent inherits a crew credential", () => {
    readiness = {
      agent_id: "a1", adapter: "OPENCODE",
      model_credential: { state: "ready", credential_name: "Prod OpenAI key", source: "crew_link", delivery: "env", provider: "OPENAI" },
      notes: [],
    }
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
    readiness = null
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
