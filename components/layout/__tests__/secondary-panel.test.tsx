import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, act } from "@testing-library/react"

// Harbor 2 M3: one secondary panel for every section (#3350).

let mobile = false
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mobile }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => ({ ok: true, status: 200, json: async () => ({}) })) }))

import { useAppStore } from "@/lib/store"
import { Link2 } from "lucide-react"
import { SecondaryPanel } from "../secondary-panel"
import { SidebarToolbar } from "../sidebar-kit"

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
  // happy-dom's window is 1024 px wide, a tablet; the cases below are about
  // the desktop column unless they say otherwise (environment only).
  vi.stubGlobal("matchMedia", (q: string) => ({ matches: false, media: q, addEventListener: () => {}, removeEventListener: () => {} }))
  act(() => {
    useAppStore.getState().setSecondaryPanel(null)
    useAppStore.getState().setSecondaryPanelOpen(false)
  })
})

afterEach(() => vi.unstubAllGlobals())

describe("SecondaryPanel", () => {
  it("is a labelled 272 px column with a toolbar, a body and a collapse button", () => {
    render(<SecondaryPanel id="issues" label="Issues views" toolbar={<input aria-label="Search" />}><p>Views</p></SecondaryPanel>)
    const aside = screen.getByRole("complementary", { name: "Issues views" })
    expect(aside).toHaveClass("secondary-panel")
    expect(aside).toHaveAttribute("data-collapsed", "false")
    expect(screen.getByRole("textbox", { name: "Search" })).toBeInTheDocument()
    expect(screen.getByText("Views")).toBeInTheDocument()
  })

  it("collapses to a rail and remembers it per section", () => {
    render(<SecondaryPanel id="issues" label="Issues views"><p>Views</p></SecondaryPanel>)
    fireEvent.click(screen.getByRole("button", { name: "Hide Issues views" }))
    expect(screen.getByRole("complementary")).toHaveAttribute("data-collapsed", "true")
    expect(screen.queryByText("Views")).toBeNull()
    expect(store.get("crewship.pref.panel.issues.collapsed")).toBe("true")
    cleanup()
    render(<SecondaryPanel id="issues" label="Issues views"><p>Views</p></SecondaryPanel>)
    expect(screen.getByRole("complementary")).toHaveAttribute("data-collapsed", "true")
    cleanup()
    render(<SecondaryPanel id="inbox" label="Inbox views"><p>Views</p></SecondaryPanel>)
    expect(screen.getByRole("complementary")).toHaveAttribute("data-collapsed", "false")
  })

  it("registers itself for the phone top bar while mounted", () => {
    const { unmount } = render(<SecondaryPanel id="issues" label="Issues views" />)
    expect(useAppStore.getState().secondaryPanel).toEqual({ id: "issues", label: "Issues views" })
    unmount()
    expect(useAppStore.getState().secondaryPanel).toBeNull()
  })

  it("on a phone is a bottom sheet opened from the top bar, not a column", () => {
    mobile = true
    render(<SecondaryPanel id="issues" label="Issues views"><p>Views</p></SecondaryPanel>)
    expect(screen.queryByRole("complementary")).toBeNull()
    expect(screen.queryByText("Views")).toBeNull()
    act(() => useAppStore.getState().setSecondaryPanelOpen(true))
    expect(screen.getByRole("dialog", { name: "Issues views" })).toBeInTheDocument()
    expect(screen.getByText("Views")).toBeInTheDocument()
  })

  it("keeps a footer for bulk actions", () => {
    render(<SecondaryPanel id="x" label="X" footer={<button type="button">Archive 3</button>} />)
    expect(screen.getByRole("button", { name: "Archive 3" }).parentElement).toHaveClass("secondary-panel-footer")
  })

  it("turns a kit SidebarToolbar inside it into the panel toolbar with one collapse button", () => {
    render(
      <SecondaryPanel id="issues" label="Issues views">
        <SidebarToolbar><input aria-label="Search issues" /></SidebarToolbar>
        <p>Views</p>
      </SecondaryPanel>,
    )
    const toolbar = screen.getByRole("textbox", { name: "Search issues" }).parentElement!
    expect(toolbar).toHaveClass("secondary-panel-toolbar")
    expect(screen.getAllByRole("button", { name: "Hide Issues views" })).toHaveLength(1)
    expect(toolbar).toContainElement(screen.getByRole("button", { name: "Hide Issues views" }))
  })

  // COM-05 (#3481, PNL-01/05): at 768–1279 the panel starts closed, opens
  // from the top bar's panel button (v67 panelBtn) and floats over the
  // content instead of taking 272 px from it.
  describe("on a tablet", () => {
    let tablet = true
    beforeEach(() => {
      tablet = true
      vi.stubGlobal("matchMedia", (q: string) => ({
        matches: q.includes("1279") ? tablet : false,
        media: q,
        addEventListener: () => {},
        removeEventListener: () => {},
      }))
    })

    it("starts closed, out of the layout, and registers for the top bar", () => {
      render(<SecondaryPanel id="routines" label="Routines views"><p>Views</p></SecondaryPanel>)
      expect(screen.queryByText("Views")).toBeNull()
      expect(screen.queryByRole("complementary")).toBeNull()
      expect(useAppStore.getState().secondaryPanel).toEqual({ id: "routines", label: "Routines views" })
    })

    it("ignores a remembered desktop collapse", () => {
      store.set("crewship.pref.panel.routines.collapsed", "true")
      render(<SecondaryPanel id="routines" label="Routines views"><p>Views</p></SecondaryPanel>)
      act(() => useAppStore.getState().setSecondaryPanelOpen(true))
      expect(screen.getByText("Views")).toBeInTheDocument()
    })

    it("opens floating over the content and closes from its button, Escape or the scrim", () => {
      render(<SecondaryPanel id="routines" label="Routines views"><p>Views</p></SecondaryPanel>)
      act(() => useAppStore.getState().setSecondaryPanelOpen(true))
      const aside = screen.getByRole("complementary", { name: "Routines views" })
      expect(aside).toHaveAttribute("data-float", "true")
      expect(aside).toHaveClass("secondary-panel-float")
      fireEvent.click(screen.getByRole("button", { name: "Close Routines views" }))
      expect(useAppStore.getState().secondaryPanelOpen).toBe(false)
      expect(screen.queryByText("Views")).toBeNull()

      act(() => useAppStore.getState().setSecondaryPanelOpen(true))
      fireEvent.keyDown(screen.getByText("Views"), { key: "Escape" })
      expect(useAppStore.getState().secondaryPanelOpen).toBe(false)

      act(() => useAppStore.getState().setSecondaryPanelOpen(true))
      fireEvent.click(screen.getByTestId("secondary-panel-scrim"))
      expect(useAppStore.getState().secondaryPanelOpen).toBe(false)
    })

    it("stays a column on a desktop", () => {
      tablet = false
      render(<SecondaryPanel id="routines" label="Routines views"><p>Views</p></SecondaryPanel>)
      expect(screen.getByRole("complementary")).not.toHaveAttribute("data-float")
      expect(screen.getByText("Views")).toBeInTheDocument()
    })
  })

  it("on a phone can be the page itself (list first), without a sheet button", () => {
    mobile = true
    render(<SecondaryPanel id="inbox" label="Inbox list" phoneInline><p>Rows</p></SecondaryPanel>)
    expect(screen.getByText("Rows")).toBeVisible()
    expect(screen.getByRole("region", { name: "Inbox list" })).toHaveAttribute("data-phone", "inline")
    expect(useAppStore.getState().secondaryPanel).toBeNull()
  })
})

