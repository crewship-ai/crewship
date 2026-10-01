import { describe, it, expect, beforeEach, afterEach, vi } from "vitest"
import { readFileSync } from "node:fs"
import path from "node:path"

import { ACCENTS, ACCENT_BOOT_SCRIPT, ACCENT_STORAGE_KEY, BRAND_TOKENS, DEFAULT_ACCENT, accentBlock, applyAccent, isAccentId } from "../accents"

const css = readFileSync(path.resolve(__dirname, "../../../app/styles/accents.css"), "utf8")

// The registry is what the picker offers; accents.css is what the browser
// paints. They must name the same accents, and every accent must define every
// brand token in both modes — a missing one would silently inherit Blue.
describe("accent registry and stylesheet", () => {
  it("has blue as the default, listed first", () => {
    expect(DEFAULT_ACCENT).toBe("blue")
    expect(ACCENTS[0].id).toBe("blue")
  })

  it.each(ACCENTS.map((a) => a.id))("%s defines every brand token in light and dark", (id) => {
    for (const mode of ["light", "dark"] as const) {
      const tokens = accentBlock(css, id, mode)
      expect([...tokens.keys()].sort(), `${id} ${mode}`).toEqual([...BRAND_TOKENS].sort())
    }
  })

  it("has no accent block the registry does not list", () => {
    const inCss = new Set([...css.matchAll(/data-accent="([a-z-]+)"/g)].map((m) => m[1]))
    expect([...inCss].sort()).toEqual(ACCENTS.map((a) => a.id).sort())
  })

  it("never uses a status hue as an accent", () => {
    expect(ACCENTS.map((a) => a.id)).not.toEqual(expect.arrayContaining(["green", "red", "amber", "yellow", "orange"]))
  })
})

describe("applying an accent", () => {
  // vitest.setup.ts stubs localStorage with no-op mocks; these tests need a
  // store that actually keeps what is written.
  beforeEach(() => {
    const store = new Map<string, string>()
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
      clear: () => store.clear(),
    })
    document.documentElement.removeAttribute("data-accent")
  })
  afterEach(() => vi.unstubAllGlobals())

  it("recognises only registered ids", () => {
    expect(isAccentId("violet")).toBe(true)
    expect(isAccentId("green")).toBe(false)
    expect(isAccentId(null)).toBe(false)
  })

  it("sets the attribute on <html> and remembers the choice", () => {
    applyAccent("teal")
    expect(document.documentElement.dataset.accent).toBe("teal")
    expect(localStorage.getItem(ACCENT_STORAGE_KEY)).toBe("teal")
  })

  it("boot script restores a stored accent before paint and ignores junk", () => {
    localStorage.setItem(ACCENT_STORAGE_KEY, "indigo")
    new Function(ACCENT_BOOT_SCRIPT)()
    expect(document.documentElement.dataset.accent).toBe("indigo")

    document.documentElement.removeAttribute("data-accent")
    localStorage.setItem(ACCENT_STORAGE_KEY, "url(javascript:alert(1))")
    new Function(ACCENT_BOOT_SCRIPT)()
    expect(document.documentElement.hasAttribute("data-accent")).toBe(false)
  })

  it("boot script cannot close its own <script> tag", () => {
    expect(ACCENT_BOOT_SCRIPT).not.toMatch(/<\/|<!--|\u2028|\u2029/)
  })
})
