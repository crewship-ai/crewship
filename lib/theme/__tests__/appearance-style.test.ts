import { describe, it, expect, beforeEach, afterEach, vi } from "vitest"

import {
  STYLE_PREFERENCE_KEY,
  STYLE_STORAGE_KEY,
  HARBOR,
  type StyleDef,
  parseStyleState,
  serializeStyleState,
  readStyleState,
  writeStyleState,
  clearStyleState,
  renderedStyle,
  planStyleSelection,
  allowedSchemes,
  randomStyle,
  STYLE_BOOT_SNIPPET,
} from "../appearance-style"

// Harbor 2 appearance model (#3334, APR-01–08, APR-11). The style lives in
// its own preference key so a client that predates it can never overwrite
// it; theme and accent stay in "appearance" and always win when rendering.

const NIGHT: StyleDef = { id: "night-test", schemes: ["dark"], accent: "indigo" }
const REG: StyleDef[] = [HARBOR, NIGHT]

let store: Map<string, string>
beforeEach(() => {
  store = new Map()
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
  })
  delete document.documentElement.dataset.style
})
afterEach(() => vi.unstubAllGlobals())

describe("keys", () => {
  it("the style has its own preference key next to appearance", () => {
    expect(STYLE_PREFERENCE_KEY).toBe("appearance.style")
    expect(STYLE_STORAGE_KEY).toBe("crewship-appearance-style")
  })
})

describe("parseStyleState / serialize (APR-01, APR-08)", () => {
  it("accepts a valid value", () => {
    expect(parseStyleState({ v: 1, style: "harbor", knobs: { radius: "round" } })).toEqual({ v: 1, style: "harbor", knobs: { radius: "round" } })
  })
  it("keeps an unknown style and unknown knobs so a newer client's choice survives a write", () => {
    const v = { v: 1, style: "from-the-future", knobs: { glow: "max", radius: "round" } }
    expect(serializeStyleState(parseStyleState(v)!)).toBe(JSON.stringify(v))
  })
  it("rejects what is not a style state", () => {
    for (const bad of [null, 1, "harbor", [], {}, { style: 3 }, { style: "x", knobs: "y" }, { style: "Bad Id!" }]) {
      expect(parseStyleState(bad)).toBeNull()
    }
  })
  it("drops knob values that are not short strings, keeps the rest", () => {
    expect(parseStyleState({ v: 1, style: "harbor", knobs: { a: "ok", b: 3, c: "x".repeat(65) } })?.knobs).toEqual({ a: "ok" })
  })
})

describe("local storage", () => {
  it("round-trips and clears", () => {
    expect(readStyleState()).toBeNull()
    writeStyleState({ v: 1, style: "harbor", knobs: {} })
    expect(readStyleState()).toEqual({ v: 1, style: "harbor", knobs: {} })
    clearStyleState()
    expect(readStyleState()).toBeNull()
  })
  it("ignores a corrupt stored value", () => {
    store.set(STYLE_STORAGE_KEY, "{not json")
    expect(readStyleState()).toBeNull()
  })
})

describe("renderedStyle (APR-02, APR-08, APR-10, APR-11)", () => {
  it("Harbor when nothing is stored", () => {
    expect(renderedStyle(null, "light", REG)).toBe("harbor")
  })
  it("a known style in a scheme it supports", () => {
    expect(renderedStyle({ v: 1, style: "night-test", knobs: {} }, "dark", REG)).toBe("night-test")
  })
  it("Harbor for an unknown style, without touching the stored value", () => {
    writeStyleState({ v: 1, style: "from-the-future", knobs: {} })
    expect(renderedStyle(readStyleState(), "dark", REG)).toBe("harbor")
    expect(readStyleState()?.style).toBe("from-the-future")
  })
  it("Harbor for a dark-only style shown in light (an inconsistent pair), stored value kept", () => {
    expect(renderedStyle({ v: 1, style: "night-test", knobs: {} }, "light", REG)).toBe("harbor")
  })
})

describe("planStyleSelection (APR-03, APR-11)", () => {
  it("writes the style and suggests its accent; theme only for a single-scheme style", () => {
    expect(planStyleSelection("harbor", { v: 1, style: "night-test", knobs: { a: "1" } }, "system", REG)).toEqual({
      style: { v: 1, style: "harbor", knobs: { a: "1" } },
      accent: "blue",
    })
    expect(planStyleSelection("night-test", null, "system", REG)).toEqual({
      style: { v: 1, style: "night-test", knobs: {} },
      accent: "indigo",
      theme: "dark",
    })
  })
  it("leaves the theme alone when the style already supports it", () => {
    expect(planStyleSelection("night-test", null, "dark", REG).theme).toBeUndefined()
  })
  it("refuses an unknown style", () => {
    expect(() => planStyleSelection("nope", null, "light", REG)).toThrow()
  })
})

describe("allowedSchemes (APR-11)", () => {
  it("a dark-only style allows only Dark; Harbor allows all three", () => {
    expect(allowedSchemes("night-test", REG)).toEqual(["dark"])
    expect(allowedSchemes("harbor", REG)).toEqual(["dark", "light", "system"])
    expect(allowedSchemes("from-the-future", REG)).toEqual(["dark", "light", "system"])
  })
})

describe("randomStyle (APR-04)", () => {
  it("picks a style and keeps the knobs map; never returns scheme, text size, motion or density", () => {
    const r = randomStyle(REG, () => 0.99, { radius: "round" })
    expect(Object.keys(r).sort()).toEqual(["knobs", "style", "v"])
    expect(r.style).toBe("night-test")
    expect(r.knobs).toEqual({ radius: "round" })
  })
})

describe("boot snippet", () => {
  it("paints data-style from storage before first paint, Harbor for anything unknown", () => {
    new Function(`var d=document.documentElement,s=localStorage;${STYLE_BOOT_SNIPPET}`)()
    expect(document.documentElement.dataset.style).toBe("harbor")
    store.set(STYLE_STORAGE_KEY, JSON.stringify({ v: 1, style: "x\" onload=\"alert(1)", knobs: {} }))
    new Function(`var d=document.documentElement,s=localStorage;${STYLE_BOOT_SNIPPET}`)()
    expect(document.documentElement.dataset.style).toBe("harbor")
  })
  it("stays inert inside an inline <script>", () => {
    expect(STYLE_BOOT_SNIPPET).not.toMatch(/<\/|<!--|\u2028|\u2029/)
  })
})
