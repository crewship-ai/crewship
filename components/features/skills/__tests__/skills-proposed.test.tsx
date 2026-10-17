import { describe, expect, it, vi } from "vitest"
import { act, fireEvent, render, screen } from "@testing-library/react"
import { ProposedView } from "@/components/features/skills/skills-views"
import { visibleViews } from "@/components/features/skills/skills-explorer"
import { canManageSkills, type ProposedSkill } from "@/components/features/skills/skills-model"

vi.mock("next/link", () => ({ default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }))

const p: ProposedSkill = { crew_id: "c1", file_name: "skill-g2.md", name: "g2-scraper", description: "Read G2 pages.", description_quality: "OK", category: "RESEARCH" }

describe("Proposed by agents", () => {
  it("reject asks ConfirmDialog; approve adds to library", async () => {
    const onReview = vi.fn(async () => undefined)
    render(<ProposedView proposed={[p]} crews={[{ id: "c1", slug: "r", name: "Research", color: null, icon: null }]} busy={false} onReview={onReview} />)
    fireEvent.click(screen.getByRole("button", { name: "Approve and add" }))
    expect(onReview).toHaveBeenLastCalledWith(p, true)
    fireEvent.click(screen.getByRole("button", { name: "Reject" }))
    expect(onReview).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId("confirm-dialog")).toHaveTextContent("Reject g2-scraper?")
    await act(async () => {
      fireEvent.click(screen.getAllByRole("button", { name: "Reject" }).at(-1)!)
    })
    expect(onReview).toHaveBeenLastCalledWith(p, false)
  })

  it("MEMBER and MANAGER do not see the view; ADMIN and OWNER do", () => {
    expect(visibleViews(canManageSkills("MEMBER"))).not.toContain("proposed")
    expect(visibleViews(canManageSkills("MANAGER"))).not.toContain("proposed")
    expect(visibleViews(canManageSkills("ADMIN"))).toContain("proposed")
    expect(visibleViews(canManageSkills("OWNER"))).toContain("proposed")
  })
})
