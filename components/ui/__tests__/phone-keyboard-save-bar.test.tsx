import { afterEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"

// Phone pilot (PILOT-TELEFON-STAV row 8): the bottom tab bar, the save bar and
// the on-screen keyboard never overlap. The save bars dock on every phone
// width (< 768, the app's phone breakpoint, not 640), and they and the toasts
// sit above the keyboard (visualViewport) and above the tab bar.

const seen = vi.hoisted(() => ({ props: {} as Record<string, unknown> }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("sonner", () => ({ Toaster: (p: Record<string, unknown>) => { seen.props = p; return null }, toast: { error: vi.fn() } }))
vi.mock("next-themes", () => ({ useTheme: () => ({ resolvedTheme: "dark" }) }))

import { PageSaveBar, PageSaveProvider, usePageSave } from "../page-save-bar"
import { SaveFooter } from "../save-footer"
import { Toaster } from "../sonner"
import { PhoneKeyboard } from "@/components/layout/phone-keyboard"

afterEach(() => {
  cleanup()
  delete document.documentElement.dataset.typing
  document.documentElement.style.removeProperty("--keyboard-inset")
})

function Dirty() {
  usePageSave({ label: "Workspace", count: 1, save: () => {}, discard: () => {} })
  return null
}

describe("save bars on a phone", () => {
  it("PageSaveBar docks below 768 px, above the tab bar and the keyboard", () => {
    render(<PageSaveProvider><Dirty /><PageSaveBar /></PageSaveProvider>)
    const bar = screen.getByRole("region", { name: "Unsaved changes" })
    expect(bar.className).toContain("max-md:fixed")
    expect(bar.className).not.toContain("max-sm:fixed")
    expect(bar.className).toContain("max-md:bottom-[calc(var(--mobile-tab-bar-h)+var(--keyboard-inset,0px))]")
  })

  it("SaveFooter docks below 768 px, above the tab bar and the keyboard", () => {
    const { container } = render(<SaveFooter dirty status="idle" onSave={vi.fn()} onCancel={vi.fn()} />)
    const root = container.firstElementChild as HTMLElement
    expect(root.className).toContain("max-md:fixed")
    expect(root.className).toContain("max-md:bottom-[calc(var(--mobile-tab-bar-h)+var(--keyboard-inset,0px))]")
    expect(root).toHaveAttribute("data-slot", "save-footer")
  })
})

describe("PhoneKeyboard", () => {
  it("marks the document while a field is focused, and clears it on blur", () => {
    render(<><PhoneKeyboard /><input aria-label="Name" /><button type="button">Plain</button></>)
    const input = screen.getByRole("textbox", { name: "Name" })
    act(() => { input.focus() })
    expect(document.documentElement).toHaveAttribute("data-typing")
    act(() => { screen.getByRole("button", { name: "Plain" }).focus() })
    expect(document.documentElement).not.toHaveAttribute("data-typing")
  })

  it("does not treat a checkbox as typing", () => {
    render(<><PhoneKeyboard /><input type="checkbox" aria-label="Agree" /></>)
    act(() => { screen.getByRole("checkbox", { name: "Agree" }).focus() })
    expect(document.documentElement).not.toHaveAttribute("data-typing")
  })

  it("publishes the keyboard's height from visualViewport as --keyboard-inset", () => {
    const listeners: Record<string, () => void> = {}
    const vv = { height: 400, offsetTop: 0, addEventListener: (t: string, f: () => void) => { listeners[t] = f }, removeEventListener: vi.fn() }
    const original = Object.getOwnPropertyDescriptor(window, "visualViewport")
    Object.defineProperty(window, "visualViewport", { value: vv, configurable: true })
    Object.defineProperty(window, "innerHeight", { value: 740, configurable: true })
    render(<PhoneKeyboard />)
    expect(document.documentElement.style.getPropertyValue("--keyboard-inset")).toBe("340px")
    vv.height = 740
    act(() => listeners.resize?.())
    expect(document.documentElement.style.getPropertyValue("--keyboard-inset")).toBe("0px")
    if (original) Object.defineProperty(window, "visualViewport", original)
    else delete (window as { visualViewport?: unknown }).visualViewport
  })

  it("leaves nothing behind when it unmounts", () => {
    const { unmount } = render(<><PhoneKeyboard /><input aria-label="Name" /></>)
    act(() => { fireEvent.focusIn(screen.getByRole("textbox", { name: "Name" })) })
    unmount()
    expect(document.documentElement).not.toHaveAttribute("data-typing")
  })
})

describe("Toaster on a phone", () => {
  it("lifts the toasts by --toast-lift (tab bar, keyboard, save bar)", () => {
    render(<Toaster />)
    const offset = seen.props.offset as { bottom: string }
    const mobile = seen.props.mobileOffset as { bottom: string }
    expect(offset.bottom).toContain("var(--toast-lift")
    expect(mobile.bottom).toContain("var(--toast-lift")
  })
})
