import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, render, screen } from "@testing-library/react"

// The app skeleton the owner chose on 2026-10-18 (v67 at every width):
// desktop starts with the sidebar pinned open; a tablet shows the rail, with a
// list page's panel docked beside the content; a phone has one top bar.

vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/hooks/use-user-preference", () => ({ useUserPreference: (_k: string, d: unknown) => [d, () => {}] }))

import { readFileSync } from "node:fs"
import path from "node:path"
import { SidebarProvider, useSidebar } from "@/components/ui/sidebar"
import { SecondaryPanel } from "@/components/layout/secondary-panel"
import { useAppStore } from "@/lib/store"
import { SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import { Plus, Upload } from "lucide-react"
import { DEFAULT_SIDEBAR } from "@/lib/theme/appearance"

const store = new Map<string, string>()
let width = 1440

function setWidth(w: number) {
  width = w
}

beforeEach(() => {
  store.clear()
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, String(v)),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  })
  // Enough of matchMedia for min/max-width queries.
  vi.stubGlobal("matchMedia", (query: string) => {
    const min = /min-width:\s*(\d+)px/.exec(query)
    const max = /max-width:\s*(\d+)px/.exec(query)
    const matches = (!min || width >= Number(min[1])) && (!max || width <= Number(max[1]))
    return { matches, media: query, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} }
  })
  setWidth(1440)
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function Probe() {
  const { state, sidebarMode } = useSidebar()
  return <div data-testid="probe" data-state={state} data-mode={sidebarMode} />
}

describe("sidebar default", () => {
  it("a new device starts pinned open on a desk", () => {
    expect(DEFAULT_SIDEBAR).toBe("pinned")
    render(
      <SidebarProvider>
        <Probe />
      </SidebarProvider>,
    )
    const probe = screen.getByTestId("probe")
    expect(probe).toHaveAttribute("data-mode", "pinned")
    expect(probe).toHaveAttribute("data-state", "expanded")
  })

  it("keeps a device's own stored choice", () => {
    store.set("crewship_sidebar_mode", "hover")
    render(
      <SidebarProvider>
        <Probe />
      </SidebarProvider>,
    )
    expect(screen.getByTestId("probe")).toHaveAttribute("data-mode", "hover")
    expect(screen.getByTestId("probe")).toHaveAttribute("data-state", "collapsed")
  })

  it("a tablet shows the rail even when the choice is pinned", async () => {
    setWidth(1024)
    render(
      <SidebarProvider>
        <Probe />
      </SidebarProvider>,
    )
    await act(async () => {})
    const probe = screen.getByTestId("probe")
    expect(probe).toHaveAttribute("data-mode", "pinned")
    expect(probe).toHaveAttribute("data-state", "collapsed")
  })
})

// The panel column starts at the top of the card with its own 48 px toolbar;
// the page's top bar spans the content only (NEW-HEADER, v67). The layout
// keeps a slot beside the content column and the panel renders into it.
describe("panel column", () => {
  beforeEach(() => {
    useAppStore.setState({ panelSlot: null, secondaryPanel: null, secondaryPanelOpen: false })
  })

  function withSlot() {
    const slot = document.createElement("div")
    slot.setAttribute("data-slot", "panel-slot")
    document.body.appendChild(slot)
    useAppStore.setState({ panelSlot: slot })
    return slot
  }

  it("renders into the layout's panel slot on a desk", () => {
    const slot = withSlot()
    render(
      <div data-testid="page">
        <SecondaryPanel id="issues" label="Issues views">
          <p>views</p>
        </SecondaryPanel>
        <main>content</main>
      </div>,
    )
    expect(slot.querySelector("[data-slot=secondary-panel]")).not.toBeNull()
    expect(screen.getByTestId("page").querySelector("[data-slot=secondary-panel]")).toBeNull()
    slot.remove()
  })

  it("docks a list page's panel beside the content on a tablet", async () => {
    setWidth(1024)
    const slot = withSlot()
    render(
      <SecondaryPanel id="inbox" label="Inbox views" kind="list">
        <p>queue</p>
      </SecondaryPanel>,
    )
    await act(async () => {})
    const panel = slot.querySelector("[data-slot=secondary-panel]")
    expect(panel).not.toBeNull()
    expect(panel).not.toHaveAttribute("data-float")
    expect(useAppStore.getState().secondaryPanel).toMatchObject({ id: "inbox", kind: "list" })
    slot.remove()
  })

  it("keeps a filter page's panel behind the top-bar button on a tablet", async () => {
    setWidth(1024)
    const slot = withSlot()
    render(
      <SecondaryPanel id="issues" label="Issues views">
        <p>views</p>
      </SecondaryPanel>,
    )
    await act(async () => {})
    expect(document.querySelector("[data-slot=secondary-panel]")).toBeNull()
    expect(useAppStore.getState().secondaryPanel).toMatchObject({ id: "issues", kind: "filter" })
    slot.remove()
  })

  it("the dashboard layout puts the panel slot beside the column that holds the top bar", () => {
    const layout = readFileSync(path.resolve(__dirname, "../../../app/(dashboard)/layout.tsx"), "utf8")
    const slotAt = layout.indexOf('data-slot="panel-slot"')
    const columnAt = layout.indexOf('data-slot="content-column"')
    const toolbarAt = layout.indexOf("<AppToolbar")
    expect(slotAt).toBeGreaterThan(-1)
    expect(columnAt).toBeGreaterThan(slotAt)
    expect(toolbarAt).toBeGreaterThan(columnAt)
  })
})

