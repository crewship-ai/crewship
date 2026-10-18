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

  // SET-07 kit request 1: the top bar is the path, the page owns the head.
  it("names the place in the top bar as a path, 'Settings / Crew links'", () => {
    renderPage()
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Settings / Crew links")
  })

  it("draws the section head once: icon tile, title, and the sentence once", () => {
    renderPage({ note: "Managers and up change links" })
    const head = document.querySelector<HTMLElement>("[data-slot=drill-head]")!
    expect(head).not.toBeNull()
    expect(within(head).getByRole("heading", { level: 2, name: "Crew links" })).toBeInTheDocument()
    expect(head.querySelector(".page-tile")).not.toBeNull()
    expect(screen.getAllByText("Who hands work to whom")).toHaveLength(1)
    expect(within(head).getByText("Managers and up change links")).toHaveAttribute("data-slot", "drill-head-note")
  })

  it("leaves the head out for a page that shows only a no-access state", () => {
    renderPage({ head: false })
    expect(document.querySelector("[data-slot=drill-head]")).toBeNull()
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Settings / Crew links")
  })

  it("on a phone the top bar names the page, with the arrow back before it (390 px)", () => {
    mobile = true
    renderPage()
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent(/^Crew links$/)
    expect(document.querySelector("[data-slot=drill-head]")).not.toBeNull()
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
    const back = within(aside).getByRole("link", { name: "Back to Settings" })
    expect(aside.querySelector("[data-slot=drill-back-row]")).toContainElement(back)
  })

  // SET-07 kit request 3 (v67 pk-back): the back row is above the toolbar.
  it("puts the back row above the panel toolbar, outside the sections nav", () => {
    renderPage({ toolbar: <input aria-label="Search crews" /> })
    const aside = panel()!
    const back = aside.querySelector("[data-slot=drill-back-row]")!
    const search = within(aside).getByLabelText("Search crews")
    expect(back.compareDocumentPosition(search) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(within(aside).getByRole("navigation", { name: "Crew links sections" })).not.toContainElement(back as HTMLElement)
  })

  // SET-07 kit request 2: a slot directly under the toolbar for the facets.
  it("has a facets slot right under the toolbar, before the sections", () => {
    renderPage({ toolbar: <input aria-label="Search crews" />, facets: <div data-testid="facets">facets</div> })
    const aside = panel()!
    const search = within(aside).getByLabelText("Search crews")
    const facets = within(aside).getByTestId("facets")
    const nav = within(aside).getByRole("navigation", { name: "Crew links sections" })
    expect(search.compareDocumentPosition(facets) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(facets.compareDocumentPosition(nav) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(nav).not.toContainElement(facets)
  })

  // SET-07 kit request 4: spaces on keys 1 to 9.
  it("lists spaces under the facets and switches them on the number keys", () => {
    const onChange = vi.fn()
    renderPage({
      spaces: {
        items: [{ key: "crew", label: "By crew", icon: Link2 }, { key: "all", label: "All links", icon: Link2, count: 4 }],
        value: "crew",
        onChange,
      },
    })
    const tabs = within(panel()!).getAllByRole("tab")
    expect(tabs.map((t) => t.getAttribute("aria-keyshortcuts"))).toEqual(["1", "2"])
    expect(tabs[0]).toHaveAttribute("aria-selected", "true")
    fireEvent.keyDown(window, { key: "2" })
    expect(onChange).toHaveBeenCalledWith("all")
  })

  it("on a phone the spaces are in the panel sheet and are 44 px rows (390 px)", () => {
    mobile = true
    renderPage({ spaces: { items: [{ key: "crew", label: "By crew", icon: Link2 }, { key: "all", label: "All links", icon: Link2 }], value: "crew", onChange: () => {} } })
    act(() => useAppStore.getState().setSecondaryPanelOpen(true))
    const sheet = screen.getByRole("dialog", { name: "Crew links navigation" })
    const tab = within(sheet).getByRole("tab", { name: /All links/ })
    expect(tab.className).toMatch(/kit-tap/)
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

// SET-07 on a tablet (768 to 1279 px, 1024 in the check): the panel floats and
// starts closed, so the way back cannot live only inside it.
describe("DrillPage on a tablet (1024 px)", () => {
  beforeEach(() => {
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("1279"), media: q, addEventListener: () => {}, removeEventListener: () => {} }))
  })

  it("keeps the way back on the top bar, with the parent's name, while the panel is closed", () => {
    renderPage()
    expect(panel()).toBeNull()
    const back = screen.getByRole("link", { name: "Back to Settings" })
    expect(back).toHaveAttribute("href", "/settings")
    expect(back).toHaveTextContent("Settings")
  })

  it("shows the path, the head and the content as on a desktop", () => {
    renderPage({ note: "Managers and up change links" })
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Settings / Crew links")
    expect(document.querySelector("[data-slot=drill-head]")).not.toBeNull()
    expect(screen.getByText("content")).toBeInTheDocument()
  })

  it("has the way back once when the panel is opened", () => {
    renderPage()
    act(() => useAppStore.getState().setSecondaryPanelOpen(true))
    expect(screen.getAllByRole("link", { name: "Back to Settings" })).toHaveLength(1)
  })
})
