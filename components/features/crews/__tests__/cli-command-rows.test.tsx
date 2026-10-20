import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor, within } from "@testing-library/react"

vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => true }, role: "OWNER", capabilities: [], hasCapability: () => false, loading: false }),
}))
vi.mock("@/components/features/agents/agent-learning-toggle", () => ({
  AgentLearningToggle: () => <div data-testid="learning-toggle" />,
}))

import { CrewCommandLine, crewCommandRows } from "@/components/features/crews/crew-yaml"
import { AgentCommandLine, agentCommandRows } from "@/components/features/crews/agent-command-line"
import { AgentHooksCard } from "@/components/features/crews/agent-hooks-card"
import { ConfigTab } from "@/components/features/crews/agent-canvas-tabs/config-tab"
import type { AgentRecord } from "@/components/features/crews/agent-canvas-tabs/types"

afterEach(() => { cleanup(); vi.restoreAllMocks() })

function clipboard() {
  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true })
  return writeText
}

describe("Crew Settings › Command line and crewship.yaml (ODCH-CR-14)", () => {
  it("lists get, export, apply and agent list, each with its own line of description", () => {
    expect(crewCommandRows("ops")).toEqual([
      { cmd: "crewship crew get ops", desc: "Show the crew, its agents and container" },
      { cmd: "crewship export crew ops > ops.yaml", desc: "Save the crew as YAML" },
      { cmd: "crewship apply --file ops.yaml", desc: "Create or update it from YAML" },
      { cmd: "crewship agent list --crew ops", desc: "List the agents in this crew" },
    ])
  })

  it("renders every description and a Copy button that copies its command", async () => {
    const writeText = clipboard()
    render(<CrewCommandLine workspaceId="ws1" crew={{ slug: "ops", name: "Operations" }} />)
    const card = screen.getByRole("region", { name: /Command line/ })
    expect(within(card).getByText("Create or update it from YAML")).toBeInTheDocument()
    expect(within(card).getByText("crewship apply --file ops.yaml")).toBeInTheDocument()
    fireEvent.click(within(card).getByRole("button", { name: "Copy crewship apply --file ops.yaml" }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("crewship apply --file ops.yaml"))
  })
})

describe("Agent Settings › Command line (ODCH-CR-14)", () => {
  it("lists only commands the CLI has: get, update --llm-model, logs --follow", () => {
    expect(agentCommandRows({ slug: "scout", llm_model: "claude-sonnet-5-5" })).toEqual([
      { cmd: "crewship agent get scout", desc: "Everything about this agent" },
      { cmd: "crewship agent update scout --llm-model \"claude-sonnet-5-5\"", desc: "Change the model" },
      { cmd: "crewship logs scout --follow", desc: "Live log of its runs" },
    ])
  })

  it("renders descriptions and Copy buttons", async () => {
    const writeText = clipboard()
    render(<AgentCommandLine agent={{ slug: "scout", llm_model: null }} />)
    const card = screen.getByRole("region", { name: "Command line" })
    expect(within(card).getByText("Live log of its runs")).toBeInTheDocument()
    fireEvent.click(within(card).getByRole("button", { name: "Copy crewship logs scout --follow" }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("crewship logs scout --follow"))
  })
})

describe("Agent Settings › Hooks (ODCH-CR-15)", () => {
  it("is a static card with a CLI hint and a Copy button", async () => {
    const writeText = clipboard()
    render(<AgentHooksCard />)
    const card = screen.getByRole("region", { name: "Hooks" })
    expect(within(card).getByText(/set in the CLI/)).toBeInTheDocument()
    expect(within(card).getByText(/crewship hooks create/)).toBeInTheDocument()
    fireEvent.click(within(card).getByRole("button", { name: /^Copy crewship hooks create/ }))
    await waitFor(() => expect(writeText).toHaveBeenCalled())
  })

  it("says plainly that hooks do not fire yet", () => {
    render(<AgentHooksCard />)
    expect(screen.getByText(/paused/i)).toBeInTheDocument()
  })

  it("shows in the agent config while external triggers stay off", () => {
    const agent = { id: "a1", workspace_id: "w1", name: "Scout", slug: "scout", agent_role: "LEAD", llm_model: "m", crew_id: "c1", schedule_enabled: false, schedule_cron: null } as unknown as AgentRecord
    render(<ConfigTab agent={agent} patch={vi.fn()} />)
    expect(screen.getByRole("region", { name: "Hooks" })).toBeInTheDocument()
    expect(screen.queryByText(/Webhook and hooks/)).toBeNull()
  })
})
