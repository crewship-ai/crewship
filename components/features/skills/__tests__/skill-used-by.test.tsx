import { describe, expect, it, vi } from "vitest"
import { act, fireEvent, render, screen, within } from "@testing-library/react"
import { parseActivityUrl } from "@/lib/activity-url"
import { SkillPage, type SkillPageProps } from "@/components/features/skills/skill-page"
import type { SkillAgentRef, SkillDetail, SkillsAgent, SkillsCrew } from "@/components/features/skills/skills-model"

vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))
vi.mock("next/link", () => ({ default: ({ children, href, ...rest }: { children: React.ReactNode; href: string }) => <a href={href} {...rest}>{children}</a> }))
vi.mock("streamdown", () => ({ Streamdown: () => null }))
vi.mock("@/hooks/use-journal-list", () => ({ useJournalList: () => ({ entries: [], loading: false, error: null }) }))

const h = vi.hoisted(() => ({ detail: null as unknown, mutate: (() => Promise.resolve()) as (p: { agentId: string; on: boolean }) => Promise<void> }))
vi.mock("@/hooks/use-skills", () => ({
  useSkillDetail: () => ({ data: h.detail, isLoading: false, isError: false, error: null, refetch: vi.fn() }),
  useDeleteSkill: () => ({ mutateAsync: vi.fn() }),
  useSetSkillAssignment: () => ({ mutateAsync: (p: { agentId: string; on: boolean }) => h.mutate(p) }),
}))

const crews: SkillsCrew[] = [{ id: "c1", slug: "research", name: "Research", color: null, icon: null }]
const agents: SkillsAgent[] = [
  { id: "a1", slug: "scout", name: "Scout", crew_id: "c1" },
  { id: "a2", slug: "writer", name: "Writer", crew_id: "c1" },
  { id: "a3", slug: "analyst", name: "Analyst", crew_id: "c1" },
]
const held = (id: string, name: string): SkillAgentRef => ({
  agent_id: id, agent_slug: name.toLowerCase(), agent_name: name, avatar_seed: null, avatar_style: null, crew_id: "c1", crew_slug: "research",
  crew_name: "Research", crew_color: null, crew_icon: null, crew_avatar_style: null,
})

function renderUsedBy(mutate: typeof h.mutate) {
  h.mutate = mutate
  h.detail = {
    id: "sk", name: "web", slug: "web", display_name: "Web", description: null, version: null, author: null, category: "RESEARCH",
    source: "BUNDLED", icon: null, scan_status: "CLEAN", verification: "VERIFIED", content: "", license: null, agent_count: 1,
    installed_on: [held("a1", "Scout")],
  } satisfies SkillDetail
  const props: SkillPageProps = {
    workspaceId: "ws", skillId: "sk", agents, crews, tab: "used", onTab: vi.fn(), onBack: vi.fn(), onGive: vi.fn(),
    onDeleted: vi.fn(), onEditor: vi.fn(), canGive: true, canManage: true,
  }
  render(<SkillPage {...props} />)
}

describe("Activity action (ODCH-SK-14)", () => {
  function renderOverview(onTab = vi.fn()) {
    h.mutate = () => Promise.resolve()
    h.detail = {
      id: "sk", name: "web", slug: "web", display_name: "Web", description: null, version: null, author: null, category: "RESEARCH",
      source: "BUNDLED", icon: null, scan_status: "CLEAN", verification: "VERIFIED", content: "", license: null, agent_count: 0, installed_on: [],
    } satisfies SkillDetail
    render(
      <SkillPage workspaceId="ws" skillId="sk" agents={agents} crews={crews} tab="overview" onTab={onTab} onBack={vi.fn()} onGive={vi.fn()}
        onDeleted={vi.fn()} onEditor={vi.fn()} canGive canManage />,
    )
    return onTab
  }

  it("opens Activity filtered to the skill, with its own name next to the shell's Activity button", () => {
    const onTab = renderOverview()
    // The top bar already has the shell's "Activity" (live runs) button.
    expect(screen.queryByRole("link", { name: "Activity" })).not.toBeInTheDocument()
    const link = screen.getByRole("link", { name: "Skill activity" })
    const href = link.getAttribute("href") ?? ""
    expect(href.startsWith("/activity?")).toBe(true)
    const state = parseActivityUrl(new URL(href, "http://x").searchParams)
    expect(state.path.stops).toEqual([{ kind: "skill", id: "sk", label: "Web" }])
    // It leaves the page; it does not switch tabs in place.
    fireEvent.click(link)
    expect(onTab).not.toHaveBeenCalled()
  })

  it("is not hidden at phone width", () => {
    renderOverview()
    expect(screen.getByRole("link", { name: "Skill activity" }).className).not.toMatch(/max-sm:hidden/)
  })
})

describe("Used by tab", () => {
  it("grouped by crew; whole-crew toggle is tri-state", () => {
    renderUsedBy(() => Promise.resolve())
    const crew = screen.getByRole("region", { name: "Research" })
    expect(within(crew).getByText("1/3")).toBeInTheDocument()
    expect(within(crew).getByRole("button", { name: "Give to the whole crew" })).toHaveAttribute("aria-pressed", "mixed")
    expect(within(crew).getByRole("switch", { name: "Scout has Web" })).toBeChecked()
    expect(screen.getByText("Recent uses")).toBeInTheDocument()
  })

  it("partial failure lists agents and offers Retry", async () => {
    const calls: string[] = []
    renderUsedBy(async (p) => {
      calls.push(p.agentId)
      if (p.agentId === "a3") throw new Error("nope")
    })
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Give to the whole crew" }))
    })
    expect(calls.sort()).toEqual(["a2", "a3"])
    expect(screen.getByRole("alert")).toHaveTextContent("Not changed for Analyst")
    calls.length = 0
    h.mutate = async (p) => void calls.push(p.agentId)
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Retry" }))
    })
    expect(calls).toEqual(["a3"])
  })
})
