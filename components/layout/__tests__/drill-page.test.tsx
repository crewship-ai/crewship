import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, act, within } from "@testing-library/react"
import { Link2, Settings } from "lucide-react"

let mobile = false
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mobile }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => ({ ok: true, status: 200, json: async () => ({}) })) }))

import { useAppStore } from "@/lib/store"
import { DrillPage, DrillNavItem } from "../drill-page"

function renderPage(props: Partial<React.ComponentProps<typeof DrillPage>> = {}) {
  return render(
    <DrillPage
      parent={{ href: "/settings", label: "Settings", icon: Settings }}
      title="Crew links"
      panelId="drill-crew-links"
      icon={Link2}
      description="Who hands work to whom"
      nav={<DrillNavItem label="Engineering" onSelect={() => {}} />}
      {...props}
    >
      <div>content</div>
    </DrillPage>,
  )
}

let store: Map<string, string>
beforeEach(() => {
  cleanup()
  mobile = false
  store = new Map()
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  })
  act(() => {
    useAppStore.getState().setSecondaryPanel(null)
    useAppStore.getState().setSecondaryPanelOpen(false)
  })
})
afterEach(() => vi.unstubAllGlobals())

const panel = () => document.querySelector<HTMLElement>("[data-slot=secondary-panel]")

// The nested page had the path twice: "Settings / Crew links" on the sub-bar
// and again at the top of its panel ("← Settings", "Crew links", the
// description), where the way back was small grey text. Now there is one
// bordered back control, and the page is named once.
describe("DrillPage header", () => {
  it("has exactly one way back, to the parent, labelled as a control", () => {
    renderPage()
    const back = screen.getAllByRole("link", { name: "Back to Settings" })
    expect(back).toHaveLength(1)
    expect(back[0]).toHaveAttribute("href", "/settings")
    expect(back[0]).toHaveTextContent("Settings")
  })

  it("names the page once, with its description on the sub-bar", () => {
    renderPage()
    expect(screen.getAllByText("Crew links")).toHaveLength(1)
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Crew links")
    expect(screen.getAllByText("Who hands work to whom")).toHaveLength(1)
  })

  it("keeps the back button on a phone as an arrow on the sub-bar", () => {
    mobile = true
    renderPage()
    const back = screen.getByRole("link", { name: "Back to Settings" })
    expect(back).not.toHaveTextContent("Settings")
  })

  it("keeps the way back on the sub-bar for a page without a panel", () => {
    renderPage({ nav: undefined })
    expect(panel()).toBeNull()
    expect(screen.getAllByRole("link", { name: "Back to Settings" })).toHaveLength(1)
  })
})

// Harbor 2 M3 (#3350): the nested page's panel is the shared SecondaryPanel.
describe("DrillPage panel", () => {
  it("renders inside the SecondaryPanel with its drill id, the way back first", () => {
    renderPage()
    const aside = panel()!
    expect(aside).toHaveAttribute("data-panel-id", "drill-crew-links")
    expect(aside).toHaveAccessibleName("Crew links navigation")
    const nav = within(aside).getByRole("navigation", { name: "Crew links sections" })
    const back = within(nav).getByRole("link", { name: "Back to Settings" })
    expect(nav.firstElementChild).toContainElement(back)
  })

  it("collapses through the panel and remembers it per user", () => {
    renderPage()
    fireEvent.click(screen.getByRole("button", { name: "Hide Crew links navigation" }))
    expect(panel()).toHaveAttribute("data-collapsed", "true")
    expect(screen.queryByText("Engineering")).toBeNull()
    expect(store.get("crewship.pref.panel.drill-crew-links.collapsed")).toBe("true")
  })

  it("starts from the collapse the user set under the old key, once", () => {
    store.set("drill-collapsed:Crew links", "1")
    renderPage()
    expect(panel()).toHaveAttribute("data-collapsed", "true")
    expect(store.has("drill-collapsed:Crew links")).toBe(false)
    expect(store.get("crewship.pref.panel.drill-crew-links.collapsed")).toBe("true")
  })

  it("does not let the old key override a choice already made in the panel", () => {
    store.set("drill-collapsed:Crew links", "1")
    store.set("crewship.pref.panel.drill-crew-links.collapsed", "false")
    renderPage()
    expect(panel()).toHaveAttribute("data-collapsed", "false")
  })

  it("on a phone puts the panel in the sheet and closes it on a pick", () => {
    mobile = true
    renderPage()
    expect(panel()).toBeNull()
    expect(screen.queryByText("Engineering")).toBeNull()
    expect(screen.queryByRole("button", { name: /Filters/ })).toBeNull()
    act(() => useAppStore.getState().setSecondaryPanelOpen(true))
    const sheet = screen.getByRole("dialog", { name: "Crew links navigation" })
    fireEvent.click(within(sheet).getByRole("button", { name: "Engineering" }))
    expect(useAppStore.getState().secondaryPanelOpen).toBe(false)
  })
})
