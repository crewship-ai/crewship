import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, render, screen } from "@testing-library/react"

// The app skeleton the owner chose on 2026-10-18 (v67 at every width):
// desktop starts with the sidebar pinned open; a tablet shows the rail, with a
// list page's panel docked beside the content; a phone has one top bar.

vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))

import { SidebarProvider, useSidebar } from "@/components/ui/sidebar"
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
