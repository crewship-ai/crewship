import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup, within } from "@testing-library/react"

vi.mock("@/components/features/crews/crew-activity-feed", () => ({ CrewActivityFeed: () => <p>recent activity</p> }))

import { CrewOverview } from "@/components/features/crews/crew-overview"
import { CrewTeam } from "@/components/features/crews/crew-team"
import type { AgentSummary, CrewRecord } from "@/components/features/crews/crew-canvas-tabs/types"

afterEach(cleanup)

const crew = {
  id: "c1", workspace_id: "ws", name: "Ops", slug: "ops", description: null, color: null, icon: null, avatar_style: null,
  issue_prefix: "OPS", network_mode: "restricted", allowed_domains: [], container_memory_mb: 4096, container_cpus: 2,
  container_ttl_hours: 4, runtime_image: "x", devcontainer_config: null, mise_config: null, escalation_config: null,
  cached_image: null, created_at: "", updated_at: "",
} as CrewRecord
const agents: AgentSummary[] = [
  { id: "a1", name: "Ledger", slug: "ledger", status: "IDLE", role_title: null, agent_role: "LEAD" },
  { id: "a2", name: "Scout", slug: "scout", status: "IDLE", role_title: null, agent_role: "AGENT" },
]

function overview(spend: React.ComponentProps<typeof CrewOverview>["spend"]) {
  render(<CrewOverview workspaceId="ws" crew={crew} agents={agents} needs={[]} onBuild={vi.fn()} onInstall={vi.fn()} containers={[]}
    gapCount={0} integrations={[]} onSelectAgent={vi.fn()} onTab={vi.fn()} onNewAgent={vi.fn()} spend={spend} />)
  return screen.getByTestId("crew-health")
}
function team(costByAgent?: ReadonlyMap<string, number> | null) {
  render(<CrewTeam agents={agents} autonomy="guided" hireLimit={10} onSelectAgent={vi.fn()} onNewAgent={vi.fn()} onHire={vi.fn()}
    onApprove={vi.fn()} onRehire={vi.fn()} costByAgent={costByAgent} />)
}

describe("Health › Spend · 30 d", () => {
  it("shows the crew's cost and its model calls", () => {
    const health = overview({ costUsd: 1.1, calls: 12, byAgent: new Map() })
    expect(within(health).getByText("Spend · 30 d")).toBeInTheDocument()
    expect(within(health).getByText("$1.10")).toBeInTheDocument()
    expect(within(health).getByText(/12 model calls/)).toBeInTheDocument()
  })
  it("claims no budget it does not have", () => {
    const health = overview({ costUsd: 1.1, calls: 12, byAgent: new Map() })
    expect(within(health).queryByText(/limit/i)).toBeNull()
  })
  it("is absent, not zero, when the spend is unknown or hidden", () => {
    const health = overview(null)
    expect(within(health).queryByText("Spend · 30 d")).toBeNull()
  })
})

describe("Team › Cost · 30 d", () => {
  it("adds a cost column with each agent's 30-day cost", () => {
    team(new Map([["a1", 0.8]]))
    expect(screen.getByRole("columnheader", { name: "Cost · 30 d" })).toBeInTheDocument()
    const row = document.querySelector('[data-agent-row="ledger"]') as HTMLElement
    expect(within(row).getByText("$0.80")).toBeInTheDocument()
    const none = document.querySelector('[data-agent-row="scout"]') as HTMLElement
    expect(within(none).getByText("$0.00")).toBeInTheDocument()
  })
  it("the column is hidden below md like Skills and Credentials", () => {
    team(new Map())
    expect(screen.getByRole("columnheader", { name: "Cost · 30 d", hidden: true }).className).toContain("max-md:hidden")
  })
  it("has no column when spend is unknown or hidden", () => {
    team(null)
    expect(screen.queryByRole("columnheader", { name: "Cost · 30 d" })).toBeNull()
  })
})
