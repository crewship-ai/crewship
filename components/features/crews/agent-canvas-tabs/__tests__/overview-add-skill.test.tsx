import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"

vi.mock("@/components/features/crews/canvas/use-agent-relations", () => ({
  useAgentRelations: () => ({
    issues: [], credentials: [], pipelines: [],
    skills: [{ id: "as1", skill_id: "s1", enabled: true, skill: { id: "s1", name: "incident-triage", slug: "incident-triage" } }],
    refresh: vi.fn(),
  }),
  deriveTriggers: () => [],
}))
vi.mock("@/hooks/use-agent-reach", () => ({
  useAgentReach: () => ({ toolkits: [], channels: [], loading: false, refresh: vi.fn() }),
}))
vi.mock("@/components/features/crews/agent-canvas-managers", () => ({
  SkillsManager: () => <div data-testid="skills-manager" />,
}))
vi.mock("@/components/features/crews/agent-skills-picker", () => ({
  AgentSkillsPicker: (p: { open: boolean; agentName: string; assigned: Array<{ skill_id: string }>; onClose: () => void }) =>
    p.open ? <div role="dialog" aria-label={`Give skills to ${p.agentName}`} data-held={p.assigned.map((a) => a.skill_id).join(",")} /> : null,
}))
vi.mock("@/components/features/integrations/composio/access-editor", () => ({
  AgentConnectorsCard: () => <div />,
}))
vi.mock("@/components/features/crews/agent-channels-card", () => ({
  AgentChannelsCard: () => <div />,
}))

import { OverviewTab } from "@/components/features/crews/agent-canvas-tabs/overview-tab"
import type { AgentRecord } from "@/components/features/crews/agent-canvas-tabs/types"

afterEach(() => cleanup())

const agent = {
  id: "a1", workspace_id: "w1", name: "Casey", slug: "casey",
  agent_role: "AGENT", memory_enabled: true, tool_profile: "CODING",
  crew: { id: "c1", name: "Quality", slug: "quality" },
} as unknown as AgentRecord

describe("Add skill on the agent Skills card (NEW-CR-K09)", () => {
  it("opens the picker straight from the card, with what the agent holds", () => {
    render(<OverviewTab workspaceId="w1" agent={agent} crews={[]} inbox={{ count: 0 }} chats={[]} runs={[]} peerMessages={[]} patch={vi.fn()} onAgentChanged={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: "Add skill" }))
    expect(screen.getByRole("dialog", { name: "Give skills to Casey" })).toHaveAttribute("data-held", "s1")
    // No detour through the Manage skills dialog.
    expect(screen.queryByTestId("skills-manager")).toBeNull()
  })
})
