import { describe, it, expect, beforeEach, afterEach, vi } from "vitest"
import { render, screen, fireEvent, act, cleanup } from "@testing-library/react"
import { SidebarProvider, Sidebar, useSidebar } from "../sidebar"

// Drives the popover lock from inside the sidebar, the same way the
// workspace switcher does via onOpenChange.
function PopoverControls() {
  const { setPopoverOpen } = useSidebar()
  return (
    <>
      <button onClick={() => setPopoverOpen(true)}>open-popover</button>
      <button onClick={() => setPopoverOpen(false)}>close-popover</button>
    </>
  )
}

function Harness() {
  return (
    <SidebarProvider>
      <Sidebar collapsible="icon">
        <PopoverControls />
      </Sidebar>
    </SidebarProvider>
  )
}

function getSidebar(): HTMLElement {
  const el = document.querySelector('[data-slot="sidebar"]')
  if (!el) throw new Error("sidebar element not found")
  return el as HTMLElement
}

describe("Sidebar hover-expand popover lock", () => {
  beforeEach(() => {
    // Not mobile; provide matchMedia for useIsMobile's effect.
    window.matchMedia = vi.fn().mockReturnValue({
      matches: false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }) as unknown as typeof window.matchMedia
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    cleanup()
  })

  it("keeps the sidebar hover-expanded while a popover is open, even after the cursor leaves", () => {
    render(<Harness />)
    const sidebar = getSidebar()

    // Hover in → expands after the 80ms debounce.
    act(() => {
      fireEvent.mouseEnter(sidebar)
    })
    act(() => {
      vi.advanceTimersByTime(100)
    })
    expect(sidebar).toHaveAttribute("data-hover", "true")

    // Open a popover anchored in the sidebar, then move the cursor out
    // (onto the portalled dropdown). The sidebar must stay expanded.
    act(() => {
      fireEvent.click(screen.getByText("open-popover"))
    })
    act(() => {
      fireEvent.mouseLeave(sidebar)
    })
    act(() => {
      vi.advanceTimersByTime(400)
    })
    expect(sidebar).toHaveAttribute("data-hover", "true")
  })

  it("collapses once the popover closes with the cursor outside the sidebar", () => {
    render(<Harness />)
    const sidebar = getSidebar()

    act(() => {
      fireEvent.mouseEnter(sidebar)
    })
    act(() => {
      vi.advanceTimersByTime(100)
    })

    act(() => {
      fireEvent.click(screen.getByText("open-popover"))
    })
    act(() => {
      fireEvent.mouseLeave(sidebar)
    })
    act(() => {
      vi.advanceTimersByTime(400)
    })
    expect(sidebar).toHaveAttribute("data-hover", "true")

    // Close the popover — cursor is outside, so it should collapse.
    act(() => {
      fireEvent.click(screen.getByText("close-popover"))
    })
    act(() => {
      vi.advanceTimersByTime(400)
    })
    expect(sidebar).not.toHaveAttribute("data-hover")
  })
})