// SET-07 kit request 4: the panel takes spaces (keys 1 to 9).
describe("SecondaryPanel spaces", () => {
  const items = [{ key: "a", label: "Alpha", icon: Link2 }, { key: "b", label: "Beta", icon: Link2, count: 3 }, { key: "c", label: "Gamma", icon: Link2 }] as const

  it("lists them as a tablist under the toolbar with the key printed and announced", () => {
    render(<SecondaryPanel id="x" label="X views" toolbar={<input aria-label="Search" />} spaces={{ items, value: "a", onChange: () => {} }}><p>Rows</p></SecondaryPanel>)
    const list = screen.getByRole("tablist", { name: "Spaces" })
    expect(screen.getByLabelText("Search").compareDocumentPosition(list) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    const tabs = screen.getAllByRole("tab")
    expect(tabs.map((t) => t.getAttribute("aria-keyshortcuts"))).toEqual(["1", "2", "3"])
    expect(tabs[0]).toHaveAttribute("aria-selected", "true")
    expect(tabs[1]).toHaveTextContent("3")
  })

  it("switches on 1 to 9, but not while typing, with a modifier, or with a dialog open", () => {
    const onChange = vi.fn()
    render(<SecondaryPanel id="x" label="X views" toolbar={<input aria-label="Search" />} spaces={{ items, value: "a", onChange }}><p>Rows</p></SecondaryPanel>)
    fireEvent.keyDown(window, { key: "3" })
    expect(onChange).toHaveBeenLastCalledWith("c")
    onChange.mockClear()
    fireEvent.keyDown(screen.getByLabelText("Search"), { key: "2" })
    fireEvent.keyDown(window, { key: "2", ctrlKey: true })
    fireEvent.keyDown(window, { key: "9" })
    expect(onChange).not.toHaveBeenCalled()
  })

  it("is in the phone sheet too, as 44 px rows (390 px)", () => {
    mobile = true
    render(<SecondaryPanel id="x" label="X views" spaces={{ items, value: "a", onChange: () => {} }}><p>Rows</p></SecondaryPanel>)
    act(() => useAppStore.getState().setSecondaryPanelOpen(true))
    expect(screen.getAllByRole("tab")[1].className).toMatch(/kit-tap/)
  })
})
