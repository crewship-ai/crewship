import { describe, expect, it, vi } from "vitest"
import { act, fireEvent, render, screen } from "@testing-library/react"
import { GiveToPicker, assignmentDiff, holdingState } from "@/components/features/skills/give-to-picker"
import type { SkillAgentRef, SkillRow, SkillsAgent, SkillsCrew } from "@/components/features/skills/skills-model"

vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))
const h = vi.hoisted(() => ({ mutate: (async () => undefined) as (p: { agentId: string; skillId: string; on: boolean }) => Promise<void> }))
vi.mock("@/hooks/use-skills", () => ({ useSetSkillAssignment: () => ({ mutateAsync: (p: { agentId: string; skillId: string; on: boolean }) => h.mutate(p) }) }))

const crews: SkillsCrew[] = [{ id: "c1", slug: "review", name: "Code review", color: null, icon: null }]
const agents: SkillsAgent[] = [
  { id: "a1", slug: "rev", name: "Reviewer", crew_id: "c1" },
  { id: "a2", slug: "lint", name: "Linter", crew_id: "c1" },
]
const ref = (id: string): SkillAgentRef => ({
  agent_id: id, agent_slug: id, agent_name: id, avatar_seed: null, avatar_style: null, crew_id: "c1", crew_slug: "review",
  crew_name: "Code review", crew_color: null, crew_icon: null, crew_avatar_style: null,
})
const skill = (id: string, holders: string[]): SkillRow => ({
  id, name: id, slug: id, display_name: id, description: null, version: null, author: null, category: "CODING", source: "BUNDLED",
  icon: null, scan_status: "CLEAN", verification: "VERIFIED", installed_on: holders.map(ref),
})

describe("assignmentDiff", () => {
  it("Apply sends only the diff", () => {
    const skills = [skill("s1", ["a1"]), skill("s2", [])]
    const initial = new Map(agents.map((a) => [a.id, holdingState(skills, a.id)]))
    expect(initial.get("a1")).toBe("indeterminate")
    // a1 → all: only the missing s2; a2 unchanged: nothing.
    expect(assignmentDiff(skills, initial, new Map([["a1", true], ["a2", false]]))).toEqual([{ agentId: "a1", skillId: "s2", on: true }])
    expect(assignmentDiff(skills, initial, new Map([["a1", false], ["a2", false]]))).toEqual([{ agentId: "a1", skillId: "s1", on: false }])
  })
})

describe("GiveToPicker", () => {
  it("tri-state crew; Apply sends only the diff", async () => {
    const calls: string[] = []
    h.mutate = async (p) => void calls.push(`${p.agentId}:${p.skillId}:${p.on}`)
    const onClose = vi.fn()
    render(<GiveToPicker skills={[skill("s1", ["a1"])]} workspaceId="ws" agents={agents} crews={crews} onClose={onClose} />)
    const crew = screen.getByRole("checkbox", { name: "Give to the whole Code review crew" })
    expect(crew).toHaveAttribute("data-state", "indeterminate")
    fireEvent.click(crew)
    expect(screen.getByText("1 added · 0 removed")).toBeInTheDocument()
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Apply" }))
    })
    expect(calls).toEqual(["a2:s1:true"])
    expect(onClose).toHaveBeenCalled()
  })

  it("partial failure keeps the dialog open with the failed agents", async () => {
    h.mutate = async (p) => {
      if (p.agentId === "a2") throw new Error("no")
    }
    const onClose = vi.fn()
    render(<GiveToPicker skills={[skill("s1", []), skill("s2", [])]} workspaceId="ws" agents={agents} crews={crews} onClose={onClose} />)
    fireEvent.click(screen.getByRole("checkbox", { name: "Give to the whole Code review crew" }))
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Apply" }))
    })
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole("alert")).toHaveTextContent("2 changes failed for Linter")
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument()
  })
})
