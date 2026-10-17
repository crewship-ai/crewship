import { describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { SkillsExplorer } from "@/components/features/skills/skills-explorer"
import { EMPTY_SKILL_FILTERS, type SkillFilters, type SkillRow, type SkillsAgent, type SkillsCrew } from "@/components/features/skills/skills-model"

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
  { ...base, id: "s2", slug: "s2", category: "DEVOPS", source: "CUSTOM", homepage: "https://github.com/acme/skills", installed_on: [holder("a-riley", "riley", "c-ops")] },
  { ...base, id: "s3", slug: "s3", category: "DESIGN", installed_on: [] },
]

function renderExplorer(over: Partial<SkillFilters> = {}, canReview = true) {
  const onChange = vi.fn()
  render(<SkillsExplorer skills={skills} agents={agents} crews={crews} proposedCount={1} canReview={canReview} filters={{ ...EMPTY_SKILL_FILTERS, ...over }} onChange={onChange} />)
  return onChange
}

describe("SkillsExplorer", () => {
  it("lists the v67 views with counts, Proposed only for reviewers", () => {
    renderExplorer()
    const views = screen.getByRole("navigation", { name: "Skill views" })
    expect(views.textContent).toMatch(/Library3On agents2Not on any agent1Needs attention0Proposed by agents1Archived0/)
    cleanup()
    renderExplorer({}, false)
    expect(screen.queryByText("Proposed by agents")).not.toBeInTheDocument()
  })

  it("orders the panel: active filters, then Views, then the facets", () => {
    renderExplorer({ agentId: "a-riley" })
    const text = document.body.textContent ?? ""
    expect(text.indexOf("Agent: Riley")).toBeLessThan(text.indexOf("Views"))
    expect(text.indexOf("Views")).toBeLessThan(text.indexOf("Source"))
    for (const facet of ["Source", "Domain", "Trust", "Status", "Used by crew"]) expect(screen.getByText(facet)).toBeInTheDocument()
    expect(screen.queryByText("Crews & agents")).not.toBeInTheDocument()
    expect(screen.queryByText("Folders")).not.toBeInTheDocument()
  })

  it("the agent chip removes the agent filter", () => {
    const onChange = renderExplorer({ agentId: "a-riley" })
    fireEvent.click(screen.getByRole("button", { name: "Remove filter Agent: Riley" }))
    expect(onChange).toHaveBeenCalledWith({ agentId: null })
  })

  it("a facet picks one value and picking it again clears it", () => {
    const onChange = renderExplorer({ crewId: "c-ops" })
    // A facet with a pick starts open.
    fireEvent.click(screen.getByTestId("skills-crew-ops"))
    expect(onChange).toHaveBeenLastCalledWith({ crewId: null })
    fireEvent.click(screen.getByTestId("skills-crew-sales"))
    expect(onChange).toHaveBeenLastCalledWith({ crewId: "c-sales" })
  })

  it("Source shows where skills come from", () => {
    renderExplorer({ source: "acme/skills" })
    // The facet row and the active chip.
    expect(screen.getAllByText("acme/skills")).toHaveLength(2)
    expect(screen.getByText("Crewship built-in")).toBeInTheDocument()
  })
})
