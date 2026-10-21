import { describe, expect, it, vi } from "vitest"
import { cleanup, act, fireEvent, render, screen } from "@testing-library/react"
import { AddSkillDialog, foundFromDryRun, normalizeRepoInput, pastedSkillName } from "@/components/features/skills/add-skill-dialog"
import type { SkillsAgent, SkillsCrew } from "@/components/features/skills/skills-model"

type Req = { gitUrl: string; dryRun?: boolean; paths?: string[]; allowUnsafeLicense?: boolean }
const h = vi.hoisted(() => ({ bulk: [] as Req[], assign: [] as string[] }))
vi.mock("@/hooks/use-skills", () => ({
  useBulkImportSkills: () => ({
    mutateAsync: async (r: Req) => {
      h.bulk.push(r)
      if (r.dryRun)
        return {
          source: r.gitUrl, total_found: 3, total_imported: 0, imported: [],
          skipped: [
            { path: "skills/frontend-design/SKILL.md", slug: "frontend-design", reason: "dry-run" },
            { path: "skills/webapp-testing/SKILL.md", slug: "webapp-testing", reason: "dry-run" },
            { path: "skills/docx/SKILL.md", slug: "docx", reason: 'license "Proprietary" is not on the allowlist' },
          ],
        }
      return { source: r.gitUrl, total_found: 2, total_imported: 2, imported: r.paths!.map((p, i) => ({ skill_id: `id${i}`, slug: p, created: true })), skipped: [] }
    },
  }),
  useImportSkill: () => ({ mutateAsync: vi.fn(async () => ({ skill_id: "p1", slug: "x", name: "x", created: true })) }),
  useSetSkillAssignment: () => ({ mutateAsync: async (p: { agentId: string; skillId: string }) => void h.assign.push(`${p.agentId}:${p.skillId}`) }),
}))

const crews: SkillsCrew[] = [{ id: "c1", slug: "code-review", name: "Code review", color: null, icon: null }]
const agents: SkillsAgent[] = [
  { id: "a1", slug: "rev", name: "Reviewer", crew_id: "c1" },
  { id: "a2", slug: "lint", name: "Linter", crew_id: "c1" },
]

function renderDialog(over: Partial<React.ComponentProps<typeof AddSkillDialog>> = {}) {
  const props = {
    open: true, onOpenChange: vi.fn(), workspaceId: "ws", crews, agents, takenSlugs: new Set(["webapp-testing"]),
    recentSources: [{ label: "anthropics/skills", url: "https://github.com/anthropics/skills" }], onEditor: vi.fn(), onAdded: vi.fn(), ...over,
  }
  render(<AddSkillDialog {...props} />)
  return props
}

describe("helpers", () => {
  it("owner/repo becomes a GitHub URL", () => {
    expect(normalizeRepoInput("anthropics/skills")).toBe("https://github.com/anthropics/skills")
    expect(normalizeRepoInput("github.com/acme/skills/")).toBe("https://github.com/acme/skills")
    expect(normalizeRepoInput("https://git.example.com/x.git")).toBe("https://git.example.com/x.git")
    expect(pastedSkillName("---\nname: my-skill\ndescription: x\n---\nbody")).toBe("my-skill")
    expect(foundFromDryRun({ source: "", total_found: 1, total_imported: 0, imported: [], skipped: [{ path: "a/SKILL.md", reason: "parse: bad" }] })[0]).toMatchObject({ slug: "a", refused: "parse: bad" })
  })
})

describe("AddSkillDialog", () => {
  it("step 2 lists dry-run results; disallowed licence is disabled; step 3 gives to crews", async () => {
    h.bulk = []
    h.assign = []
    const p = renderDialog()
    fireEvent.click(screen.getByRole("button", { name: "anthropics/skills" }))
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Look for skills" }))
    })
    expect(h.bulk[0]).toMatchObject({ gitUrl: "https://github.com/anthropics/skills", dryRun: true })
    expect(screen.getByRole("checkbox", { name: "frontend-design" })).toBeChecked()
    expect(screen.getByRole("checkbox", { name: "docx" })).toBeDisabled()
    expect(screen.getByText("License not on the allow list · admins can override")).toBeInTheDocument()
    expect(screen.getByText("A skill with this slug exists · importing replaces it")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Continue" }))
    fireEvent.click(screen.getByRole("checkbox", { name: "Code review" }))
    expect(document.querySelector("[data-slot=add-skill-cli]")!.textContent).toContain(
      "crewship skill import --repo https://github.com/anthropics/skills --paths skills/frontend-design/SKILL.md,skills/webapp-testing/SKILL.md",
    )
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Add 2 skills" }))
    })
    expect(h.bulk[1].dryRun).toBeUndefined()
    expect(h.bulk[1]).toMatchObject({ paths: ["skills/frontend-design/SKILL.md", "skills/webapp-testing/SKILL.md"] })
    expect(h.assign.sort()).toEqual(["a1:id0", "a1:id1", "a2:id0", "a2:id1"])
    expect(p.onAdded).toHaveBeenCalledWith(["id0", "id1"])
  })

  it("Describe it and Write it open the editor", () => {
    const p = renderDialog()
    fireEvent.click(screen.getByRole("button", { name: /Describe it/ }))
    expect(p.onEditor).toHaveBeenCalledWith("describe")
    cleanup()
    const q = renderDialog()
    fireEvent.click(screen.getByRole("button", { name: /Write it/ }))
    expect(q.onEditor).toHaveBeenCalledWith("write")
  })

  it("no skills.sh, upload or Follow/Pin", () => {
    renderDialog()
    expect(screen.queryByText(/skills\.sh/)).not.toBeInTheDocument()
    expect(screen.queryByText(/Upload/)).not.toBeInTheDocument()
    expect(screen.queryByText(/Follow main|Pin this commit/)).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Paste SKILL\.md/ })).toBeInTheDocument()
  })
})
