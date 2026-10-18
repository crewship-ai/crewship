import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, within } from "@testing-library/react"

const setTheme = vi.fn()
let theme = "dark"
vi.mock("next-themes", () => ({ useTheme: () => ({ theme, resolvedTheme: theme === "light" ? "light" : "dark", setTheme }) }))

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))
vi.mock("@/components/layout/workspace-accent", () => ({ useWorkspaceAccentHex: () => null }))
vi.mock("@/hooks/use-appearance-sync", () => ({ useAppearanceSyncStatus: () => "synced", retryAppearanceSync: () => {} }))

import { AppearanceSection } from "../appearance-section"
import { STYLES, STYLE_CATEGORIES, STYLE_STORAGE_KEY } from "@/lib/theme/appearance-style"
import { ACCENT_STORAGE_KEY } from "@/lib/theme/accents"

// Settings › Appearance › Style (STY-01, APR-03/04/11). Picking a style is a
// one-shot action: it writes the style and its suggested accent (and the
// theme, for a single-scheme style). The section never calls the server —
// the shell's AppearanceSync carries the style key.

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
  for (const a of ["data-style", "data-accent", "data-knob-radius", "data-knob-cards", "data-knob-lines"]) document.documentElement.removeAttribute(a)
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const stored = () => JSON.parse(store.get(STYLE_STORAGE_KEY) ?? "null")
const gallery = () => screen.getByRole("radiogroup", { name: "Style" })

