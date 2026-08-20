import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

import { CanvasTabs, CanvasTabPanel, canvasTabIds } from "@/components/features/crews/canvas-base"

// =============================================================================
// Why these assertions are structural rather than visual.
//
// The strip used to be plain <button>s carrying `aria-selected`. That attribute
// is not allowed on the implicit `button` role (axe: aria-allowed-attr), so the
// one thing the markup tried to say — which section you are on — was the one
// thing a screen reader dropped. Both the agent overview and the crew canvas
// render this component, so the rule failed on two surfaces from one file.
//
// The pairing half is here for the reason #1978 exists: `role="tab"` and
// `role="tabpanel"` sitting in the same document as unrelated regions means
// switching tabs never moves the reading position to the panel that just
// appeared. Only the selected panel is mounted, so `aria-controls` has to be
// absent on the others — an id that resolves to nothing is its own violation.
// =============================================================================

const TABS = [
  { id: "overview", label: "Overview" },
  { id: "roster", label: "Roster" },
  { id: "settings", label: "Settings" },
] as const

type Tab = (typeof TABS)[number]["id"]

function renderStrip(active: Tab = "overview", onChange = vi.fn()) {
  return render(
    <>
      <CanvasTabs<Tab> tabs={TABS} active={active} onChange={onChange} idPrefix="crew-canvas" label="Crew sections" />
      <CanvasTabPanel idPrefix="crew-canvas" active={active}>
        panel body
      </CanvasTabPanel>
    </>,
  )
}

describe("<CanvasTabs>", () => {
  it("exposes a named tablist whose children are all tabs", () => {
    renderStrip()
    const list = screen.getByRole("tablist", { name: "Crew sections" })
    expect(list).toBeInTheDocument()
    // Every direct child must be a tab, or `aria-required-children` fails on
    // the tablist itself.
    expect(screen.getAllByRole("tab")).toHaveLength(TABS.length)
    for (const child of Array.from(list.children)) {
      expect(child.getAttribute("role")).toBe("tab")
    }
  })

  it("marks exactly one tab selected, and it is the active one", () => {
    renderStrip("roster")
    expect(screen.getByRole("tab", { name: "Roster" })).toHaveAttribute("aria-selected", "true")
    expect(screen.getByRole("tab", { name: "Overview" })).toHaveAttribute("aria-selected", "false")
    expect(screen.getByRole("tab", { name: "Settings" })).toHaveAttribute("aria-selected", "false")
  })

  it("points the selected tab at the panel that is actually mounted", () => {
    renderStrip("settings")
    const { tabId, panelId } = canvasTabIds("crew-canvas", "settings")

    const tab = screen.getByRole("tab", { name: "Settings" })
    expect(tab).toHaveAttribute("id", tabId)
    expect(tab).toHaveAttribute("aria-controls", panelId)

    const panel = screen.getByRole("tabpanel")
    expect(panel).toHaveAttribute("id", panelId)
    expect(panel).toHaveAttribute("aria-labelledby", tabId)
    // The pairing resolves in both directions — this is the check that would
    // have caught #1978 on the Pages strip.
    expect(document.getElementById(tab.getAttribute("aria-controls")!)).toBe(panel)
    expect(document.getElementById(panel.getAttribute("aria-labelledby")!)).toBe(tab)
  })

  it("omits aria-controls on the tabs whose panel is not rendered", () => {
    renderStrip("overview")
    for (const name of ["Roster", "Settings"]) {
      expect(screen.getByRole("tab", { name })).not.toHaveAttribute("aria-controls")
    }
  })

  it("still reports the clicked tab to the caller", () => {
    const onChange = vi.fn()
    renderStrip("overview", onChange)
    screen.getByRole("tab", { name: "Settings" }).click()
    expect(onChange).toHaveBeenCalledWith("settings")
  })

  it("keeps ids namespaced so two strips on one screen cannot collide", () => {
    expect(canvasTabIds("crew-canvas", "overview").tabId).not.toBe(
      canvasTabIds("agent-canvas", "overview").tabId,
    )
    expect(canvasTabIds("crew-canvas", "overview").panelId).not.toBe(
      canvasTabIds("crew-canvas", "overview").tabId,
    )
  })
})
