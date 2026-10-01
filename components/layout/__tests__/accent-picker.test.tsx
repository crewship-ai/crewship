import { describe, it, expect, beforeEach, afterEach, vi } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

import { AccentPicker } from "../theme-switcher"
import { ACCENTS, ACCENT_STORAGE_KEY } from "@/lib/theme/accents"

// One swatch per registered accent, the stored one pressed, and a press paints
// <html> at once and survives a reload.
describe("AccentPicker", () => {
  let store: Map<string, string>
  beforeEach(() => {
    store = new Map()
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
      clear: () => store.clear(),
    })
    document.documentElement.removeAttribute("data-accent")
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it("offers every registered accent, Blue pressed by default", () => {
    render(<AccentPicker />)
    expect(screen.getAllByRole("radio")).toHaveLength(ACCENTS.length)
    expect(screen.getByRole("radio", { name: "Blue" }).getAttribute("aria-checked")).toBe("true")
  })

  it("shows the stored accent as pressed", () => {
    store.set(ACCENT_STORAGE_KEY, "violet")
    render(<AccentPicker />)
    expect(screen.getByRole("radio", { name: "Violet" }).getAttribute("aria-checked")).toBe("true")
  })

  it("paints and remembers the chosen accent", () => {
    render(<AccentPicker />)
    fireEvent.click(screen.getByRole("radio", { name: "Teal" }))
    expect(document.documentElement.dataset.accent).toBe("teal")
    expect(store.get(ACCENT_STORAGE_KEY)).toBe("teal")
    expect(screen.getByRole("radio", { name: "Teal" }).getAttribute("aria-checked")).toBe("true")
  })
})
