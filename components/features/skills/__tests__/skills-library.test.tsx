import { describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { SkillsLibrary, type SkillsLibraryProps } from "@/components/features/skills/skills-library"
import type { SkillRow } from "@/components/features/skills/skills-model"

vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))

const skill = (i: number): SkillRow => ({
  id: `s${i}`, name: `skill-${String(i).padStart(3, "0")}`, slug: `skill-${i}`, display_name: null, description: `does ${i}`, version: null,
  author: null, category: i % 2 ? "CODING" : "DATA", source: "BUNDLED", icon: null, scan_status: "CLEAN", verification: "VERIFIED",
  usage: { uses_7d: i, errors_7d: 0, uses_total: i, last_used_at: null },
})
const rows = Array.from({ length: 120 }, (_, i) => skill(i))

function renderLib(over: Partial<SkillsLibraryProps> = {}) {
  const props: SkillsLibraryProps = {
    rows, sort: "used", group: "none", onSort: vi.fn(), onGroup: vi.fn(), onOpen: vi.fn(), onShowGroup: vi.fn(),
    canGive: true, onGiveToCrew: vi.fn(), phone: false, ...over,
  }
  render(<SkillsLibrary {...props} />)
  return props
}

describe("SkillsLibrary", () => {
  it("sort and group keep URL; 50 rows then more on scroll", () => {
    const p = renderLib()
    expect(screen.getAllByRole("row")).toHaveLength(51) // header + 50
    expect(screen.getByText("Showing 50 of 120")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Load 50 more" }))
    expect(screen.getAllByRole("row")).toHaveLength(101)
    fireEvent.click(screen.getByRole("radio", { name: "Name" }))
    expect(p.onSort).toHaveBeenCalledWith("name")
    fireEvent.click(screen.getByRole("radio", { name: "Domain" }))
    expect(p.onGroup).toHaveBeenCalledWith("domain")
  })

  it("is a table with the v67 columns and no Cards toggle", () => {
    renderLib()
    expect(screen.getAllByRole("columnheader").map((c) => c.textContent)).toEqual(["Select", "Skill", "Source", "Domain", "Trust", "Used by", "Uses · 7 d"])
    expect(screen.queryByRole("button", { name: "Cards" })).not.toBeInTheDocument()
  })

  it("groups into cards with a count and Show all", () => {
    const p = renderLib({ group: "domain" })
    expect(screen.getByRole("region", { name: "Coding · 60" })).toBeInTheDocument()
    fireEvent.click(screen.getAllByRole("button", { name: /Show all 60 in/ })[0])
    expect(p.onShowGroup).toHaveBeenCalledWith("domain", expect.any(String))
  })

  it("bulk bar offers Give to crew only", () => {
    const p = renderLib()
    fireEvent.click(screen.getByRole("checkbox", { name: "Select skill-119" }))
    const bar = screen.getByRole("toolbar", { name: "Selected skills" })
    expect(bar.textContent).toContain("1 selected")
    expect(bar.querySelectorAll("button")).toHaveLength(2)
    fireEvent.click(screen.getByRole("button", { name: "Give to crew" }))
    expect(p.onGiveToCrew).toHaveBeenCalledWith([expect.objectContaining({ id: "s119" })])
  })

  it("members see no checkboxes; a phone sees rows, not a table", () => {
    renderLib({ canGive: false })
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument()
    cleanup()
    renderLib({ phone: true })
    expect(screen.queryByRole("table")).not.toBeInTheDocument()
    expect(document.querySelectorAll("[data-slot=skills-phone-rows] li")).toHaveLength(50)
  })
})
