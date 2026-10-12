import { describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen } from "@testing-library/react"
import { SkillPage, type SkillPageProps } from "@/components/features/skills/skill-page"
import type { SkillDetail } from "@/components/features/skills/skills-model"

vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))
vi.mock("next/link", () => ({ default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }))
vi.mock("streamdown", () => ({ Streamdown: ({ children }: { children: string }) => <div data-testid="md">{children}</div> }))
vi.mock("@/hooks/use-journal-list", () => ({ useJournalList: () => ({ entries: [], loading: false, error: null }) }))

let detail: SkillDetail
vi.mock("@/hooks/use-skills", () => ({
  useSkillDetail: () => ({ data: detail, isLoading: false, isError: false, refetch: vi.fn() }),
  useDeleteSkill: () => ({ mutateAsync: vi.fn() }),
  useSetSkillAssignment: () => ({ mutateAsync: vi.fn() }),
}))

const base: SkillDetail = {
  id: "sk_1", name: "File Crafter", slug: "file-crafter", display_name: "File Crafter",
  description: "File and directory creation, CSV generation, data formatting", version: "1.0.0", author: null,
  category: "CODING", source: "CUSTOM", icon: "file-text", vendor: "community", scan_status: "CLEAN", verification: "UNVERIFIED",
  content: "# File Crafter\n\n## When to Activate\n- Keywords: \"create\", \"CSV\"\n\n## Instructions\n1. Use `mkdir -p` first\n2. Validate JSON\n\n## Guardrails\n- Write only to /tmp/",
  license: null, agent_count: 0, installed_on: [], usage: { uses_7d: 3, errors_7d: 1, uses_total: 9, last_used_at: null },
}

function renderPage(over: Partial<SkillPageProps> = {}, skill: Partial<SkillDetail> = {}) {
  detail = { ...base, ...skill }
  const props: SkillPageProps = {
    workspaceId: "ws_1", skillId: "sk_1", agents: [], crews: [], tab: "overview", onTab: vi.fn(), onAssign: vi.fn(),
    onDeleted: vi.fn(), onEditor: vi.fn(), ...over,
  }
  render(<SkillPage {...props} />)
  return props
}

describe("SkillPage", () => {
  it("lays the SKILL.md out on the Overview", () => {
    renderPage()
    expect(screen.getByText("When agents use it")).toBeInTheDocument()
    expect(screen.getByText("create")).toBeInTheDocument()
    expect(screen.getByText("How it works")).toBeInTheDocument()
    expect(screen.getByText("mkdir -p").tagName).toBe("CODE")
    expect(screen.getByText("Guardrails")).toBeInTheDocument()
    expect(screen.getByText("Write only to /tmp/")).toBeInTheDocument()
    expect(screen.queryByText("What it hands back")).not.toBeInTheDocument()
    expect(screen.getByText("Nothing. It runs as instructions only.")).toBeInTheDocument()
  })

  it("lists the sections when the SKILL.md has none it can lay out", () => {
    renderPage({}, { content: "# Brand\n\n## Colours\nNavy.\n\n## Typography\nInter." })
    expect(screen.getByText("What's inside")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Colours" })).toBeInTheDocument()
    expect(screen.queryByText("How it works")).not.toBeInTheDocument()
  })

  it("opens the editor from Edit and from the icon", () => {
    const p = renderPage()
    fireEvent.click(screen.getByRole("button", { name: "Edit" }))
    expect(p.onEditor).toHaveBeenLastCalledWith({ kind: "edit", skill: detail })
    fireEvent.click(screen.getByRole("button", { name: "Change icon or domain" }))
    expect(p.onEditor).toHaveBeenLastCalledWith({ kind: "edit", skill: detail, panel: "icon" })
  })

  it("offers no Edit for a built-in skill", () => {
    renderPage({}, { source: "BUNDLED" })
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Change icon or domain" })).not.toBeInTheDocument()
  })

  it("shows the frontmatter as a table and the source on request", () => {
    renderPage({ tab: "content" })
    const fm = screen.getByLabelText("Frontmatter")
    expect(fm).toHaveTextContent("namefile-crafter")
    expect(fm).toHaveTextContent("categoryCODING")
    fireEvent.click(screen.getByRole("button", { name: "Source" }))
    expect(document.querySelector("pre")?.textContent).toMatch(/^---\nname: file-crafter\n/)
    expect(screen.queryByLabelText("Frontmatter")).not.toBeInTheDocument()
  })
})