describe("the gallery (STY-01)", () => {
  it("is a Style card above Theme, with one tile per style, grouped by category", () => {
    render(<AppearanceSection />)
    const style = screen.getByRole("region", { name: "Style" })
    const themeCard = screen.getByRole("region", { name: "Theme" })
    expect(style.compareDocumentPosition(themeCard) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    const tiles = within(gallery()).getAllByRole("radio")
    expect(tiles.map((t) => t.getAttribute("aria-label"))).toEqual(STYLES.map((s) => s.name))
    for (const c of STYLE_CATEGORIES) expect(within(gallery()).getByRole("heading", { name: c })).toBeTruthy()
  })

  it("Harbor is pressed by default and every tile carries a live preview in its own style", () => {
    render(<AppearanceSection />)
    expect(screen.getByRole("radio", { name: "Harbor" }).getAttribute("aria-checked")).toBe("true")
    for (const s of STYLES) {
      const preview = within(screen.getByRole("radio", { name: s.name })).getByTestId("style-preview")
      expect(preview.getAttribute("data-style"), s.name).toBe(s.id)
    }
  })

  it("a preview of a dark-only style is dark whatever the page is; others follow the page scheme", () => {
    theme = "light"
    render(<AppearanceSection />)
    expect(within(screen.getByRole("radio", { name: "Harbor Night" })).getByTestId("style-preview").classList.contains("dark")).toBe(true)
    expect(within(screen.getByRole("radio", { name: "Slate" })).getByTestId("style-preview").classList.contains("dark")).toBe(false)
  })

  it("shows the stored style as pressed, and Harbor for one it does not know", () => {
    store.set(STYLE_STORAGE_KEY, JSON.stringify({ v: 1, style: "slate", knobs: {} }))
    render(<AppearanceSection />)
    expect(screen.getByRole("radio", { name: "Slate" }).getAttribute("aria-checked")).toBe("true")
    cleanup()
    store.set(STYLE_STORAGE_KEY, JSON.stringify({ v: 1, style: "from-the-future", knobs: {} }))
    render(<AppearanceSection />)
    expect(screen.getByRole("radio", { name: "Harbor" }).getAttribute("aria-checked")).toBe("true")
    expect(stored().style).toBe("from-the-future")
  })
})

describe("picking a style (APR-03)", () => {
  it("writes the style and its suggested accent, paints at once, and never calls the server", () => {
    render(<AppearanceSection />)
    const slate = STYLES.find((s) => s.id === "slate")!
    fireEvent.click(screen.getByRole("radio", { name: "Slate" }))
    expect(stored()).toEqual({ v: 1, style: "slate", knobs: {} })
    expect(store.get(ACCENT_STORAGE_KEY)).toBe(slate.accent)
    expect(document.documentElement.getAttribute("data-style")).toBe("slate")
    expect(setTheme).not.toHaveBeenCalled()
    expect(apiFetch).not.toHaveBeenCalled()
    expect(screen.getByRole("radio", { name: "Slate" }).getAttribute("aria-checked")).toBe("true")
  })

  it("the accent stays the user's afterwards: choosing one does not change the style", () => {
    render(<AppearanceSection />)
    fireEvent.click(screen.getByRole("radio", { name: "Slate" }))
    fireEvent.click(screen.getByRole("radio", { name: "Teal" }))
    expect(stored().style).toBe("slate")
    expect(store.get(ACCENT_STORAGE_KEY)).toBe("teal")
  })

  it("keeps the knobs when another style is picked", () => {
    render(<AppearanceSection />)
    fireEvent.click(within(screen.getByRole("group", { name: "Corners" })).getByRole("button", { name: "Sharp" }))
    fireEvent.click(screen.getByRole("radio", { name: "Slate" }))
    expect(stored()).toEqual({ v: 1, style: "slate", knobs: { radius: "sharp" } })
  })
})

describe("a single-scheme style (APR-11)", () => {
  it("picking Harbor Night sets the theme to Dark", () => {
    theme = "light"
    render(<AppearanceSection />)
    fireEvent.click(screen.getByRole("radio", { name: "Harbor Night" }))
    expect(setTheme).toHaveBeenCalledWith("dark")
    expect(stored().style).toBe("harbor-night")
    expect(store.get(ACCENT_STORAGE_KEY)).toBe("sky")
  })

  it("while it is active, Light and System are disabled with a hint and Switch to Harbor", () => {
    store.set(STYLE_STORAGE_KEY, JSON.stringify({ v: 1, style: "harbor-night", knobs: {} }))
    render(<AppearanceSection />)
    expect(screen.getByRole("radio", { name: "Light" }).hasAttribute("disabled")).toBe(true)
    expect(screen.getByRole("radio", { name: "System" }).hasAttribute("disabled")).toBe(true)
    expect(screen.getByRole("radio", { name: "Dark" }).hasAttribute("disabled")).toBe(false)
    expect(screen.getByTestId("scheme-hint")).toHaveTextContent(/Harbor Night is a dark style/i)
    fireEvent.click(screen.getByRole("radio", { name: "Light" }))
    expect(setTheme).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Switch to Harbor" }))
    expect(stored().style).toBe("harbor")
    expect(store.get(ACCENT_STORAGE_KEY)).toBe("blue")
  })

  it("a style with both schemes disables nothing and shows no hint", () => {
    store.set(STYLE_STORAGE_KEY, JSON.stringify({ v: 1, style: "slate", knobs: {} }))
    render(<AppearanceSection />)
    for (const n of ["Dark", "Light", "System"]) expect(screen.getByRole("radio", { name: n }).hasAttribute("disabled")).toBe(false)
    expect(screen.queryByTestId("scheme-hint")).toBeNull()
  })
})

describe("reset and knobs (APR-04, STY-03)", () => {
  it("Reset style goes back to Harbor with default knobs; theme and accent stay", () => {
    store.set(STYLE_STORAGE_KEY, JSON.stringify({ v: 1, style: "slate", knobs: { radius: "round" } }))
    store.set(ACCENT_STORAGE_KEY, "teal")
    render(<AppearanceSection />)
    fireEvent.click(screen.getByRole("button", { name: "Reset style" }))
    expect(stored()).toEqual({ v: 1, style: "harbor", knobs: {} })
    expect(store.get(ACCENT_STORAGE_KEY)).toBe("teal")
    expect(setTheme).not.toHaveBeenCalled()
    expect(document.documentElement.hasAttribute("data-knob-radius")).toBe(false)
  })

  it("each knob changes only its own aspect", () => {
    render(<AppearanceSection />)
    fireEvent.click(within(screen.getByRole("group", { name: "Corners" })).getByRole("button", { name: "Round" }))
    expect(document.documentElement.getAttribute("data-knob-radius")).toBe("round")
    expect(document.documentElement.hasAttribute("data-knob-cards")).toBe(false)
    fireEvent.click(within(screen.getByRole("group", { name: "Cards" })).getByRole("button", { name: "Shadow" }))
    expect(document.documentElement.getAttribute("data-knob-cards")).toBe("shadow")
    expect(document.documentElement.getAttribute("data-knob-radius")).toBe("round")
    expect(stored().knobs).toEqual({ radius: "round", cards: "shadow" })
    expect(stored().style).toBe("harbor")
    fireEvent.click(within(screen.getByRole("group", { name: "Lines" })).getByRole("button", { name: "Strong" }))
    expect(stored().knobs).toEqual({ radius: "round", cards: "shadow", lines: "strong" })
    expect(store.get(ACCENT_STORAGE_KEY)).toBeUndefined()
  })

  it("the default option of a knob is pressed when nothing is stored", () => {
    render(<AppearanceSection />)
    expect(within(screen.getByRole("group", { name: "Corners" })).getByRole("button", { name: "Style default" }).getAttribute("aria-pressed")).toBe("true")
    expect(within(screen.getByRole("group", { name: "Cards" })).getByRole("button", { name: "Border" }).getAttribute("aria-pressed")).toBe("true")
  })
})

describe("phone and tablet (390 and 1024 px)", () => {
  it("the grid collapses to one column on a phone, two on a tablet, three on a desktop", () => {
    render(<AppearanceSection />)
    const grids = gallery().querySelectorAll("[data-testid='style-grid']")
    expect(grids.length).toBe(STYLE_CATEGORIES.length)
    for (const g of grids) {
      const cls = g.className
      expect(cls).toContain("grid-cols-1")
      expect(cls).toMatch(/\bsm:grid-cols-2\b/)
      expect(cls).toMatch(/\blg:grid-cols-3\b/)
    }
  })

  it("tiles and reset are 44 px on a coarse pointer, written in px", () => {
    render(<AppearanceSection />)
    for (const tile of within(gallery()).getAllByRole("radio")) expect(tile.className).toMatch(/\bcoarse:min-h-\[44px\]/)
    expect(screen.getByRole("button", { name: "Reset style" }).className).toMatch(/\bcoarse:h-\[44px\]/)
  })
})
