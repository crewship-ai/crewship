import { describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, within } from "@testing-library/react"
import { SkillsExplorer } from "@/components/features/skills/skills-explorer"
import { EMPTY_SKILL_FILTERS, type SkillRow, type SkillsAgent, type SkillsCrew } from "@/components/features/skills/skills-model"

vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))

const crews: SkillsCrew[] = [
  { id: "c-ops", slug: "ops", name: "Operations", color: "cyan", icon: "server" },
  { id: "c-sales", slug: "sales", name: "Sales", color: "blue", icon: "users" },
]
const agents: SkillsAgent[] = [
  { id: "a-riley", slug: "riley", name: "Riley", crew_id: "c-ops" },
  { id: "a-jamie", slug: "jamie", name: "Jamie", crew_id: "c-sales" },
]
const holder = (id: string, slug: string, crew: string) => ({
  agent_id: id, agent_slug: slug, agent_name: slug, avatar_seed: null, avatar_style: null, crew_id: crew, crew_slug: crew,
  crew_name: crew, crew_color: null, crew_icon: null, crew_avatar_style: null, missing_credentials: [],
})
const base = { name: "", display_name: null, description: null, version: null, author: null, source: "BUNDLED", icon: null, scan_status: "CLEAN", verification: "VERIFIED" }
const skills: SkillRow[] = [
  { ...base, id: "s1", slug: "s1", category: "CODING", installed_on: [holder("a-riley", "riley", "c-ops"), holder("a-jamie", "jamie", "c-sales")] },
  { ...base, id: "s2", slug: "s2", category: "DEVOPS", installed_on: [holder("a-riley", "riley", "c-ops")] },
  { ...base, id: "s3", slug: "s3", category: "DESIGN", installed_on: [] },
]

function renderExplorer(onChange = vi.fn(), filters = EMPTY_SKILL_FILTERS) {
  render(<SkillsExplorer skills={skills} agents={agents} crews={crews} proposedCount={1} filters={filters} onChange={onChange} />)
  return onChange
}

describe("SkillsExplorer", () => {
  it("counts the View buckets and the proposals", () => {
    renderExplorer()
    expect(within(screen.getByTestId("skills-view-all")).getByText("3")).toBeDefined()
    expect(within(screen.getByTestId("skills-view-assigned")).getByText("2")).toBeDefined()
    expect(within(screen.getByTestId("skills-view-unassigned")).getByText("1")).toBeDefined()
    expect(within(screen.getByTestId("skills-view-proposed")).getByText("1")).toBeDefined()
  })

  it("lists each crew's agents with how many skills they hold", () => {
    renderExplorer()
    expect(within(screen.getByTestId("skills-agent-riley")).getByText("2")).toBeDefined()
    expect(within(screen.getByTestId("skills-agent-jamie")).getByText("1")).toBeDefined()
  })

  it("filters by agent, and by crew", () => {
    const onChange = renderExplorer()
    fireEvent.click(screen.getByTestId("skills-agent-jamie"))
    expect(onChange).toHaveBeenLastCalledWith({ agentId: "a-jamie", crewId: "c-sales" })
    fireEvent.click(screen.getByTestId("skills-crew-ops"))
    expect(onChange).toHaveBeenLastCalledWith({ crewId: "c-ops", agentId: null })
  })

  it("shows only the domains in use", () => {
    renderExplorer()
    expect(screen.getByText("Coding")).toBeDefined()
    expect(screen.getByText("DevOps")).toBeDefined()
    expect(screen.getByText("Design")).toBeDefined()
    expect(screen.queryByText("Finance")).toBeNull()
  })
})
