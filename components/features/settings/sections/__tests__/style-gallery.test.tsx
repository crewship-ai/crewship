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
import { STYLES, STYLE_LOOKS, STYLE_STORAGE_KEY } from "@/lib/theme/appearance-style"
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

const preview = () => screen.getByTestId("style-preview")
const filters = () => screen.getByRole("group", { name: "Looks" })
// The gallery is closed until "Change style" opens it, inline under the Style row.
const renderOpen = () => {
  const view = render(<AppearanceSection />)
  fireEvent.click(screen.getByRole("button", { name: "Change style" }))
  return view
}

// The Style row names the chosen style; "Change style" opens the gallery
// inline: chips for the looks (with counts) and a compact grid of palettes.
// The live preview is the "Your look" line.

describe("Gaming packs (#3790)", () => {
  it("lists Afterglow under Made for dark", () => {
    renderOpen()
    fireEvent.click(within(filters()).getByRole("button", { name: /^Made for dark/ }))
    expect(within(gallery()).getByRole("radio", { name: "Afterglow" })).toBeTruthy()
  })

  it("picking Afterglow stores it, moves the theme to Dark and keeps the sail-blue accent", () => {
    theme = "light"
    renderOpen()
    fireEvent.click(screen.getByRole("radio", { name: "Afterglow" }))
    expect(stored().style).toBe("afterglow")
    expect(setTheme).toHaveBeenCalledWith("dark")
    expect(document.documentElement.getAttribute("data-style")).toBe("afterglow")
    expect(document.documentElement.getAttribute("data-accent")).toBe("blue")
  })
})
describe("the gallery (STY-01)", () => {
  it("is closed at first: the Style row names the chosen style and its blurb, with Change style", () => {
    render(<AppearanceSection />)
    expect(screen.queryByRole("radiogroup", { name: "Style" })).toBeNull()
    const open = screen.getByRole("button", { name: "Change style" })
    expect(open.getAttribute("aria-expanded")).toBe("false")
    expect(screen.getByText(STYLES[0].blurb, { exact: false })).toBeTruthy()
    fireEvent.click(open)
    expect(screen.getByRole("button", { name: "Done" }).getAttribute("aria-expanded")).toBe("true")
    expect(gallery()).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Done" }))
    expect(screen.queryByRole("radiogroup", { name: "Style" })).toBeNull()
  })

  it("sits inside the Colours card, with one compact palette per style", () => {
    renderOpen()
    expect(within(screen.getByRole("region", { name: "Colours" })).getByRole("radiogroup", { name: "Style" })).toBeTruthy()
    const tiles = within(gallery()).getAllByRole("radio")
    expect(tiles.map((t) => t.getAttribute("aria-label")).sort()).toEqual(STYLES.map((s) => s.name).sort())
    for (const s of STYLES) {
      const tile = screen.getByRole("radio", { name: s.name })
      const swatches = within(tile).getAllByTestId("style-swatch")
      expect(swatches.map((w) => w.style.background), s.name).toHaveLength(4)
      // The blurb is the tile's tooltip, not on every tile.
      expect(tile.textContent).not.toContain(s.blurb)
    }
  })

  it("groups by look: All, Neutral, Warm, Violet, Made for dark, with counts; All shows every style again", () => {
    renderOpen()
    expect(within(filters()).getAllByRole("button").map((b) => b.textContent)).toEqual(["All", ...STYLE_LOOKS.map((l) => `${l.label}${l.styles.length}`)])
    expect(within(filters()).getByRole("button", { name: "All" }).getAttribute("aria-pressed")).toBe("true")
    fireEvent.click(within(filters()).getByRole("button", { name: /^Warm/ }))
    expect(within(filters()).getByRole("button", { name: /^Warm/ }).getAttribute("aria-pressed")).toBe("true")
    const warm = STYLE_LOOKS.find((l) => l.id === "warm")!.styles
    expect(within(gallery()).getAllByRole("radio").map((t) => t.getAttribute("aria-label"))).toEqual(warm.map((id) => STYLES.find((s) => s.id === id)!.name))
    fireEvent.click(within(filters()).getByRole("button", { name: "All" }))
    expect(within(gallery()).getAllByRole("radio")).toHaveLength(STYLES.length)
  })

  it("Harbor is pressed by default and the Your look line previews the chosen style", () => {
    renderOpen()
    expect(screen.getByRole("radio", { name: "Harbor" }).getAttribute("aria-checked")).toBe("true")
    expect(screen.getAllByTestId("style-preview")).toHaveLength(1)
    expect(preview().getAttribute("data-style")).toBe("harbor")
    expect(screen.getByTestId("look-summary")).toHaveTextContent(/^Harbor · Blue · /)
  })

  it("the preview shows the stored style; a dark-only style previews dark, others follow the page", () => {
    store.set(STYLE_STORAGE_KEY, JSON.stringify({ v: 1, style: "harbor-night", knobs: {} }))
    theme = "dark"
    render(<AppearanceSection />)
    expect(preview().getAttribute("data-style")).toBe("harbor-night")
    expect(preview().classList.contains("dark")).toBe(true)
    cleanup()
    store.set(STYLE_STORAGE_KEY, JSON.stringify({ v: 1, style: "slate", knobs: {} }))
    theme = "light"
    render(<AppearanceSection />)
    expect(preview().getAttribute("data-style")).toBe("slate")
    expect(preview().classList.contains("dark")).toBe(false)
  })

  it("shows the stored style as pressed, and Harbor for one it does not know", () => {
    store.set(STYLE_STORAGE_KEY, JSON.stringify({ v: 1, style: "slate", knobs: {} }))
    renderOpen()
    expect(screen.getByRole("radio", { name: "Slate" }).getAttribute("aria-checked")).toBe("true")
    cleanup()
    store.set(STYLE_STORAGE_KEY, JSON.stringify({ v: 1, style: "from-the-future", knobs: {} }))
    renderOpen()
    expect(screen.getByRole("radio", { name: "Harbor" }).getAttribute("aria-checked")).toBe("true")
    expect(stored().style).toBe("from-the-future")
  })
})

