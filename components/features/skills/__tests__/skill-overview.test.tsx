import { describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen } from "@testing-library/react"
import { SkillPage, type SkillPageProps } from "@/components/features/skills/skill-page"
import type { SkillDetail } from "@/components/features/skills/skills-model"

vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))
vi.mock("next/link", () => ({ default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }))
vi.mock("streamdown", () => ({ Streamdown: ({ children }: { children: string }) => <div data-testid="md">{children}</div> }))
vi.mock("@/hooks/use-journal-list", () => ({ useJournalList: () => ({ entries: [], loading: false, error: null }) }))

const h = vi.hoisted(() => ({ detail: null as unknown, error: null as Error | null }))
vi.mock("@/hooks/use-skills", () => ({
  useSkillDetail: () => ({ data: h.error ? undefined : h.detail, isLoading: false, isError: !!h.error, error: h.error, refetch: vi.fn() }),
  useDeleteSkill: () => ({ mutateAsync: vi.fn() }),
  useSetSkillAssignment: () => ({ mutateAsync: vi.fn() }),
}))

const base: SkillDetail = {
  id: "sk_1", name: "web-search", slug: "web-search", display_name: "Web search",
  description: "Search the web when a task needs current facts.", version: "1.0.0", author: null,
  category: "RESEARCH", source: "CUSTOM", icon: null, vendor: "community", homepage: "https://github.com/acme/skills",
  spdx_license: "MIT", runtime: "INSTRUCTIONS", scan_status: "CLEAN", verification: "VERIFIED",
  content: "## Instructions\n1. Search", license: null, agent_count: 0, installed_on: [],
  usage: { uses_7d: 3, errors_7d: 1, uses_total: 9, last_used_at: null },
}

function renderPage(over: Partial<SkillPageProps> = {}, skill: Partial<SkillDetail> = {}) {
  h.detail = { ...base, ...skill }
  const props: SkillPageProps = {
    workspaceId: "ws_1", skillId: "sk_1", agents: [], crews: [], tab: "overview", onTab: vi.fn(), onBack: vi.fn(), onGive: vi.fn(),
    onDeleted: vi.fn(), onEditor: vi.fn(), canGive: true, canManage: true, ...over,
  }
  render(<SkillPage {...props} />)
  return props
}

describe("skill overview", () => {
  it("header chips from source, trust and license; Update hidden; Give to agents opens the picker", () => {
    const p = renderPage()
    expect(screen.getByRole("heading", { level: 1, name: "Web search" })).toBeInTheDocument()
    const chips = document.querySelector("[data-slot=skill-chips]")!
    expect(chips.textContent).toContain("Verified")
    expect(chips.textContent).toContain("acme/skills")
    expect(chips.textContent).toContain("Research")
    expect(chips.textContent).toContain("MIT")
    expect(chips.textContent).toContain("Instructions")
    expect(screen.queryByRole("button", { name: /^Update/ })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Give to agents" }))
    expect(p.onGive).toHaveBeenCalled()
  })

  it("shows the v67 tabs and the Overview cards, without the SKILL.md breakdown", () => {
    renderPage()
    expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual(["Overview", "SKILL.md", "Used by 0", "Security", "Version"])
    expect(screen.getByText("When an agent uses it")).toBeInTheDocument()
    expect(screen.getByText("Last 7 days")).toBeInTheDocument()
    expect(screen.getByText(".claude/skills/web-search/SKILL.md")).toBeInTheDocument()
    expect(screen.getByText("No credentials. Uses only what the agent already has.")).toBeInTheDocument()
    expect(screen.queryByText("How it works")).not.toBeInTheDocument()
    expect(screen.queryByText(/Today the whole skill is also pasted/)).not.toBeInTheDocument()
  })

  it("Give to agents is hidden from members", () => {
    renderPage({ canGive: false, canManage: false })
    expect(screen.queryByRole("button", { name: "Give to agents" })).not.toBeInTheDocument()
  })

  it("an unknown id shows not found with a way back", () => {
    h.error = new Error("HTTP 404")
    const p = renderPage()
    expect(screen.getByText("This skill doesn’t exist")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Back to skills" }))
    expect(p.onBack).toHaveBeenCalled()
    h.error = null
  })

  it("the Security tab lists Crewship's checks and offers to take a flagged skill from everyone", () => {
    renderPage({ tab: "security" }, {
      scan_status: "FLAGGED", description_quality: "base64 block",
      installed_on: [{ agent_id: "a1", agent_slug: "scout", agent_name: "Scout", avatar_seed: null, avatar_style: null, crew_id: "c1", crew_slug: "r", crew_name: "R", crew_color: null, crew_icon: null, crew_avatar_style: null }],
    })
    expect(screen.getByText(/Flagged · base64 block/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Take it from all agents" })).toBeInTheDocument()
    expect(screen.queryByText(/skills\.sh/)).not.toBeInTheDocument()
  })
})
