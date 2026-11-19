import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, act, within } from "@testing-library/react"

const setTheme = vi.fn()
let theme = "dark"
vi.mock("next-themes", () => ({ useTheme: () => ({ theme, setTheme }) }))

// The section itself never talks to the server — AppearanceSync in the shell
// does. A request from here would be a second writer.
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

const toastMock = vi.hoisted(() => Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), dismiss: vi.fn() }))
vi.mock("@/lib/toast", () => ({ toast: toastMock }))

let workspaceHex: string | null | undefined = null
vi.mock("@/components/layout/workspace-accent", () => ({ useWorkspaceAccentHex: () => workspaceHex }))
const sync = vi.hoisted(() => ({ status: "synced" as "synced" | "pending" | "failed", retry: vi.fn() }))
vi.mock("@/hooks/use-appearance-sync", () => ({ useAppearanceSyncStatus: () => sync.status, retryAppearanceSync: () => sync.retry() }))

import { AppearanceSection } from "../appearance-section"
import { ACCENTS } from "@/lib/theme/accents"
import { readExplicit } from "@/lib/theme/appearance-explicit"
import { readStyleState } from "@/lib/theme/appearance-style"
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
  toastMock.mockReset()
  apiFetch.mockReset()
  theme = "dark"
  delete document.documentElement.dataset.density
  delete document.documentElement.dataset.accent
  delete document.documentElement.dataset.motion
  delete document.documentElement.dataset.textSize
  workspaceHex = null
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("AppearanceSection", () => {
  it("is Your look, then Colours, Text and layout, Motion, Devices and workspace, in that order", () => {
    render(<AppearanceSection />)
    const names = ["Your look", "Colours", "Text and layout", "Motion", "Devices and workspace"]
    const regions = names.map((name) => screen.getByRole("region", { name }))
    regions.slice(1).forEach((r, i) => expect(regions[i].compareDocumentPosition(r) & Node.DOCUMENT_POSITION_FOLLOWING, names[i + 1]).toBeTruthy())
    for (const gone of ["Style", "Theme", "Accent", "Layout & motion", "Workspace default"]) expect(screen.queryByRole("region", { name: gone }), gone).toBeNull()
  })

  it("offers three theme tiles with the stored one pressed", () => {
    theme = "system"
    render(<AppearanceSection />)
    const group = screen.getByRole("radiogroup", { name: "Theme" })
    const tiles = Array.from(group.querySelectorAll("[role=radio]"))
    expect(tiles.map((t) => t.getAttribute("aria-label"))).toEqual(["Light", "Dark", "System"])
    expect(screen.getByRole("radio", { name: "System" }).getAttribute("aria-checked")).toBe("true")
    fireEvent.click(screen.getByRole("radio", { name: "Light" }))
    expect(setTheme).toHaveBeenCalledWith("light")
  })

  // v67 `.swatches`: named by tooltip and accessible name only (PAR-097).
  it("labels every accent swatch", () => {
    render(<AppearanceSection />)
    const group = screen.getByRole("radiogroup", { name: "Accent colour" })
    for (const { label } of ACCENTS) {
      expect(screen.getByRole("radio", { name: label })).toHaveAttribute("title", label)
      expect(within(group).queryByText(label)).toBeNull()
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


  it("offers the team colour as the ninth swatch, and says where it comes from", () => {
    render(<AppearanceSection />)
    const without = screen.getByRole("radio", { name: "Team colour" })
    expect(without).toBeDisabled()
    expect(without.getAttribute("title")).toMatch(/admin can set one in General › Artifacts appearance/i)
    cleanup()
    workspaceHex = "#E11D48"
    render(<AppearanceSection />)
    const team = screen.getByRole("radio", { name: "Team colour" })
    expect(team).toBeEnabled()
    expect(team.getAttribute("title")).toContain("the workspace brand colour")
    fireEvent.click(team)
    expect(document.documentElement.dataset.accent).toBe("workspace")
    expect(screen.getByTestId("accent-name")).toHaveTextContent("Team colour")
  })

  it("names the current accent in the hint, including an accent only a style carries", () => {
    render(<AppearanceSection />)
    expect(screen.getByTestId("accent-name")).toHaveTextContent("Blue")
    fireEvent.click(screen.getByRole("radio", { name: "Teal" }))
    expect(screen.getByTestId("accent-name")).toHaveTextContent("Teal")
    fireEvent.click(screen.getByRole("button", { name: "Change style" }))
    fireEvent.click(screen.getByRole("radio", { name: "Terracotta" }))
    expect(screen.getByTestId("accent-name")).toHaveTextContent("Clay")
    expect(screen.getByTestId("look-summary")).toHaveTextContent("Terracotta · Clay ·")
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
    // ODCH-SET-02: the names the rail's own menu and ⌘B use, in its order.
    expect(["Expanded", "Expand on hover", "Collapsed"].map((n) => screen.getByRole("button", { name: n }))).toHaveLength(3)
    expect(screen.queryByRole("button", { name: "Pinned open" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Collapsed" }))
    expect(store.get("crewship_sidebar_mode")).toBe("collapsed")
    fireEvent.click(screen.getByRole("button", { name: "Expanded" }))
    expect(store.get("crewship_sidebar_mode")).toBe("pinned")
  })

  it("offers every preset as a swatch, and no separate accent preview any more", () => {
    render(<AppearanceSection />)
    expect(screen.queryByRole("group", { name: "Accent preview" })).toBeNull()
    const tiles = screen.getByRole("radiogroup", { name: "Accent colour" }).querySelectorAll('[role="radio"]')
    expect(tiles).toHaveLength(ACCENTS.length + 1)
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

// Personal appearance saves as you make it, and the last change can be taken
// back at once. Only the workspace default (a shared value) needs a button.
describe("AppearanceSection autosave and Undo", () => {
  const lastUndo = () => {
    const call = toastMock.mock.calls.at(-1)
    expect(call, "a toast follows the change").toBeTruthy()
    const action = call![1]?.action as { label: string; onClick: () => void } | undefined
    expect(action?.label).toBe("Undo")
    return action!
  }

  it("says it once, in the line under the look: it applies at once", () => {
    render(<AppearanceSection />)
    expect(screen.queryByTestId("appearance-autosave")).toBeNull()
    expect(screen.getByTestId("look-source")).toHaveTextContent("applies at once")
  })

  it("offers Undo after an accent change and puts the accent and the inherited mark back", () => {
    render(<AppearanceSection />)
    const before = document.documentElement.dataset.accent ?? "blue"
    fireEvent.click(screen.getByRole("radio", { name: "Teal" }))
    expect(document.documentElement.dataset.accent).toBe("teal")
    expect(readExplicit().has("accent")).toBe(true)
    act(() => lastUndo().onClick())
    expect(document.documentElement.dataset.accent).toBe(before)
    expect(readExplicit().has("accent")).toBe(false)
  })

  it("Undo of a theme change restores the previous theme and the mark", () => {
    render(<AppearanceSection />)
    fireEvent.click(screen.getByRole("radio", { name: "Light" }))
    expect(setTheme).toHaveBeenLastCalledWith("light")
    expect(readExplicit().has("theme")).toBe(true)
    act(() => lastUndo().onClick())
    expect(setTheme).toHaveBeenLastCalledWith("dark")
    expect(readExplicit().has("theme")).toBe(false)
  })

  it("Undo of a density change restores it", () => {
    render(<AppearanceSection />)
    fireEvent.click(screen.getByRole("button", { name: "Compact" }))
    expect(document.documentElement.dataset.density).toBe("compact")
    act(() => lastUndo().onClick())
    expect(document.documentElement.dataset.density).not.toBe("compact")
    expect(readExplicit().has("density")).toBe(false)
  })

  it("Undo of a style pick restores the style, its accent and its marks", () => {
    render(<AppearanceSection />)
    const before = document.documentElement.dataset.accent ?? "blue"
    fireEvent.click(screen.getByRole("button", { name: "Change style" }))
    fireEvent.click(screen.getByRole("radio", { name: /^Plum$/ }))
    expect(readStyleState()?.style).toBe("plum")
    act(() => lastUndo().onClick())
    expect(readStyleState()).toBeNull()
    expect(document.documentElement.dataset.accent).toBe(before)
    expect(readExplicit().has("style")).toBe(false)
  })

  it("only the last change is undone", () => {
    render(<AppearanceSection />)
    fireEvent.click(screen.getByRole("radio", { name: "Teal" }))
    fireEvent.click(screen.getByRole("button", { name: "Compact" }))
    act(() => lastUndo().onClick())
    expect(document.documentElement.dataset.density).not.toBe("compact")
    expect(document.documentElement.dataset.accent).toBe("teal")
  })

  it("never writes the shared workspace default by itself", () => {
    render(<AppearanceSection />)
    fireEvent.click(screen.getByRole("radio", { name: "Teal" }))
    fireEvent.click(screen.getByRole("radio", { name: "Light" }))
    act(() => lastUndo().onClick())
    expect(apiFetch).not.toHaveBeenCalled()
  })
})
