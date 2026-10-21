import { describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { SkillPage, type SkillPageProps } from "@/components/features/skills/skill-page"
import type { SkillDetail } from "@/components/features/skills/skills-model"

vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))
vi.mock("next/link", () => ({ default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }))
vi.mock("streamdown", () => ({ Streamdown: ({ children }: { children: string }) => <div data-testid="md">{children}</div> }))
vi.mock("@/hooks/use-journal-list", () => ({ useJournalList: () => ({ entries: [], loading: false, error: null }) }))

const h = vi.hoisted(() => ({ detail: null as unknown }))
vi.mock("@/hooks/use-skills", () => ({
  useSkillDetail: () => ({ data: h.detail, isLoading: false, isError: false, error: null, refetch: vi.fn() }),
  useDeleteSkill: () => ({ mutateAsync: vi.fn() }),
  useSetSkillLifecycle: () => ({ mutateAsync: vi.fn() }),
  useSetSkillAssignment: () => ({ mutateAsync: vi.fn() }),
}))

const base: SkillDetail = {
  id: "sk_1", name: "eu-pricing", slug: "eu-pricing", display_name: "EU pricing", description: "Prefer the EU price.",
  version: "3", author: null, category: "RESEARCH", source: "GENERATED", icon: null, scan_status: "CLEAN", verification: "UNVERIFIED",
  content: "## How\n1. Check VAT", license: null, agent_count: 0, installed_on: [],
}

function renderMd(skill: Partial<SkillDetail> = {}) {
  h.detail = { ...base, ...skill }
  const props: SkillPageProps = {
    workspaceId: "ws", skillId: "sk_1", agents: [], crews: [], tab: "md", onTab: vi.fn(), onBack: vi.fn(), onGive: vi.fn(),
    onDeleted: vi.fn(), onEditor: vi.fn(), canGive: true, canManage: true,
  }
  render(<SkillPage {...props} />)
  return props
}

describe("SKILL.md tab", () => {
  it("Preview/Source toggle; no file tree", () => {
    renderMd()
    expect(screen.getByTestId("md")).toHaveTextContent("Check VAT")
    fireEvent.click(screen.getByRole("radio", { name: "Source" }))
    expect(document.querySelector("[data-slot=skill-md-source]")!.textContent).toContain("name: eu-pricing")
    expect(screen.queryByText("scripts/")).not.toBeInTheDocument()
    expect(screen.queryByText("references/")).not.toBeInTheDocument()
  })

  it("Edit only for non-built-in", () => {
    const p = renderMd()
    fireEvent.click(screen.getByRole("button", { name: "Edit" }))
    expect(p.onEditor).toHaveBeenCalledWith(expect.objectContaining({ kind: "edit" }))
    cleanup()
    renderMd({ source: "BUNDLED" })
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument()
    expect(screen.getByText(/copy it into this workspace/)).toBeInTheDocument()
  })

  it("a skill made here shows its version in the header", () => {
    renderMd()
    expect(document.querySelector("[data-slot=skill-chips]")!.textContent).toContain("v3")
  })
})
