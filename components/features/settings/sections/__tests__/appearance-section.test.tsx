import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

const setTheme = vi.fn()
let theme = "dark"
vi.mock("next-themes", () => ({ useTheme: () => ({ theme, setTheme }) }))

// The section itself never talks to the server — AppearanceSync in the shell
// does. A request from here would be a second writer.
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

let workspaceHex: string | null | undefined = null
vi.mock("@/components/layout/workspace-accent", () => ({ useWorkspaceAccentHex: () => workspaceHex }))

import { AppearanceSection } from "../appearance-section"
import { ACCENTS } from "@/lib/theme/accents"
import { APPEARANCE_SYNC_STORAGE_KEY, DENSITY_STORAGE_KEY, MOTION_STORAGE_KEY } from "@/lib/theme/appearance"

let store: Map<string, string>
beforeEach(() => {
  store = new Map()
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  })
  setTheme.mockReset()
  apiFetch.mockReset()
  theme = "dark"
  delete document.documentElement.dataset.density
  delete document.documentElement.dataset.motion
  delete document.documentElement.dataset.base
  workspaceHex = null
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("AppearanceSection", () => {
  it("renders the Theme, Accent and Layout & motion cards", () => {
    render(<AppearanceSection />)
    for (const name of ["Theme", "Accent", "Layout & motion"]) {
      expect(screen.getByRole("region", { name }), name).toBeTruthy()
    }
  })

  it("offers three theme tiles with the stored one pressed", () => {
    theme = "system"
    render(<AppearanceSection />)
    const group = screen.getByRole("radiogroup", { name: "Theme" })
    const tiles = Array.from(group.querySelectorAll("[role=radio]"))
    expect(tiles.map((t) => t.getAttribute("aria-label"))).toEqual(["Dark", "Light", "System"])
    expect(screen.getByRole("radio", { name: "System" }).getAttribute("aria-checked")).toBe("true")
    fireEvent.click(screen.getByRole("radio", { name: "Light" }))
    expect(setTheme).toHaveBeenCalledWith("light")
  })

  it("labels every accent swatch", () => {
    render(<AppearanceSection />)
    for (const { label } of ACCENTS) {
      expect(screen.getByRole("radio", { name: label })).toBeTruthy()
      expect(screen.getByText(label)).toBeTruthy()
    }
  })

  it("applies Compact density to <html> at once", () => {
    render(<AppearanceSection />)
    const density = screen.getByRole("group", { name: "Density" })
    fireEvent.click(density.querySelector("button:nth-child(2)")!)
    expect(document.documentElement.dataset.density).toBe("compact")
    expect(store.get(DENSITY_STORAGE_KEY)).toBe("compact")
    expect(screen.getByRole("button", { name: "Compact" }).getAttribute("aria-pressed")).toBe("true")
  })

  it("applies Reduce motion On to <html> at once", () => {
    render(<AppearanceSection />)
    fireEvent.click(screen.getByRole("button", { name: "On" }))
    expect(document.documentElement.dataset.motion).toBe("reduce")
    expect(store.get(MOTION_STORAGE_KEY)).toBe("on")
  })

  it("has sync on by default and remembers turning it off on this device", () => {
    render(<AppearanceSection />)
    const sw = screen.getByRole("switch", { name: "Sync across devices" })
    expect(sw.getAttribute("aria-checked")).toBe("true")
    fireEvent.click(sw)
    expect(store.get(APPEARANCE_SYNC_STORAGE_KEY)).toBe("0")
    expect(screen.getByRole("switch", { name: "Sync across devices" }).getAttribute("aria-checked")).toBe("false")
  })

  it("never calls the server itself", () => {
    render(<AppearanceSection />)
    fireEvent.click(screen.getByRole("button", { name: "Compact" }))
    fireEvent.click(screen.getByRole("radio", { name: "Teal" }))
    expect(apiFetch).not.toHaveBeenCalled()
  })

  it("picks a base palette and remembers it; Cool is the bare default", () => {
    render(<AppearanceSection />)
    const group = screen.getByRole("radiogroup", { name: "Base palette" })
    expect(group.querySelectorAll('[role="radio"]')).toHaveLength(4)
    fireEvent.click(screen.getByRole("radio", { name: /^Warm/ }))
    expect(document.documentElement.dataset.base).toBe("warm")
    expect(store.get("crewship-base")).toBe("warm")
    fireEvent.click(screen.getByRole("radio", { name: /^Cool/ }))
    expect(document.documentElement.dataset.base).toBeUndefined()
  })

  it("says where the Workspace accent comes from", () => {
    render(<AppearanceSection />)
    expect(screen.getByTestId("workspace-accent-hint")).toHaveTextContent(/admin can set a team brand colour/i)
    cleanup()
    workspaceHex = "#E11D48"
    render(<AppearanceSection />)
    expect(screen.getByTestId("workspace-accent-hint")).toHaveTextContent(/team's brand colour, adjusted/i)
    expect(screen.getByRole("radio", { name: "Workspace" })).toBeInTheDocument()
  })
})