describe("picking a style (APR-03)", () => {
  it("writes the style and its suggested accent, paints at once, and never calls the server", () => {
    renderOpen()
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
    renderOpen()
    fireEvent.click(screen.getByRole("radio", { name: "Slate" }))
    fireEvent.click(screen.getByRole("radio", { name: "Teal" }))
    expect(stored().style).toBe("slate")
    expect(store.get(ACCENT_STORAGE_KEY)).toBe("teal")
  })

  it("keeps the knobs when another style is picked", () => {
    renderOpen()
    fireEvent.click(within(screen.getByRole("group", { name: "Corners" })).getByRole("button", { name: "Sharp" }))
    fireEvent.click(screen.getByRole("radio", { name: "Slate" }))
    expect(stored()).toEqual({ v: 1, style: "slate", knobs: { radius: "sharp" } })
  })
})

describe("a single-scheme style (APR-11)", () => {
  it("picking Harbor Night sets the theme to Dark", () => {
    theme = "light"
    renderOpen()
    fireEvent.click(screen.getByRole("radio", { name: "Harbor Night" }))
    expect(setTheme).toHaveBeenCalledWith("dark")
    expect(stored().style).toBe("harbor-night")
    expect(store.get(ACCENT_STORAGE_KEY)).toBe("sky")
  })

  it("while it is active, Light and System are disabled with a hint and Switch to Harbor", () => {
    store.set(STYLE_STORAGE_KEY, JSON.stringify({ v: 1, style: "harbor-night", knobs: {} }))
    renderOpen()
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
    renderOpen()
    for (const n of ["Dark", "Light", "System"]) expect(screen.getByRole("radio", { name: n }).hasAttribute("disabled")).toBe(false)
    expect(screen.queryByTestId("scheme-hint")).toBeNull()
  })
})

