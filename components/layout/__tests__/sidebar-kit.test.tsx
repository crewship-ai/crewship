import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"

import {
  SidebarSearch,
  SidebarFilterButton,
  SidebarViewButton,
  SidebarSection,
  SidebarRow,
  SidebarActiveChip,
  SidebarCollapseButton,
} from "../sidebar-kit"

describe("sidebar-kit", () => {
  beforeEach(() => cleanup())

  it("SidebarSearch is controlled and fires onValueChange", () => {
    const onValueChange = vi.fn()
    render(<SidebarSearch value="" onValueChange={onValueChange} placeholder="Search issues…" />)
    const input = screen.getByPlaceholderText("Search issues…")
    fireEvent.change(input, { target: { value: "dns" } })
    expect(onValueChange).toHaveBeenCalledWith("dns")
  })

  it("SidebarSearch shows a clear button only when there's a value, and clears", () => {
    const onValueChange = vi.fn()
    const { rerender } = render(<SidebarSearch value="" onValueChange={onValueChange} />)
    expect(screen.queryByRole("button", { name: /clear search/i })).toBeNull()
    rerender(<SidebarSearch value="dns" onValueChange={onValueChange} />)
    fireEvent.click(screen.getByRole("button", { name: /clear search/i }))
    expect(onValueChange).toHaveBeenCalledWith("")
  })

  it("SidebarFilterButton shows a count badge and active styling when filters apply", () => {
    render(<SidebarFilterButton activeCount={2} />)
    const btn = screen.getByRole("button", { name: /filter/i })
    expect(btn.textContent).toContain("2")
    expect(btn.className).toContain("border-primary/30")
  })

  it("SidebarFilterButton has no badge and inactive styling when count is 0", () => {
    render(<SidebarFilterButton activeCount={0} />)
    const btn = screen.getByRole("button", { name: /filter/i })
    expect(btn.className).toContain("border-white/[0.08]")
  })

  it("SidebarViewButton renders an accessible view control", () => {
    render(<SidebarViewButton />)
    expect(screen.getByRole("button", { name: /view/i })).toBeTruthy()
  })

  it("SidebarSection toggles collapse via its header button", () => {
    const onToggle = vi.fn()
    render(
      <SidebarSection label="Projects" count={5} collapsible collapsed={false} onToggle={onToggle}>
        <div>child-content</div>
      </SidebarSection>,
    )
    expect(screen.getByText("child-content")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: /projects/i }))
    expect(onToggle).toHaveBeenCalled()
  })

  it("SidebarSection hides children when collapsed", () => {
    render(
      <SidebarSection label="Projects" collapsible collapsed>
        <div>child-content</div>
      </SidebarSection>,
    )
    expect(screen.queryByText("child-content")).toBeNull()
  })

  it("SidebarRow fires onSelect and reflects selected state via ListRow", () => {
    const onSelect = vi.fn()
    render(
      <SidebarRow selected onSelect={onSelect}>
        <span>File Operations</span>
      </SidebarRow>,
    )
    const row = screen.getByRole("button", { name: /file operations/i })
    expect(row.getAttribute("data-selected")).toBe("true")
    fireEvent.click(row)
    expect(onSelect).toHaveBeenCalled()
  })

  it("SidebarCollapseButton toggles and reflects collapsed state in its label", () => {
    const onToggle = vi.fn()
    const { rerender } = render(<SidebarCollapseButton collapsed={false} onToggle={onToggle} />)
    fireEvent.click(screen.getByRole("button", { name: /collapse sidebar/i }))
    expect(onToggle).toHaveBeenCalled()
    rerender(<SidebarCollapseButton collapsed onToggle={onToggle} />)
    expect(screen.getByRole("button", { name: /expand sidebar/i })).toBeTruthy()
  })

  it("SidebarActiveChip renders a removable chip", () => {
    const onRemove = vi.fn()
    render(<SidebarActiveChip onRemove={onRemove}>Source: Manual</SidebarActiveChip>)
    expect(screen.getByText("Source: Manual")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: /remove filter/i }))
    expect(onRemove).toHaveBeenCalled()
  })
})
