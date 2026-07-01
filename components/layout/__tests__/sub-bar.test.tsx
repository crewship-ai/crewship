import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"
import { CircleDot, List } from "lucide-react"

import { SubBar, SubBarPrimary, SubBarSecondary, SubBarIconButton } from "../sub-bar"

describe("SubBar", () => {
  beforeEach(() => cleanup())

  it("always renders identity: title + description (row 1 is never empty)", () => {
    render(<SubBar icon={CircleDot} title="Issues" description="15 open · 15 backlog" />)
    expect(screen.getByText("Issues")).toBeTruthy()
    expect(screen.getByText(/15 open/)).toBeTruthy()
  })

  it("does NOT render the second row when there are no tabs and no tools", () => {
    render(<SubBar title="Crews & Agents" description="2 crews · 5 agents" />)
    // No tablist landmark when tabs are absent.
    expect(screen.queryByRole("tablist")).toBeNull()
  })

  it("renders a tablist and fires onTabChange when a tab is clicked", () => {
    const onTabChange = vi.fn()
    render(
      <SubBar
        title="Routines"
        description="3 in workspace"
        tabs={[
          { id: "list", label: "List", icon: List },
          { id: "schedules", label: "Schedules" },
        ]}
        activeTab="list"
        onTabChange={onTabChange}
      />,
    )
    expect(screen.getByRole("tablist")).toBeTruthy()
    const schedules = screen.getByRole("tab", { name: /Schedules/ })
    fireEvent.click(schedules)
    expect(onTabChange).toHaveBeenCalledWith("schedules")
  })

  it("marks the active tab with aria-selected", () => {
    render(
      <SubBar
        title="Skills"
        description="22 total"
        tabs={[
          { id: "browse", label: "Browse" },
          { id: "installed", label: "Installed" },
        ]}
        activeTab="browse"
        onTabChange={vi.fn()}
      />,
    )
    expect(screen.getByRole("tab", { name: /Browse/ }).getAttribute("aria-selected")).toBe("true")
    expect(screen.getByRole("tab", { name: /Installed/ }).getAttribute("aria-selected")).toBe("false")
  })

  it("does not fire onTabChange for a locked tab", () => {
    const onTabChange = vi.fn()
    render(
      <SubBar
        title="Crew Journal"
        description="4133 loaded"
        tabs={[
          { id: "timeline", label: "Timeline" },
          { id: "spend", label: "Spend", locked: true },
        ]}
        activeTab="timeline"
        onTabChange={onTabChange}
      />,
    )
    fireEvent.click(screen.getByRole("tab", { name: /Spend/ }))
    expect(onTabChange).not.toHaveBeenCalled()
  })

  it("renders row-1 actions and row-2 tools", () => {
    render(
      <SubBar
        title="Routines"
        description="3"
        tabs={[{ id: "list", label: "List" }]}
        activeTab="list"
        onTabChange={vi.fn()}
        actions={<SubBarPrimary>New routine</SubBarPrimary>}
        tools={<button>Filter</button>}
      />,
    )
    expect(screen.getByRole("button", { name: /New routine/ })).toBeTruthy()
    expect(screen.getByRole("button", { name: /Filter/ })).toBeTruthy()
  })

  it("action helpers use the shared Button (soft = primary, ghost = secondary)", () => {
    render(
      <SubBar
        title="Issues"
        description="x"
        actions={
          <>
            <SubBarSecondary>New Project</SubBarSecondary>
            <SubBarPrimary>New Issue</SubBarPrimary>
            <SubBarIconButton aria-label="Settings">⚙</SubBarIconButton>
          </>
        }
      />,
    )
    expect(screen.getByRole("button", { name: /New Issue/ }).getAttribute("data-variant")).toBe("soft")
    expect(screen.getByRole("button", { name: /New Project/ }).getAttribute("data-variant")).toBe("ghost")
    expect(screen.getByRole("button", { name: /Settings/ }).getAttribute("data-variant")).toBe("ghost")
  })
})
