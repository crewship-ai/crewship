import { describe, it, expect, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

// =============================================================================
// The right rail is three 16px glyphs and nothing else.
//
// The owner had to guess what they were: a page icon, a lightning bolt and two
// people, stacked on a 48px strip, opening a drawer that carried no title. The
// icons had an sr-only label and a hover tooltip — which is exactly the set of
// affordances nobody looking at the screen has.
//
// So: the name is in three places that read from ONE map (right-rail's
// DRAWER_TAB_LABELS) — the tooltip, the drawer's accessible name, and the
// panel's own heading — and the keyboard shortcut the tooltip draws is
// exposed to assistive tech instead of being visual-only.
// =============================================================================

import { RightRail, DRAWER_TAB_LABELS } from "../right-rail"
import { RightDrawer } from "../right-drawer"
import { useDrawerStore } from "@/stores/drawer-store"

beforeEach(() => {
  useDrawerStore.setState({ open: false, activeTab: "files", mode: "push", width: 380 })
})
afterEach(() => cleanup())

describe("RightRail — the controls say what they are", () => {
  it("gives every control an accessible name", () => {
    render(<RightRail />)

    for (const label of ["Files", "Triggers", "Team"]) {
      expect(screen.getByRole("tab", { name: label })).toBeInTheDocument()
    }
    expect(screen.getByRole("tablist", { name: /side panels/i })).toBeInTheDocument()
  })

  it("exposes the keyboard shortcut it draws in the tooltip", () => {
    render(<RightRail />)

    expect(screen.getByRole("tab", { name: "Files" })).toHaveAttribute("aria-keyshortcuts", "Meta+1")
    expect(screen.getByRole("tab", { name: "Triggers" })).toHaveAttribute("aria-keyshortcuts", "Meta+2")
    expect(screen.getByRole("tab", { name: "Team" })).toHaveAttribute("aria-keyshortcuts", "Meta+3")
  })

  it("marks the open panel as the selected tab", () => {
    render(<RightRail />)

    fireEvent.click(screen.getByRole("tab", { name: "Team" }))

    expect(screen.getByRole("tab", { name: "Team" })).toHaveAttribute("aria-selected", "true")
    expect(screen.getByRole("tab", { name: "Files" })).toHaveAttribute("aria-selected", "false")
  })
})

describe("RightDrawer — the open panel is named", () => {
  it("names the panel after the tab that opened it", () => {
    useDrawerStore.setState({ open: true, activeTab: "triggers", mode: "push" })

    render(<RightDrawer><div /></RightDrawer>)

    expect(screen.getByRole("tabpanel", { name: "Triggers" })).toBeInTheDocument()
  })

  it("reads its name from the same map the rail does", () => {
    useDrawerStore.setState({ open: true, activeTab: "team", mode: "push" })

    render(<RightDrawer><div /></RightDrawer>)

    expect(screen.getByRole("tabpanel", { name: DRAWER_TAB_LABELS.team })).toBeInTheDocument()
  })
})