// v67 index.html:275-276: on a tablet the top bar's labelled actions become
// icon squares, so the path, the actions and both bells fit one 48 px row.
describe("top-bar actions on a tablet", () => {
  it("keeps the label for assistive tech and as a tooltip, shows the icon only below 1280", () => {
    render(
      <>
        <SubBarPrimary icon={Plus}>New issue</SubBarPrimary>
        <SubBarSecondary icon={Upload}>Import</SubBarSecondary>
      </>,
    )
    for (const name of ["New issue", "Import"]) {
      const button = screen.getByRole("button", { name })
      expect(button).toHaveAttribute("title", name)
      expect(button.querySelector("span")).toHaveClass("max-xl:sr-only")
    }
  })
})

// v67 phone top bar: the page's one primary is a 32 px blue square — on a
// touch screen its 44 px target is an invisible ring around it, not a
// bigger button filling the bar.
describe("phone primary", () => {
  it("draws a 32 px square with a 44 px hit area", () => {
    render(<SubBarPrimary icon={Plus}>New issue</SubBarPrimary>)
    const cls = screen.getByRole("button", { name: "New issue" }).className.split(" ")
    for (const c of ["max-md:size-8", "max-md:p-0", "coarse:max-md:min-h-0", "max-md:after:absolute", "max-md:after:-inset-1.5"]) expect(cls).toContain(c)
    expect(cls).not.toContain("coarse:max-xl:min-w-[44px]")
  })

  it("is what Issues puts in the phone bar, like every other area", () => {
    const src = readFileSync(path.resolve(__dirname, "../../features/orchestration/orchestration-layout.tsx"), "utf8")
    expect(src).not.toMatch(/SubBarIconButton icon=\{Plus\}/)
  })
})

// v67 core.js:345-348 and index.html .tabbar: the phone tab bar's More tab is
// a grid (it opens a grid of every place), and the bar is 62 px tall.
describe("phone tab bar", () => {
  it("draws More as a grid, not a hamburger", () => {
    const src = readFileSync(path.resolve(__dirname, "../mobile-tab-bar.tsx"), "utf8")
    expect(src).toMatch(/<LayoutGrid\b/)
    expect(src).not.toMatch(/<Menu\b/)
  })

  it("is 62 px plus the home-indicator inset", () => {
    const css = readFileSync(path.resolve(__dirname, "../../../app/globals.css"), "utf8")
    expect(css).toMatch(/--mobile-tab-bar-h:\s*calc\(62px \+ env\(safe-area-inset-bottom\)\)/)
  })
})

// v67 core.js:331-333: a phone detail has ONE bar — the back chevron first,
// then the item's own name — so the area's name gives way to it.
describe("phone detail header", () => {
  it("puts the way back first and names the item, not the area", () => {
    const src = readFileSync(path.resolve(__dirname, "../sub-bar.tsx"), "utf8")
    const toolbar = readFileSync(path.resolve(__dirname, "../app-toolbar.tsx"), "utf8")
    const css = readFileSync(path.resolve(__dirname, "../../../app/globals.css"), "utf8")
    expect(src).toMatch(/data-phone-back/)
    expect(toolbar).toMatch(/data-slot="area-crumb"/)
    expect(css).toMatch(/header:has\(\[data-phone-back\]\) \[data-slot="area-crumb"\]\s*\{\s*display:\s*none/)
  })
})
