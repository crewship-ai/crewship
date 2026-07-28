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

  // A page with an internal nav (Admin's console sections, Settings' tabs)
  // has to say WHICH section you are in. Without this the chrome above the
  // fold is identical on Admin/Overview and Admin/Backups, and the only clue
  // is a highlighted row in the sidebar.
  it("renders the active section after the title as a path", () => {
    render(<SubBar title="Admin Console" section="Users" />)
    const heading = screen.getByRole("heading", { level: 1 })
    expect(heading).toHaveTextContent("Admin Console / Users")
  })

  it("emphasises the section and demotes the page it belongs to", () => {
    render(<SubBar title="Settings" section="Audit Log" />)
    // Reads as a path — "Settings" recedes, "Audit Log" is where you are.
    // Both bold would look like two competing headings on one row.
    expect(screen.getByText("Settings").className).toMatch(/text-muted-foreground/)
    expect(screen.getByText("Audit Log").className).toMatch(/text-foreground/)
  })

  it("leaves the title alone when a page has no sections", () => {
    render(<SubBar title="Skills" description="22 total" />)
    const heading = screen.getByRole("heading", { level: 1 })
    expect(heading).toHaveTextContent("Skills")
    expect(heading.textContent).not.toContain("/")
    // Section-less pages keep the full-strength title they have today.
    expect(screen.getByText("Skills").className).toMatch(/text-foreground/)
    expect(screen.getByText("Skills").className).not.toMatch(/text-muted-foreground/)
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
