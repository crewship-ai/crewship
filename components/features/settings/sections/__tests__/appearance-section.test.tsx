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
const sync = vi.hoisted(() => ({ status: "synced" as "synced" | "pending" | "failed", retry: vi.fn() }))
vi.mock("@/hooks/use-appearance-sync", () => ({ useAppearanceSyncStatus: () => sync.status, retryAppearanceSync: () => sync.retry() }))

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
  delete document.documentElement.dataset.textSize
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


  it("offers the team colour as its own row, and says where it comes from", () => {
    render(<AppearanceSection />)
    expect(screen.getByTestId("workspace-accent-hint")).toHaveTextContent(/admin can set a brand colour/i)
    expect(screen.queryByRole("radio", { name: "Team colour" })).toBeNull()
    cleanup()
    workspaceHex = "#E11D48"
    render(<AppearanceSection />)
    expect(screen.getByTestId("workspace-accent-hint")).toHaveTextContent(/workspace's brand colour, adjusted/i)
    fireEvent.click(screen.getByRole("radio", { name: "Team colour" }))
    expect(document.documentElement.dataset.accent).toBe("workspace")
  })

  it("sets text size on <html> and keeps it with the appearance", () => {
    render(<AppearanceSection />)
    fireEvent.click(screen.getByRole("button", { name: "Large" }))
    expect(document.documentElement.dataset.textSize).toBe("large")
    expect(store.get("crewship-text-size")).toBe("large")
    // Standard is the earlier 14 px default; Small (13 px) is the Harbor 2
    // size a new device starts at. Not "Compact": that is the density name.
    fireEvent.click(screen.getByRole("button", { name: "Standard" }))
    expect(document.documentElement.dataset.textSize).toBeUndefined()
    fireEvent.click(screen.getByRole("button", { name: "Small" }))
    expect(document.documentElement.dataset.textSize).toBe("small")
  })

  it("chooses the sidebar mode through the synced store", () => {
    render(<AppearanceSection />)
    fireEvent.click(screen.getByRole("button", { name: "Pinned open" }))
    expect(store.get("crewship_sidebar_mode")).toBe("pinned")
  })

  it("previews the accent with real controls, and offers every preset as a tile", () => {
    render(<AppearanceSection />)
    expect(screen.getByRole("group", { name: "Accent preview" })).toHaveTextContent("Primary action")
    const tiles = screen.getByRole("radiogroup", { name: "Accent colour" }).querySelectorAll('[role="radio"]')
    expect(tiles).toHaveLength(ACCENTS.length)
  })

  it("has no base palette and no light-sidebar choice any more", () => {
    theme = "light"
    render(<AppearanceSection />)
    expect(screen.queryByRole("radiogroup", { name: "Base palette" })).toBeNull()
    expect(screen.queryByRole("radiogroup", { name: "Light mode sidebar" })).toBeNull()
  })

  it("shows Not synced with Retry only when a write was refused for good (#3334)", () => {
    sync.status = "synced"
    render(<AppearanceSection />)
    expect(screen.queryByText("Not synced")).toBeNull()
    cleanup()
    sync.status = "failed"
    render(<AppearanceSection />)
    expect(screen.getByRole("status")).toHaveTextContent("Not synced")
    fireEvent.click(screen.getByRole("button", { name: "Retry" }))
    expect(sync.retry).toHaveBeenCalledOnce()
    sync.status = "synced"
  })
})
