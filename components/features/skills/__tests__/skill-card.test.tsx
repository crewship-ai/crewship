import { describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen } from "@testing-library/react"
import { SkillCard } from "@/components/features/skills/skill-card"
import type { SkillRow } from "@/components/features/skills/skills-model"

vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: ({ alt }: { alt?: string }) => <img alt={alt ?? ""} data-testid="avatar" />,
}))

function makeSkill(over: Partial<SkillRow> = {}): SkillRow {
  return {
    id: "sk_1", name: "pdf-extract", slug: "pdf-extract", display_name: "Pdf Extract",
    description: "Use when the user asks to extract structured data from PDF files.",
    version: "1.0.0", author: "anthropic", category: "CODING", source: "BUNDLED", icon: null,
    vendor: "anthropic", maturity: "OFFICIAL", verification: "VERIFIED", scan_status: "CLEAN",
    needs_credentials: [], installed_on: [],
    usage: { uses_7d: 0, errors_7d: 0, uses_total: 0, last_used_at: null },
    ...over,
  }
}

const holder = (id: string, missing: string[] = []) => ({
  agent_id: id, agent_slug: id, agent_name: id, avatar_seed: null, avatar_style: null, crew_id: "c", crew_slug: "c",
  crew_name: "Crew", crew_color: null, crew_icon: null, crew_avatar_style: null, missing_credentials: missing,
})

describe("SkillCard", () => {
  it("names the skill, its origin, domain and one trust word", () => {
    render(<SkillCard skill={makeSkill()} onOpen={() => {}} />)
    expect(screen.getByText("Pdf Extract")).toBeDefined()
    expect(screen.getByText("anthropic/pdf-extract")).toBeDefined()
    expect(screen.getByText("Coding")).toBeDefined()
    expect(screen.getByText("Built-in")).toBeDefined()
    expect(screen.getByText("Verified")).toBeDefined()
    expect(screen.getByText("Not on any agent")).toBeDefined()
    expect(screen.getByText("never used")).toBeDefined()
  })

  it.each([
    [{ scan_status: "FLAGGED" }, "Flagged"],
    [{ scan_status: "UNSCANNED" }, "Not scanned"],
    [{ verification: "UNVERIFIED" }, "Unverified"],
  ])("trust %o reads %s", (over, word) => {
    render(<SkillCard skill={makeSkill(over)} onOpen={() => {}} />)
    expect(screen.getByText(word)).toBeDefined()
  })

  it("shows who holds it, what it needs and whether anyone lacks it", () => {
    render(
      <SkillCard
        skill={makeSkill({
          needs_credentials: ["GITHUB_TOKEN"],
          installed_on: [holder("ava"), holder("ben", ["GITHUB_TOKEN"])],
          usage: { uses_7d: 12, errors_7d: 0, uses_total: 40, last_used_at: new Date(Date.now() - 3_600_000).toISOString() },
        })}
        onOpen={() => {}}
      />,
    )
    expect(screen.getByText("2 agents")).toBeDefined()
    expect(screen.getAllByTestId("avatar")).toHaveLength(2)
    expect(screen.getByText("GITHUB_TOKEN")).toBeDefined()
    expect(screen.getByLabelText("Missing credential")).toBeDefined()
    expect(screen.getByText(/12 uses · 1h ago/)).toBeDefined()
  })

  it("opens the skill on click", () => {
    const onOpen = vi.fn()
    render(<SkillCard skill={makeSkill()} onOpen={onOpen} />)
    fireEvent.click(screen.getByTestId("skill-card"))
    expect(onOpen).toHaveBeenCalledWith("sk_1")
  })
})
