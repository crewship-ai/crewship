import { describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen } from "@testing-library/react"
import { SkillsOverview, type SkillsOverviewProps } from "@/components/features/skills/skills-overview"
import { EMPTY_SKILL_FILTERS, type SkillRow } from "@/components/features/skills/skills-model"

vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))
vi.mock("next/link", () => ({ default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }))

const holder = (id: string, missing: string[] = []) => ({
  agent_id: id, agent_slug: id, agent_name: id === "a-riley" ? "Riley" : "Jamie", avatar_seed: null, avatar_style: null,
  crew_id: "c-ops", crew_slug: "ops", crew_name: "Operations", crew_color: null, crew_icon: null, crew_avatar_style: null,
  missing_credentials: missing,
})
const base = { name: "", description: "d", version: "1", author: null, source: "CUSTOM", icon: null, category: "CODING", verification: "VERIFIED", scan_status: "CLEAN" }
const skills: SkillRow[] = [
  { ...base, id: "s-flag", slug: "inspector", display_name: "System Inspector", scan_status: "FLAGGED", description_quality: "Base64 block of 1.4 KB", installed_on: [holder("a-riley")] },
  { ...base, id: "s-gh", slug: "reviewer", display_name: "Code Reviewer", needs_credentials: ["GITHUB_TOKEN"], installed_on: [holder("a-riley", ["GITHUB_TOKEN"])] },
  { ...base, id: "s-idle", slug: "idle", display_name: "Idle Skill" },
]

function props(over: Partial<SkillsOverviewProps> = {}): SkillsOverviewProps {
  return {
    skills, loading: false, error: false, onRetry: vi.fn(), agents: [{ id: "a-riley", slug: "riley", name: "Riley", crew_id: "c-ops" }],
    crews: [{ id: "c-ops", slug: "ops", name: "Operations", color: null, icon: null }], proposed: [], canReview: true, reviewBusy: false,
    onReview: vi.fn(), filters: EMPTY_SKILL_FILTERS, onChange: vi.fn(), onClearAll: vi.fn(), sort: "used", onSort: vi.fn(),
    layout: "grid", onLayout: vi.fn(), onOpen: vi.fn(), onAddSkillsToAgent: vi.fn(), ...over,
  }
}

describe("SkillsOverview", () => {
  it("leads with what needs a person, each with a verb", () => {
    const p = props()
    render(<SkillsOverview {...p} />)
    expect(screen.getByTestId("skills-attention")).toBeDefined()
    expect(screen.getByText("System Inspector is flagged")).toBeDefined()
    expect(screen.getByText("1 skill missing a credential")).toBeDefined()
    fireEvent.click(screen.getByText("Fix"))
    expect(p.onOpen).toHaveBeenCalledWith("s-gh", "agents")
  })

  it("narrows to an agent and offers to add skills to it", () => {
    const p = props({ filters: { ...EMPTY_SKILL_FILTERS, agentId: "a-riley" } })
    render(<SkillsOverview {...p} />)
    expect(screen.queryByTestId("skills-attention")).toBeNull()
    expect(screen.getByText("2 of 3")).toBeDefined()
    fireEvent.click(screen.getByText("Skills for Riley"))
    expect(p.onAddSkillsToAgent).toHaveBeenCalledWith("a-riley")
  })

  it("says what to do when nothing matches", () => {
    render(<SkillsOverview {...props({ filters: { ...EMPTY_SKILL_FILTERS, query: "nothing-like-this" } })} />)
    expect(screen.getByText("No skills match these filters.")).toBeDefined()
    expect(screen.getByText("Clear filters")).toBeDefined()
  })

  it("lists proposals with Approve and Reject", () => {
    const p = props({
      filters: { ...EMPTY_SKILL_FILTERS, view: "proposed" },
      proposed: [{ crew_id: "c-ops", file_name: "skill-x.md", name: "Invoice Matcher", description: "Match payments", description_quality: "OK", category: "FINANCE" }],
    })
    render(<SkillsOverview {...p} />)
    fireEvent.click(screen.getByText("Approve"))
    expect(p.onReview).toHaveBeenCalledWith(expect.objectContaining({ file_name: "skill-x.md" }), true)
  })
})