describe("reset and knobs (APR-04, STY-03)", () => {
  it("Reset to default goes back to the built-in look: forgets the style, accent and theme the person chose", () => {
    store.set(STYLE_STORAGE_KEY, JSON.stringify({ v: 1, style: "slate", knobs: { radius: "round" } }))
    store.set("crewship-appearance-explicit", JSON.stringify(["style", "accent"]))
    render(<AppearanceSection />)
    fireEvent.click(screen.getByRole("button", { name: "Reset to default" }))
    expect(JSON.parse(store.get("crewship-appearance-explicit")!)).toEqual([])
    expect(screen.getByRole("button", { name: "Reset to default" })).toBeDisabled()
  })

  it("the corners, cards and lines rows are in the gallery with the prototype's labels", () => {
    renderOpen()
    const labels = (g: string) => within(screen.getByRole("group", { name: g })).getAllByRole("button").map((b) => b.textContent)
    expect(labels("Corners")).toEqual(["As the style", "Sharp", "Round"])
    expect(labels("Cards")).toEqual(["Outlined", "Raised"])
    expect(labels("Lines")).toEqual(["Subtle", "Normal", "Strong"])
  })

  it("each knob changes only its own aspect", () => {
    renderOpen()
    fireEvent.click(within(screen.getByRole("group", { name: "Corners" })).getByRole("button", { name: "Round" }))
    expect(document.documentElement.getAttribute("data-knob-radius")).toBe("round")
    expect(document.documentElement.hasAttribute("data-knob-cards")).toBe(false)
    fireEvent.click(within(screen.getByRole("group", { name: "Cards" })).getByRole("button", { name: "Raised" }))
    expect(document.documentElement.getAttribute("data-knob-cards")).toBe("shadow")
    expect(document.documentElement.getAttribute("data-knob-radius")).toBe("round")
    expect(stored().knobs).toEqual({ radius: "round", cards: "shadow" })
    expect(stored().style).toBe("harbor")
    fireEvent.click(within(screen.getByRole("group", { name: "Lines" })).getByRole("button", { name: "Strong" }))
    expect(stored().knobs).toEqual({ radius: "round", cards: "shadow", lines: "strong" })
    expect(store.get(ACCENT_STORAGE_KEY)).toBeUndefined()
  })

  it("the default option of a knob is pressed when nothing is stored", () => {
    renderOpen()
    expect(within(screen.getByRole("group", { name: "Corners" })).getByRole("button", { name: "As the style" }).getAttribute("aria-pressed")).toBe("true")
    expect(within(screen.getByRole("group", { name: "Cards" })).getByRole("button", { name: "Outlined" }).getAttribute("aria-pressed")).toBe("true")
  })
})

describe("phone and tablet (390 and 1024 px)", () => {
  it("the palettes follow the card's width, not the window's: two, then four, then six to a row", () => {
    renderOpen()
    // A tablet's Settings column is narrow at a desk-sized window, so the
    // grid reads its container (Tailwind container queries).
    expect(gallery().parentElement!.className).toMatch(/(^|\s)@container(\s|$)/)
    const cls = gallery().className
    expect(gallery().getAttribute("data-testid")).toBe("style-grid")
    expect(cls).toContain("grid-cols-2")
    expect(cls).toMatch(/(^|\s)@lg:grid-cols-4(\s|$)/)
    expect(cls).toMatch(/(^|\s)@2xl:grid-cols-6(\s|$)/)
    expect(cls).not.toMatch(/(^|\s)(sm|lg):grid-cols-/)
  })

  it("tiles, chips and the reset are 44 px on a coarse pointer, written in px", () => {
    renderOpen()
    for (const tile of within(gallery()).getAllByRole("radio")) expect(tile.className).toMatch(/\bcoarse:min-h-\[44px\]/)
    for (const chip of within(filters()).getAllByRole("button")) expect(chip.className).toMatch(/\bcoarse:min-h-\[44px\]/)
    expect(screen.getByRole("button", { name: "Reset to default" }).className).toMatch(/\bcoarse:h-\[44px\]/)
    expect(screen.getByRole("button", { name: "Done" }).className).toMatch(/\bcoarse:min-h-\[44px\]/)
  })
})
